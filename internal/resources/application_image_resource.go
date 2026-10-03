package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // image.DecodeConfig for the pixel limit
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/valuefree"
)

var (
	_ resource.Resource                   = &applicationImageResource{}
	_ resource.ResourceWithConfigure      = &applicationImageResource{}
	_ resource.ResourceWithImportState    = &applicationImageResource{}
	_ resource.ResourceWithModifyPlan     = &applicationImageResource{}
	_ resource.ResourceWithValidateConfig = &applicationImageResource{}
)

func init() { register(NewApplicationImageResource) }

// NewApplicationImageResource returns the pocketid_application_image resource.
func NewApplicationImageResource() resource.Resource {
	return &applicationImageResource{}
}

type applicationImageResource struct {
	client *client.Client
}

type applicationImageModel struct {
	ID     types.String `tfsdk:"id"`
	Kind   types.String `tfsdk:"kind"`
	Source types.String `tfsdk:"source"`
	SHA256 types.String `tfsdk:"sha256"`
}

// applicationImageMaxPixels is this provider's limit on a JPEG or PNG image,
// on every server version, like its size limit. It is the limit Pocket ID
// 2.15.0 and later apply (utils/image: maxImagePixels, checked with
// image.DecodeConfig); 2.14.0 has none.
const applicationImageMaxPixels = 16_000_000

// applicationImageStoredKey is the private-state key holding the SHA-256 of
// the image Pocket ID served right after this resource uploaded it. Pocket
// ID strips metadata from JPEG, PNG and WebP files, so this can differ from
// the hash of source; a later read serving something else means the image
// was replaced outside Terraform.
const applicationImageStoredKey = "served_sha256"

func (r *applicationImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_application_image"
}

func applicationImageKinds() []string {
	kinds := make([]string, 0, 6)
	for _, kind := range client.ApplicationImages() {
		kinds = append(kinds, string(kind))
	}
	return kinds
}

func (r *applicationImageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	defer func() { resp.Schema = valuefree.ResourceSchema(resp.Schema) }()
	var kinds strings.Builder
	for _, kind := range client.ApplicationImages() {
		removal := "destroying the resource removes it"
		if !kind.Deletable() {
			removal = "Pocket ID cannot remove it, so destroying the resource leaves it in place"
		}
		fmt.Fprintf(&kinds, "\n  - `%s`: %s; %s.", kind, strings.Join(kind.Extensions(), ", "), removal)
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Uploads one of Pocket ID's application images (logos, e-mail logo, background, favicon or default profile picture) " +
			"from a local file. Use one resource per image. The file is uploaded again when its content changes, and when Pocket ID " +
			"holds a different image than the one this resource uploaded (it was replaced or removed outside Terraform). " +
			"Each read adds a random `nocache` query parameter and asks caches to revalidate (`Cache-Control: no-cache`), so a " +
			"cache in front of Pocket ID that keys on the whole URL or honors that header never answers it; if your cache does " +
			"neither, exclude `/api/application-images/` from caching, or the provider may compare against an older image and " +
			"upload yours again.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The image, the same as `kind`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"kind": schema.StringAttribute{
				MarkdownDescription: "Which image to set, and the file types Pocket ID accepts for it:" + kinds.String() +
					"\n\n  Destroying a removable image does not bring back Pocket ID's bundled one: without an uploaded image, " +
					"the logos fall back to Pocket ID's built-in logo (2.15.0 and later), and the background and default profile " +
					"picture are absent. Changing `kind` replaces the resource.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.OneOf(applicationImageKinds()...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"source": schema.StringAttribute{
				MarkdownDescription: fmt.Sprintf("Path of the image file to upload. Pocket ID takes the image's type from the file "+
					"name's extension (any case), which must be one `kind` accepts. The file is uploaded again when its content "+
					"or its extension changes; another path to the same content with the same extension is not uploaded. "+
					"This provider uploads files of at most %d bytes, and JPEG and PNG images of at most %d pixels, whatever the "+
					"server version (Pocket ID itself has no size limit for these images; 2.15.0 and later refuse JPEG and PNG "+
					"images over that pixel count, 2.14.0 does not).", client.MaxApplicationImageBytes, applicationImageMaxPixels),
				Required:   true,
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"sha256": schema.StringAttribute{
				MarkdownDescription: "SHA-256 (hex) of the content of `source` as last uploaded. When Pocket ID serves a different " +
					"image than the one this resource uploaded, a refresh sets it to the SHA-256 of the image Pocket ID serves, so " +
					"the next plan uploads `source` again.",
				Computed: true,
			},
		},
	}
}

func (r *applicationImageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	r.client = c
}

// applicationImageExtension returns source's extension in lower case, the
// way Pocket ID reads it from the uploaded file name.
func applicationImageExtension(source string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(source), "."))
}

