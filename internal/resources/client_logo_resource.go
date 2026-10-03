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
	_ "image/jpeg" // registers the JPEG decoder for the pixel check
	_ "image/png"  // registers the PNG decoder for the pixel check
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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/valuefree"
)

var (
	_ resource.Resource                = &clientLogoResource{}
	_ resource.ResourceWithConfigure   = &clientLogoResource{}
	_ resource.ResourceWithImportState = &clientLogoResource{}
	_ resource.ResourceWithModifyPlan  = &clientLogoResource{}
)

func init() { register(NewClientLogoResource) }

// NewClientLogoResource returns the pocketid_client_logo resource.
func NewClientLogoResource() resource.Resource {
	return &clientLogoResource{}
}

// clientLogoResource manages the light or the dark logo of one OIDC client,
// uploaded from a local file. Drift is found two ways: the client reports
// whether it has each logo (hasLogo, hasDarkLogo), and the hash of the image
// Pocket ID served right after the upload is kept in private state and
// compared with what it serves later. Pocket ID strips metadata from JPEG,
// PNG and WebP images, so the served image is not the file itself.
type clientLogoResource struct {
	client *client.Client
}

type clientLogoResourceModel struct {
	ID       types.String `tfsdk:"id"`
	ClientID types.String `tfsdk:"client_id"`
	Variant  types.String `tfsdk:"variant"`
	Source   types.String `tfsdk:"source"`
	SHA256   types.String `tfsdk:"sha256"`
}

const (
	clientLogoLight = "light"
	clientLogoDark  = "dark"

	// clientLogoServedKey is the private-state key holding the SHA-256 of
	// the image Pocket ID served after this resource's last upload.
	clientLogoServedKey = "served_sha256"

	// clientLogoMaxPixels is Pocket ID's limit on a JPEG or PNG logo
	// (utils/image maxImagePixels, checked with image.DecodeConfig in
	// 2.15.0 to 2.17.0).
	clientLogoMaxPixels = 16_000_000
)

func (r *clientLogoResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_client_logo"
}

func (r *clientLogoResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	defer func() { resp.Schema = valuefree.ResourceSchema(resp.Schema) }()
	extensions := strings.Join(client.ClientLogoExtensions(), ", ")
	resp.Schema = schema.Schema{
		Description: "Uploads the light or dark logo of an OIDC client in Pocket ID from a local image file.",
		MarkdownDescription: "Uploads the light or dark logo of an OIDC client in Pocket ID from a local image file. " +
			"Use one resource per logo: the light logo is the one Pocket ID shows by default, and the dark logo, when there " +
			"is one, replaces it in dark mode. Destroying the resource removes that logo from the client.\n\n" +
			"The file is uploaded again when its content changes, and when Pocket ID holds a different image than the one " +
			"this resource uploaded (it was replaced or removed outside Terraform).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "`<client_id>/<variant>`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"client_id": schema.StringAttribute{
				Description: "The ID of the OIDC client (`pocketid_client.<name>.id`). Changing it moves the logo to the other client.",
				Required:    true,
				Validators:  []validator.String{clientRefIDValidator{}},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"variant": schema.StringAttribute{
				Description: "Which logo this is: `light` (the default logo) or `dark` (the logo for dark mode). Defaults to `light`. Changing it replaces the resource.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(clientLogoLight),
				Validators:  []validator.String{stringvalidator.OneOf(clientLogoLight, clientLogoDark)},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source": schema.StringAttribute{
				Description: fmt.Sprintf("Path of the image file to upload. Pocket ID takes the image's type from the file "+
					"name's extension (any case), which must be one of: %s. The file is uploaded again when its content or its "+
					"extension changes; another path to the same content with the same extension is not uploaded. At most %d bytes (Pocket ID's 2 MiB upload limit, "+
					"less the request's own framing); a JPEG or PNG image may have at most %d pixels. The file is read while "+
					"planning; one that does not exist yet (another resource writes it during the apply) is read when it is uploaded.",
					extensions, client.ClientLogoMaxBytes, clientLogoMaxPixels),
				Required:   true,
				Validators: []validator.String{clientLogoSourceValidator{}},
			},
			"sha256": schema.StringAttribute{
				Description: "SHA-256 (hex) of the content of `source` as last uploaded. When Pocket ID serves a different " +
					"image than the one this resource uploaded, a refresh sets it to the SHA-256 of the image Pocket ID serves, so " +
					"the next plan uploads `source` again.",
				Computed: true,
			},
		},
	}
}

func (r *clientLogoResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	r.client = c
}

