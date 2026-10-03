package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/valuefree"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource               = &userProfilePictureResource{}
	_ resource.ResourceWithConfigure  = &userProfilePictureResource{}
	_ resource.ResourceWithModifyPlan = &userProfilePictureResource{}
)

func init() { register(NewUserProfilePictureResource) }

// NewUserProfilePictureResource creates the resource that sets a user's
// profile picture from a local file.
func NewUserProfilePictureResource() resource.Resource {
	return &userProfilePictureResource{}
}

type userProfilePictureResource struct {
	client *client.Client
}

type userProfilePictureResourceModel struct {
	ID           types.String `tfsdk:"id"`
	UserID       types.String `tfsdk:"user_id"`
	Source       types.String `tfsdk:"source"`
	SHA256       types.String `tfsdk:"sha256"`
	StoredSHA256 types.String `tfsdk:"stored_sha256"`
}

var userProfilePictureUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Metadata returns the resource type name.
func (r *userProfilePictureResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_profile_picture"
}

// Schema defines the schema for the resource.
func (r *userProfilePictureResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	defer func() { resp.Schema = valuefree.ResourceSchema(resp.Schema) }()
	resp.Schema = schema.Schema{
		Description: "Sets a Pocket-ID user's profile picture from a local image file. Destroying the resource restores the default picture if the stored picture is still the one it uploaded.",
		MarkdownDescription: "Sets a Pocket-ID user's profile picture from a local image file. Destroying the resource restores the default picture if the stored picture is still the one it uploaded (see Destroy below).\n\n" +
			"**What Pocket ID does with the file.** It decodes the image itself (the file name and media type do not matter), scales and crops it to a 300x300 PNG and stores that. " +
			"It accepts PNG, JPEG, GIF, WebP and BMP, and from Pocket ID 2.15 refuses an image of more than 16 million pixels in total (about 4000x4000). " +
			"The provider checks at plan time that the file exists, is a regular file of at most 10 MiB, and, for PNG, JPEG and GIF, is within the pixel limit and decodes completely (a file that is cut short or damaged is refused before anything is uploaded); " +
			"it applies the pixel limit on every supported version, 2.14 included. WebP and BMP files are passed to the server with only their size checked.\n\n" +
			"**Changes to the file.** `sha256` is the digest of the file's content: when the file changes, the next plan shows a new digest and applying uploads the file again. " +
			"A change to `source` alone (the same content at another path) uploads nothing. Use `${path.module}/...` for `source`; a relative path is resolved against Terraform's working directory.\n\n" +
			"**Drift.** Pocket ID reports no hash of the stored picture and does not say whether a user has a custom picture. " +
			"The provider therefore keeps, in `stored_sha256`, the digest of the picture the server *serves* for the user right after the upload, and compares the served picture with it on every refresh. " +
			"A picture replaced or removed outside Terraform is detected that way, and the next apply uploads the file again. " +
			"A false difference can appear after a Pocket ID upgrade that changes how pictures are scaled or encoded; the cost is one more upload. " +
			"If the upload succeeded but the picture could not be read back, `stored_sha256` is null, nothing can be compared, and the next plan shows an upload that records it. " +
			"What cannot be detected: that the stored picture came from *this* file, as opposed to an identical image uploaded some other way, and any change while the user does not exist.\n\n" +
			"**Destroy.** Destroying removes the picture only while the picture the server serves is still the one recorded in `stored_sha256`: the provider reads it again, past caches, immediately before it deletes. " +
			"A refresh never replaces `stored_sha256`; only an upload does. " +
			"If the served picture is different (replaced or removed outside Terraform, or encoded differently after a Pocket ID upgrade), destroy sends no request, leaves the picture exactly as it is, warns that it did so because the picture is not the one the provider uploaded, and removes the resource from state: the provider does not delete a picture it cannot show it uploaded, and cannot tell a replacement from the default picture. " +
			"If `stored_sha256` is null (the picture could not be read back after the upload), the provider has nothing to compare with and destroy stops with an error: apply again, which uploads the file and records it, and destroy afterwards, or run `terraform state rm` to stop managing the picture. " +
			"If the picture cannot be read at all, destroy stops with an error and changes nothing.\n\n" +
			"**What `stored_sha256` proves.** It is the digest of the picture the server served when the provider read it right after its upload. It proves which bytes the provider observed, not that the provider uploaded them: if someone else uploads a different picture after the provider's upload and before that read, their picture becomes `stored_sha256`, and a later destroy removes it. " +
			"Separately, Pocket ID has no conditional delete, so a picture uploaded between the provider's check and its delete request is removed all the same. Neither window can be closed from here.\n\n" +
			"There is no import: the source file is not recoverable from the server. Do not manage the same user's picture with two of these resources.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The resource ID, the same as `user_id`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"user_id": schema.StringAttribute{
				Description: "The ID of the user (a UUID). Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(userProfilePictureUUIDPattern, "must be a UUID"),
				},
			},
			"source": schema.StringAttribute{
				Description: "The path of the image file to upload: PNG, JPEG, GIF, WebP or BMP, at most 10 MiB and, from Pocket ID 2.15, at most 16 million pixels.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"sha256": schema.StringAttribute{
				Description: "The SHA-256 digest of the file's content, in hexadecimal. A changed file changes it, which makes the next apply upload the file again. Unknown until apply when the file does not exist yet at plan time.",
				Computed:    true,
			},
			"stored_sha256": schema.StringAttribute{
				Description: "The SHA-256 digest of the picture Pocket ID served for the user right after the provider's last upload (the 300x300 PNG it made from the file). It detects a picture changed or removed outside Terraform and is what destroy checks before deleting; a refresh never changes it. It proves the bytes the provider observed, not who uploaded them. Null when it could not be read after the upload.",
				Computed:    true,
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *userProfilePictureResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan reads the source file at plan time, so that a changed file shows
// as a changed digest and a file Pocket ID cannot use is refused before any
// request. A file that does not exist yet (another resource may create it
// during the apply) leaves the digest unknown and is checked at apply time.
func (r *userProfilePictureResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan userProfilePictureResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state *userProfilePictureResourceModel
	if !req.State.Raw.IsNull() {
		state = &userProfilePictureResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	unknownDigests := func() {
		plan.SHA256 = types.StringUnknown()
		plan.StoredSHA256 = types.StringUnknown()
	}
	if plan.Source.IsUnknown() {
		unknownDigests()
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}

	file, err := readUserProfilePicture(plan.Source.ValueString())
	switch {
	case errors.Is(err, errProfilePictureMissing):
		resp.Diagnostics.AddAttributeWarning(path.Root("source"), "Source file does not exist yet",
			"The file does not exist at plan time. It is read, and checked, when the change is applied.")
		unknownDigests()
	case err != nil:
		resp.Diagnostics.AddAttributeError(path.Root("source"), "Unusable profile picture file", err.Error())
		return
	default:
		plan.SHA256 = types.StringValue(file.SHA256)
		if state != nil && !state.SHA256.IsNull() && state.SHA256.ValueString() == file.SHA256 {
			// Nothing will be uploaded: the stored digest stays.
			plan.StoredSHA256 = state.StoredSHA256
		} else {
			plan.StoredSHA256 = types.StringUnknown()
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// servedDigest returns the digest of the picture Pocket ID serves for the user.
func (r *userProfilePictureResource) servedDigest(ctx context.Context, userID string) (string, error) {
	picture, err := r.client.GetUserProfilePicture(ctx, userID)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(picture)
	return hex.EncodeToString(digest[:]), nil
}

// upload reads the source file again, checks it is the file that was planned,
// uploads it and records the digest of what the server then serves. It reports
// whether the picture was uploaded; if not, an error is in the diagnostics.
func (r *userProfilePictureResource) upload(ctx context.Context, plan *userProfilePictureResourceModel, diags *diag.Diagnostics) bool {
	file, err := readUserProfilePicture(plan.Source.ValueString())
	if err != nil {
		if errors.Is(err, errProfilePictureMissing) {
			diags.AddError("Source file does not exist", "The profile picture file does not exist. Nothing was uploaded.")
		} else {
			diags.AddError("Unusable profile picture file", err.Error()+" Nothing was uploaded.")
		}
		return false
	}
	if !plan.SHA256.IsUnknown() && !plan.SHA256.IsNull() && plan.SHA256.ValueString() != file.SHA256 {
		diags.AddError("Source file changed since the plan",
			"The file's content is not what it was when the plan was made. Nothing was uploaded; plan again.")
		return false
	}

	userID := plan.UserID.ValueString()
	err = r.client.UploadUserProfilePicture(ctx, userID, client.MultipartFile{
		FileName:    file.Name,
		ContentType: file.ContentType,
		Content:     file.Content,
	})
	if err != nil {
		if client.IsUserNotFound(err) {
			diags.AddError("User not found", fmt.Sprintf("No user found with id '%s'. Nothing was uploaded.", userID))
		} else if writeRefused(err) {
			diags.AddError("Error uploading profile picture", fmt.Sprintf("Pocket ID refused the picture for user %s: %s. Nothing was changed.", userID, err))
		} else {
			diags.AddError("Error uploading profile picture", fmt.Sprintf("The upload for user %s failed and may or may not have replaced the picture: %s. It is not repeated; check the user's picture and apply again.", userID, err))
		}
		return false
	}

	plan.SHA256 = types.StringValue(file.SHA256)
	served, err := r.servedDigest(ctx, userID)
	if err != nil {
		// The picture was uploaded; only the baseline is missing. A baseline
		// adopted from a later read could be someone else's picture, so Read
		// does not adopt one: it asks for another upload instead.
		plan.StoredSHA256 = types.StringNull()
		diags.AddWarning("Could not read the uploaded picture back",
			fmt.Sprintf("The picture was uploaded, but reading it back failed (%s). Without that record the provider cannot tell later whether the stored picture is the one it uploaded: the next plan shows an upload to record it, and destroying the resource stops until then.", err))
		return true
	}
	plan.StoredSHA256 = types.StringValue(served)
	return true
}

// Create uploads the picture.
func (r *userProfilePictureResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userProfilePictureResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "configuration", knownAs("user", plan.UserID)) {
		return
	}
	if !r.upload(ctx, &plan, &resp.Diagnostics) {
		return
	}
	plan.ID = types.StringValue(plan.UserID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read confirms the user still exists and compares the picture the server
// serves with the one recorded after the last upload. A difference clears
// sha256 in the state, so that the next plan shows the file's digest as a
// change and the apply uploads the file again. stored_sha256 stays what the
// last upload recorded: it is the picture Delete may remove, and a picture
// found at refresh time is not that.
func (r *userProfilePictureResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userProfilePictureResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("user", state.UserID)) {
		return
	}
	userID := state.UserID.ValueString()

	served, err := r.servedDigest(ctx, userID)
	if err != nil {
		// Only Pocket ID's own "no such user" means the user, and with them
		// the picture, is gone.
		if client.IsUserNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading profile picture", fmt.Sprintf("Could not read the picture of user %s: %s", userID, err))
		return
	}

	state.ID = types.StringValue(userID)
	if state.StoredSHA256.IsNull() || state.StoredSHA256.IsUnknown() || state.StoredSHA256.ValueString() != served {
		// Either the picture differs from the one uploaded, or there is no
		// record of what was uploaded (the read-back failed): the file is
		// uploaded again, which records it.
		state.SHA256 = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update uploads the file again when its content changed (or the picture was
// found changed); a change of path alone uploads nothing.
func (r *userProfilePictureResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state userProfilePictureResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "configuration", knownAs("user", plan.UserID)) {
		return
	}

	unchanged := !state.SHA256.IsNull() && !plan.SHA256.IsUnknown() && !plan.SHA256.IsNull() &&
		state.SHA256.ValueString() == plan.SHA256.ValueString()
	if unchanged {
		plan.StoredSHA256 = state.StoredSHA256
	} else {
		if !r.upload(ctx, &plan, &resp.Diagnostics) {
			return
		}
	}
	plan.ID = types.StringValue(plan.UserID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete restores the default picture, but only when the picture the server
// serves is still the one recorded after the last upload: it is read again here,
// past caches, because the state may be older than the plan (a refresh, then a
// wait). A different picture is left exactly as it is: no request is sent, a
// warning says so, and the resource leaves the state, which also covers a
// picture restored to the default by someone else without having to tell it
// from a replacement. No record of the upload cannot be compared with anything
// and is an error. A user that is already gone is nothing to do. The digest
// proves the bytes observed after the upload, not who uploaded them, and Pocket
// ID has no conditional delete, so a picture uploaded between the check and the
// request is still removed.
func (r *userProfilePictureResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userProfilePictureResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("user", state.UserID)) {
		return
	}
	userID := state.UserID.ValueString()

	served, err := r.servedDigest(ctx, userID)
	if err != nil {
		if client.IsUserNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error checking the profile picture before removing it",
			fmt.Sprintf("Could not read the picture of user %s: %s. Nothing was removed. Destroy again, or remove the resource from the state with `terraform state rm` to leave the picture as it is.", userID, err))
		return
	}
	switch {
	case state.StoredSHA256.IsNull() || state.StoredSHA256.IsUnknown():
		resp.Diagnostics.AddError("Cannot tell whether the stored picture is the one this resource uploaded",
			fmt.Sprintf("The provider has no record of the picture it uploaded for user %s (reading it back after the upload failed), so it cannot tell whether the picture stored now is that one. "+
				"Nothing was removed. Apply the configuration again to upload the file and record it, then destroy; "+
				"or remove the resource from the state with `terraform state rm` to leave the picture as it is.", userID))
		return
	case state.StoredSHA256.ValueString() != served:
		resp.Diagnostics.AddWarning("Profile picture left in place",
			fmt.Sprintf("The picture stored for user %s is not the one this resource last uploaded: it was replaced or removed outside Terraform, or Pocket ID now encodes pictures differently. "+
				"It was left exactly as it is, and the resource is removed from the state.", userID))
		return
	}

	if err := r.client.ResetUserProfilePicture(ctx, userID); err != nil {
		if client.IsUserNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error restoring the default profile picture", fmt.Sprintf("Could not remove the picture of user %s: %s", userID, err))
	}
}