func applicationImageExtensionProblem(kind client.ApplicationImage, source string) string {
	extension := applicationImageExtension(source)
	for _, allowed := range kind.Extensions() {
		if extension == allowed {
			return ""
		}
	}
	return fmt.Sprintf("Pocket ID takes the image type from the file extension, and accepts only %s for the %s; the source file's extension is not one of them (its name is not shown).",
		strings.Join(kind.Extensions(), ", "), kind)
}

// ValidateConfig checks the source's extension against the kind.
func (r *applicationImageResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config applicationImageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.Kind.IsNull() || config.Kind.IsUnknown() || config.Source.IsNull() || config.Source.IsUnknown() {
		return
	}
	kind := client.ApplicationImage(config.Kind.ValueString())
	if !kind.Valid() {
		return // the kind's own validator reports it
	}
	if problem := applicationImageExtensionProblem(kind, config.Source.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(path.Root("source"), "Unsupported image file type", problem)
	}
}

// readApplicationImageSource reads source and checks it against Pocket ID's
// limits. It returns the content and its SHA-256 in hex.
func readApplicationImageSource(kind client.ApplicationImage, source string) ([]byte, string, error) {
	if problem := applicationImageExtensionProblem(kind, source); problem != "" {
		return nil, "", errors.New(problem)
	}
	file, err := os.Open(source) // #nosec G304 -- the configured file is the input
	if err != nil {
		return nil, "", localFileError(err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, client.MaxApplicationImageBytes+1))
	if err != nil {
		return nil, "", localFileError(err)
	}
	if len(content) > client.MaxApplicationImageBytes {
		return nil, "", fmt.Errorf("the file is larger than %d bytes, the most this provider uploads", client.MaxApplicationImageBytes)
	}
	switch applicationImageExtension(source) {
	case "jpg", "jpeg", "png":
		// Pocket ID 2.15.0+ refuses an image with too many pixels; one it
		// cannot decode is accepted as it is, so it is not refused here
		// either.
		if config, _, err := image.DecodeConfig(bytes.NewReader(content)); err == nil && int64(config.Width)*int64(config.Height) > applicationImageMaxPixels {
			return nil, "", fmt.Errorf("the image is %dx%d pixels; this provider uploads JPEG and PNG images of at most %d pixels (Pocket ID 2.15.0 and later refuse larger ones)", config.Width, config.Height, applicationImageMaxPixels)
		}
	}
	return content, appImageSHA256Hex(content), nil
}

func appImageSHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// ModifyPlan plans sha256 from the file's current content, so a changed file
// plans an upload, and warns before destroying an image Pocket ID cannot
// remove.
func (r *applicationImageResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		var state applicationImageModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if !resp.Diagnostics.HasError() && !client.ApplicationImage(state.Kind.ValueString()).Deletable() {
			resp.Diagnostics.AddWarning("The image stays in Pocket ID",
				fmt.Sprintf("Pocket ID has no way to remove the %s, so destroying this resource leaves the image in place; only Terraform stops managing it. Upload another image to change it.", state.Kind.ValueString()))
		}
		return
	}
	var plan applicationImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	kind := client.ApplicationImage(plan.Kind.ValueString())
	if plan.Kind.IsUnknown() || plan.Source.IsUnknown() || !kind.Valid() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("sha256"), types.StringUnknown())...)
		return
	}
	_, hash, err := readApplicationImageSource(kind, plan.Source.ValueString())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Perhaps written by another resource during the apply; it is read
		// then, before anything is sent.
		resp.Diagnostics.AddAttributeWarning(path.Root("source"), "Image file not found",
			"The file does not exist yet, so the plan cannot tell whether it changed. It is read when the change is applied.")
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("sha256"), types.StringUnknown())...)
	case err != nil:
		resp.Diagnostics.AddAttributeError(path.Root("source"), "Cannot use the image file", err.Error())
	default:
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("sha256"), types.StringValue(hash))...)
	}
}

// appImageServedHash returns the recorded SHA-256 of what Pocket ID served after the
// last upload; "" when none is recorded.
func appImageServedHash(ctx context.Context, private interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}) (string, diag.Diagnostics) {
	raw, diags := private.GetKey(ctx, applicationImageStoredKey)
	if diags.HasError() || len(raw) == 0 {
		return "", diags
	}
	var hash string
	if json.Unmarshal(raw, &hash) != nil {
		return "", diags
	}
	return hash, diags
}

// setAppImageServedHash records hash; "" removes the record.
func setAppImageServedHash(ctx context.Context, private interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}, hash string) diag.Diagnostics {
	if hash == "" {
		return private.SetKey(ctx, applicationImageStoredKey, nil)
	}
	raw, _ := json.Marshal(hash)
	return private.SetKey(ctx, applicationImageStoredKey, raw)
}