// ModifyPlan plans sha256 from the file's current content, so a changed file
// plans an upload, and plans id from client_id and variant.
func (r *clientLogoResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan clientLogoResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.ClientID.IsUnknown() && !plan.Variant.IsUnknown() {
		plan.ID = types.StringValue(plan.ClientID.ValueString() + "/" + plan.Variant.ValueString())
	}
	plan.SHA256 = types.StringUnknown()
	if !plan.Source.IsUnknown() && !plan.Source.IsNull() {
		_, sum, err := readClientLogoSource(plan.Source.ValueString())
		switch {
		case err == nil:
			plan.SHA256 = types.StringValue(sum)
		case errors.Is(err, fs.ErrNotExist):
			// Perhaps written by another resource during the apply; it is
			// read then, before anything is sent.
			resp.Diagnostics.AddAttributeWarning(path.Root("source"), "Logo file not found",
				"The file does not exist yet, so the plan cannot tell whether it changed. It is read when the change is applied.")
		default:
			resp.Diagnostics.AddAttributeError(path.Root("source"), "Cannot use the logo file", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *clientLogoResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clientLogoResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "configuration", knownAs("OIDC client", plan.ClientID)) {
		return
	}
	uploaded, served := r.upload(ctx, &plan, &resp.Diagnostics)
	if !uploaded {
		return
	}
	resp.Diagnostics.Append(setClientLogoServedHash(ctx, resp.Private, served)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// clientLogoNeedsUpload reports whether an update must upload source: when
// its content changed (or is not known yet), and when its file type did.
// Pocket ID stores and serves a logo with the type of the uploaded file
// name's extension, so the same bytes under another extension are a
// different logo. Another path, or another letter case of the same
// extension, with the same content needs no upload.
func clientLogoNeedsUpload(plan, state *clientLogoResourceModel) bool {
	if plan.SHA256.IsUnknown() || !plan.SHA256.Equal(state.SHA256) {
		return true
	}
	return state.Source.IsNull() || clientLogoExtension(plan.Source.ValueString()) != clientLogoExtension(state.Source.ValueString())
}

// clientLogoExtension returns a file name's extension the way Pocket ID
// reads it: lower case, without the dot.
func clientLogoExtension(source string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(source), "."))
}

func (r *clientLogoResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state clientLogoResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "configuration", knownAs("OIDC client", plan.ClientID)) {
		return
	}
	if clientLogoNeedsUpload(&plan, &state) {
		uploaded, served := r.upload(ctx, &plan, &resp.Diagnostics)
		if !uploaded {
			return
		}
		resp.Diagnostics.Append(setClientLogoServedHash(ctx, resp.Private, served)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// upload sends the source file, checks that the client now reports the logo,
// and fills in id and sha256. It returns whether the upload was made, and the
// SHA-256 of the image Pocket ID then serves ("" when it could not be read
// back; the next refresh records it). Nothing is retried: a failed upload may
// or may not have replaced the logo, and applying again uploads it again.
func (r *clientLogoResource) upload(ctx context.Context, plan *clientLogoResourceModel, diags *diag.Diagnostics) (bool, string) {
	clientID, variant, source := plan.ClientID.ValueString(), plan.Variant.ValueString(), plan.Source.ValueString()
	light := variant != clientLogoDark
	content, sum, err := readClientLogoSource(source)
	if err != nil {
		diags.AddAttributeError(path.Root("source"), "Cannot use the logo file", err.Error()+" Nothing was uploaded.")
		return false, ""
	}
	if !plan.SHA256.IsUnknown() && plan.SHA256.ValueString() != sum {
		diags.AddAttributeError(path.Root("source"), "Logo file changed",
			"The file's content changed after the plan was made. Nothing was uploaded; plan again.")
		return false, ""
	}

	tflog.Debug(ctx, "Uploading client logo", map[string]any{"client_id": clientID, "variant": variant, "bytes": len(content)})
	if err := r.client.UploadClientLogo(ctx, clientID, light, filepath.Ext(source), content); err != nil {
		switch {
		case client.IsNotFound(err, client.ResourceOIDCClient):
			diags.AddAttributeError(path.Root("client_id"), "OIDC client not found", "Pocket ID has no OIDC client "+clientID+"; nothing was uploaded.")
		case errors.Is(err, client.ErrInvalidUpload):
			diags.AddAttributeError(path.Root("source"), "Logo not uploaded", err.Error())
		case writeRefused(err):
			diags.AddError("Logo not uploaded", "Pocket ID refused the "+variant+" logo for OIDC client "+clientID+" ("+err.Error()+"); the client's logo is unchanged.")
		default:
			diags.AddError("Logo upload result uncertain", "Uploading the "+variant+" logo for OIDC client "+clientID+" failed after the request was sent ("+
				err.Error()+"), so the logo may or may not have been replaced. The upload was not retried; applying again uploads it again.")
		}
		return false, ""
	}

	plan.ID = types.StringValue(clientID + "/" + variant)
	plan.SHA256 = types.StringValue(sum)

	// Pocket ID ignores parameters it does not understand, so confirm the
	// client now reports the logo uploaded to, then record what it serves.
	current, err := r.client.GetClient(ctx, clientID)
	if err != nil {
		diags.AddWarning("Uploaded logo not read back",
			"The "+variant+" logo of OIDC client "+clientID+" was uploaded, but reading the client back failed ("+err.Error()+"). The next refresh checks it.")
		return true, ""
	}
	if !clientLogoPresent(current, light) {
		diags.AddError("Logo not stored",
			"Pocket ID accepted the "+variant+" logo for OIDC client "+clientID+", but the client does not report a "+variant+" logo afterwards.")
		return false, ""
	}
	served, err := r.client.GetClientLogo(ctx, clientID, light)
	if err != nil {
		diags.AddWarning("Uploaded logo not read back",
			"The "+variant+" logo of OIDC client "+clientID+" was uploaded, but reading it back failed ("+err.Error()+"). The next refresh records what Pocket ID serves.")
		return true, ""
	}
	return true, clientLogoSHA256(served)
}

func (r *clientLogoResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clientLogoResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("OIDC client", state.ClientID)) {
		return
	}
	recorded, diags := clientLogoServedHash(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	gone, record := r.refresh(ctx, &state, recorded, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	if record != "" {
		resp.Diagnostics.Append(setClientLogoServedHash(ctx, resp.Private, record)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// refresh reads the logo for state. It returns whether the logo is confirmed
// gone, and the served hash to record ("" to keep the record as it is).
// recorded is the hash recorded so far.
func (r *clientLogoResource) refresh(ctx context.Context, state *clientLogoResourceModel, recorded string, diags *diag.Diagnostics) (bool, string) {
	clientID, variant := state.ClientID.ValueString(), state.Variant.ValueString()
	light := variant != clientLogoDark
	current, err := r.client.GetClient(ctx, clientID)
	if err != nil {
		if client.IsNotFound(err, client.ResourceOIDCClient) {
			tflog.Info(ctx, "OIDC client no longer exists; removing its logo from state", map[string]any{"client_id": clientID, "variant": variant})
			return true, ""
		}
		diags.AddError("Error reading client logo", "Could not read OIDC client "+clientID+": "+err.Error())
		return false, ""
	}
	// Asked for a dark logo the client lacks, Pocket ID serves the light
	// one, so the client's own report decides whether the logo exists.
	if !clientLogoPresent(current, light) {
		tflog.Info(ctx, "The client has no such logo any more; removing it from state", map[string]any{"client_id": clientID, "variant": variant})
		return true, ""
	}
	served, err := r.client.GetClientLogo(ctx, clientID, light)
	if client.IsNotFound(err, client.ResourceImage) || client.IsNotFound(err, client.ResourceOIDCClient) {
		tflog.Info(ctx, "Pocket ID serves no such logo; removing it from state", map[string]any{"client_id": clientID, "variant": variant})
		return true, ""
	}
	if err != nil {
		diags.AddError("Error reading client logo", "Could not read the "+variant+" logo of OIDC client "+clientID+": "+err.Error())
		return false, ""
	}
	state.ID = types.StringValue(clientID + "/" + variant)
	return false, clientLogoCompareServed(state, recorded, clientLogoSHA256(served))
}

// clientLogoCompareServed compares the hash of the image served now with the
// one recorded after the last upload. With no record (after an import, or
// when the read-back after the upload failed) the served image becomes the
// reference: it returns the hash to record, and fills in a missing sha256.
// When the two differ the logo was replaced outside Terraform: sha256 is set
// to the served image's hash, so the next plan uploads source again.
func clientLogoCompareServed(state *clientLogoResourceModel, recorded, current string) string {
	switch {
	case recorded == "":
		if state.SHA256.IsNull() || state.SHA256.ValueString() == "" {
			state.SHA256 = types.StringValue(current)
		}
		return current
	case recorded != current:
		state.SHA256 = types.StringValue(current)
	}
	return ""
}

func (r *clientLogoResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clientLogoResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("OIDC client", state.ClientID)) {
		return
	}
	clientID, variant := state.ClientID.ValueString(), state.Variant.ValueString()
	light := variant != clientLogoDark
	err := r.client.DeleteClientLogo(ctx, clientID, light)
	if err == nil || client.IsNotFound(err, client.ResourceImage) || client.IsNotFound(err, client.ResourceOIDCClient) {
		return
	}
	// The DELETE may have been applied anyway; only a read that shows the
	// logo gone (or the client gone) settles it.
	current, readErr := r.client.GetClient(ctx, clientID)
	if client.IsNotFound(readErr, client.ResourceOIDCClient) || (readErr == nil && !clientLogoPresent(current, light)) {
		return
	}
	resp.Diagnostics.AddError("Error removing client logo",
		"Could not remove the "+variant+" logo of OIDC client "+clientID+" ("+err.Error()+"), and the client still reports it or could not be read; it stays in state.")
}

func (r *clientLogoResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clientID, variant, ok := strings.Cut(req.ID, "/")
	if !ok || r.client.ValidateIdentifier("OIDC client", clientID) != nil || (variant != clientLogoLight && variant != clientLogoDark) ||
		r.client.ContainsAPIKey(req.ID) {
		resp.Diagnostics.AddError("Unexpected import identifier",
			"Expected <client_id>/light or <client_id>/dark, not containing the API key this provider authenticates with.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("client_id"), clientID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("variant"), variant)...)
}

func clientLogoPresent(c *client.OIDCClient, light bool) bool {
	if light {
		return c.HasLogo
	}
	return c.HasDarkLogo
}

func clientLogoSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// clientLogoServedHash returns the recorded SHA-256 of what Pocket ID served
// after the last upload; "" when none is recorded.
func clientLogoServedHash(ctx context.Context, private interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}) (string, diag.Diagnostics) {
	raw, diags := private.GetKey(ctx, clientLogoServedKey)
	if diags.HasError() || len(raw) == 0 {
		return "", diags
	}
	var hash string
	if json.Unmarshal(raw, &hash) != nil {
		return "", diags
	}
	return hash, diags
}

// setClientLogoServedHash records hash; "" removes the record.
func setClientLogoServedHash(ctx context.Context, private interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}, hash string) diag.Diagnostics {
	if hash == "" {
		return private.SetKey(ctx, clientLogoServedKey, nil)
	}
	raw, _ := json.Marshal(hash)
	return private.SetKey(ctx, clientLogoServedKey, raw)
}

// readClientLogoSource reads a logo file and checks it against Pocket ID's
// limits. It returns the content and its SHA-256 in hex. The error wraps
// fs.ErrNotExist for a missing file.
func readClientLogoSource(source string) ([]byte, string, error) {
	extension := clientLogoExtension(source)
	if _, ok := client.ClientLogoMediaType(extension); !ok {
		return nil, "", fmt.Errorf("the file name has no extension Pocket ID accepts for a logo (it takes the image type from the extension: %s); the name is not shown",
			strings.Join(client.ClientLogoExtensions(), ", "))
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, "", localFileError(err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, "", localFileError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", errors.New("the source is not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(client.ClientLogoMaxBytes)+1))
	if err != nil {
		return nil, "", localFileError(err)
	}
	if len(content) > client.ClientLogoMaxBytes {
		return nil, "", fmt.Errorf("the file is larger than %d bytes, the most Pocket ID accepts for a logo", client.ClientLogoMaxBytes)
	}
	switch extension {
	case "jpg", "jpeg", "png":
		// Pocket ID refuses an image with too many pixels; one it cannot
		// decode is accepted as it is, so it is not refused here either.
		if config, _, err := image.DecodeConfig(bytes.NewReader(content)); err == nil && int64(config.Width)*int64(config.Height) > clientLogoMaxPixels {
			return nil, "", fmt.Errorf("the image is %dx%d pixels; Pocket ID accepts at most %d pixels", config.Width, config.Height, clientLogoMaxPixels)
		}
	}
	return content, clientLogoSHA256(content), nil
}

// clientLogoSourceValidator requires a file name with an image extension
// Pocket ID accepts for a logo.
type clientLogoSourceValidator struct{}

func (clientLogoSourceValidator) Description(context.Context) string {
	return "must name a file with an image extension Pocket ID accepts for a logo"
}

func (v clientLogoSourceValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (clientLogoSourceValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, ok := client.ClientLogoMediaType(filepath.Ext(req.ConfigValue.ValueString())); !ok {
		resp.Diagnostics.AddAttributeError(req.Path, "Unsupported logo file type",
			"Pocket ID takes a logo's type from its file extension and accepts only: "+strings.Join(client.ClientLogoExtensions(), ", ")+".")
	}
}
