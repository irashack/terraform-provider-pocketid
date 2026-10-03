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
	resp.Schema = schema.Schema{
		Description: "Sets a Pocket-ID user's profile picture from a local image file. Destroying the resource restores the default picture.",
		MarkdownDescription: "Sets a Pocket-ID user's profile picture from a local image file. Destroying the resource restores the default picture.\n\n" +
			"**What Pocket ID does with the file.** It decodes the image itself (the file name and media type do not matter), scales and crops it to a 300x300 PNG and stores that. " +
			"It accepts PNG, JPEG, GIF, WebP and BMP, and from Pocket ID 2.15 refuses an image of more than 16 million pixels in total (about 4000x4000). " +
			"The provider checks at plan time that the file exists, is a regular file of at most 10 MiB, and, for PNG, JPEG and GIF, is a readable image within the pixel limit; " +
			"it applies the pixel limit on every supported version, 2.14 included. WebP and BMP files are passed to the server with only their size checked.\n\n" +
			"**Changes to the file.** `sha256` is the digest of the file's content: when the file changes, the next plan shows a new digest and applying uploads the file again. " +
			"A change to `source` alone (the same content at another path) uploads nothing. Use `${path.module}/...` for `source`; a relative path is resolved against Terraform's working directory.\n\n" +
			"**Drift.** Pocket ID reports no hash of the stored picture and does not say whether a user has a custom picture. " +
			"The provider therefore keeps, in `stored_sha256`, the digest of the picture the server *serves* for the user right after the upload, and compares the served picture with it on every refresh. " +
			"A picture replaced or removed outside Terraform is detected that way, and the next apply uploads the file again. " +
			"A false difference can appear after a Pocket ID upgrade that changes how pictures are scaled or encoded; the cost is one more upload. " +
			"What cannot be detected: that the stored picture came from *this* file, as opposed to an identical image uploaded some other way, and any change while the user does not exist.\n\n" +
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
				Description: "The SHA-256 digest of the picture Pocket ID served for the user right after the upload (the 300x300 PNG it made from the file), used to detect a picture changed or removed outside Terraform. Null when it could not be read after the upload.",
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
		} else if client.IsDefiniteRejection(err) {
			diags.AddError("Error uploading profile picture", fmt.Sprintf("Pocket ID refused the picture for user %s: %s. Nothing was changed.", userID, err))
		} else {
			diags.AddError("Error uploading profile picture", fmt.Sprintf("The upload for user %s failed and may or may not have replaced the picture: %s. It is not repeated; check the user's picture and apply again.", userID, err))
		}
		return false
	}

	plan.SHA256 = types.StringValue(file.SHA256)
	served, err := r.servedDigest(ctx, userID)
	if err != nil {
		// The picture was uploaded; only the drift baseline is missing. The
		// next refresh adopts whatever is served then.
		plan.StoredSHA256 = types.StringNull()
		diags.AddWarning("Could not read the uploaded picture back",
			fmt.Sprintf("The picture was uploaded, but reading it back failed (%s). A change made outside Terraform before the next refresh will not be detected.", err))
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
	if !r.upload(ctx, &plan, &resp.Diagnostics) {
		return
	}
	plan.ID = types.StringValue(plan.UserID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read confirms the user still exists and compares the picture the server
// serves with the one recorded after the upload. A difference clears sha256 in
// the state, so that the next plan shows the file's digest as a change and the
// apply uploads the file again.
func (r *userProfilePictureResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userProfilePictureResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
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
	switch {
	case state.StoredSHA256.IsNull() || state.StoredSHA256.IsUnknown():
		// No baseline (the read-back after the upload failed): adopt it.
		state.StoredSHA256 = types.StringValue(served)
	case state.StoredSHA256.ValueString() != served:
		state.StoredSHA256 = types.StringValue(served)
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

// Delete restores the default picture. A user that is already gone, or that has
// no custom picture, is nothing to do.
func (r *userProfilePictureResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userProfilePictureResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	userID := state.UserID.ValueString()
	if err := r.client.ResetUserProfilePicture(ctx, userID); err != nil {
		if client.IsUserNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error restoring the default profile picture", fmt.Sprintf("Could not remove the picture of user %s: %s", userID, err))
	}
}