// upload sends source for plan and fills in id and sha256. It returns
// whether the upload was made, and the SHA-256 of what Pocket ID then serves
// ("" when reading it back failed; the next refresh records it).
func (r *applicationImageResource) upload(ctx context.Context, plan *applicationImageModel, diags *diag.Diagnostics) (bool, string) {
	kind := client.ApplicationImage(plan.Kind.ValueString())
	content, hash, err := readApplicationImageSource(kind, plan.Source.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("source"), "Cannot use the image file", err.Error())
		return false, ""
	}
	if !plan.SHA256.IsUnknown() && plan.SHA256.ValueString() != hash {
		diags.AddAttributeError(path.Root("source"), "Image file changed",
			"The file's content changed after the plan was made. Nothing was uploaded; plan again.")
		return false, ""
	}
	tflog.Debug(ctx, "Uploading application image", map[string]any{"kind": string(kind), "bytes": len(content)})
	if err := r.client.UploadApplicationImage(ctx, kind, applicationImageExtension(plan.Source.ValueString()), content); err != nil {
		diags.AddError("Error uploading application image", fmt.Sprintf("Could not upload the %s: %s", kind, err))
		return false, ""
	}
	plan.ID = types.StringValue(string(kind))
	plan.SHA256 = types.StringValue(hash)

	served, err := r.client.GetApplicationImage(ctx, kind)
	if err != nil {
		diags.AddWarning("Uploaded image not read back",
			fmt.Sprintf("The %s was uploaded, but reading it back failed (%s). The next refresh records what Pocket ID serves.", kind, err))
		return true, ""
	}
	return true, appImageSHA256Hex(served)
}

// refresh reads the image for state. It returns whether Pocket ID confirmed
// that no image is set, and the served hash to record ("" to keep the
// record as it is). recorded is the hash recorded so far.
func (r *applicationImageResource) refresh(ctx context.Context, state *applicationImageModel, recorded string, diags *diag.Diagnostics) (bool, string) {
	kind := client.ApplicationImage(state.Kind.ValueString())
	served, err := r.client.GetApplicationImage(ctx, kind)
	if client.IsNotFound(err, client.ResourceImage) {
		return true, ""
	}
	if err != nil {
		diags.AddError("Error reading application image", fmt.Sprintf("Could not read the %s: %s", kind, err))
		return false, ""
	}
	state.ID = types.StringValue(string(kind))
	current := appImageSHA256Hex(served)
	switch {
	case recorded == "":
		// Imported, or the read-back after the upload failed: what is
		// served now is the reference from here on.
		if state.SHA256.IsNull() || state.SHA256.ValueString() == "" {
			state.SHA256 = types.StringValue(current)
		}
		return false, current
	case recorded != current:
		// Replaced outside Terraform: plan the upload again.
		state.SHA256 = types.StringValue(current)
	}
	return false, ""
}

func (r *applicationImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan applicationImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uploaded, served := r.upload(ctx, &plan, &resp.Diagnostics)
	if !uploaded {
		return
	}
	resp.Diagnostics.Append(setAppImageServedHash(ctx, resp.Private, served)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *applicationImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state applicationImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	recorded, diags := appImageServedHash(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	gone, record := r.refresh(ctx, &state, recorded, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if gone {
		tflog.Info(ctx, "No uploaded application image is set any more; removing it from state", map[string]any{"kind": state.Kind.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}
	if record != "" {
		resp.Diagnostics.Append(setAppImageServedHash(ctx, resp.Private, record)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// applicationImageNeedsUpload reports whether an update must upload source:
// when its content changed (or is not known yet), and when its file type did.
// Pocket ID stores and serves an image with the type of the uploaded file
// name, so the same bytes under another extension are a different image.
func applicationImageNeedsUpload(plan, state *applicationImageModel) bool {
	if plan.SHA256.IsUnknown() || !plan.SHA256.Equal(state.SHA256) {
		return true
	}
	return applicationImageExtension(plan.Source.ValueString()) != applicationImageExtension(state.Source.ValueString())
}

func (r *applicationImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state applicationImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if applicationImageNeedsUpload(&plan, &state) {
		uploaded, served := r.upload(ctx, &plan, &resp.Diagnostics)
		if !uploaded {
			return
		}
		resp.Diagnostics.Append(setAppImageServedHash(ctx, resp.Private, served)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *applicationImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state applicationImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	kind := client.ApplicationImage(state.Kind.ValueString())
	err := r.client.DeleteApplicationImage(ctx, kind)
	switch {
	case err == nil, client.IsNotFound(err, client.ResourceImage):
	case errors.Is(err, client.ErrApplicationImageNotDeletable):
		resp.Diagnostics.AddWarning("The image stays in Pocket ID",
			fmt.Sprintf("Pocket ID has no way to remove the %s; the uploaded image stays in place and Terraform no longer manages it.", kind))
	default:
		resp.Diagnostics.AddError("Error removing application image", fmt.Sprintf("Could not remove the %s: %s", kind, err))
	}
}

// ImportState imports an image by its kind, such as "logo_light".
func (r *applicationImageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	kind := client.ApplicationImage(req.ID)
	if !kind.Valid() {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("The import ID must be one of %s.", strings.Join(applicationImageKinds(), ", ")))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("kind"), req.ID)...)
}
