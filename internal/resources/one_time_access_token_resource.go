package resources

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// maxOneTimeAccessTokenTTL mirrors the pocket-id API limit (31 days).
const maxOneTimeAccessTokenTTL = 31 * 24 * time.Hour

// Ensure provider defined types fully satisfy framework interfaces
var _ resource.Resource = &OneTimeAccessTokenResource{}
var _ resource.ResourceWithImportState = &OneTimeAccessTokenResource{}

func init() { register(NewOneTimeAccessTokenResource) }

func NewOneTimeAccessTokenResource() resource.Resource {
	return &OneTimeAccessTokenResource{}
}

// OneTimeAccessTokenResource defines the resource implementation
type OneTimeAccessTokenResource struct {
	client *client.Client
}

// OneTimeAccessTokenResourceModel describes the resource data model
type OneTimeAccessTokenResourceModel struct {
	ID        types.String `tfsdk:"id"`
	UserID    types.String `tfsdk:"user_id"`
	TTL       types.String `tfsdk:"ttl"`
	Token     types.String `tfsdk:"token"`
	ExpiresAt types.String `tfsdk:"expires_at"`
	CreatedAt types.String `tfsdk:"created_at"`
}

func (r *OneTimeAccessTokenResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_one_time_access_token"
}

func (r *OneTimeAccessTokenResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	defer func() { resp.Schema = valuefree.ResourceSchema(resp.Schema) }()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a one-time access token for a user in Pocket-ID. These tokens let a user authenticate when they don't have access to their passkey. " +
			"The token value is returned only once on creation and cannot be read back (pocket-id exposes no read endpoint), so it is stored in Terraform state as a sensitive value. " +
			"If creation fails without a definite answer, or Pocket ID answers without a token, nothing is recorded and the request is not repeated; a token may then exist that stays valid until it is used or expires, since Pocket ID cannot revoke it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier of the one-time access token (same as user_id).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the user this token belongs to.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ttl": schema.StringAttribute{
				MarkdownDescription: "Lifetime of the token expressed as a Go duration string (e.g. `15m`, `1h`, `24h`). " +
					"Must be greater than 1 second and at most 744h (31 days). Changing this forces a new token to be created.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{oneTimeTokenTTLValidator{}},
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "The one-time access token value. Returned only on creation.",
				Computed:            true,
				Sensitive:           true,
			},
			"expires_at": schema.StringAttribute{
				MarkdownDescription: "The computed expiration time of the token in RFC3339 format (created_at + ttl).",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The creation time of the token in RFC3339 format.",
				Computed:            true,
			},
		},
	}
}

func (r *OneTimeAccessTokenResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

func (r *OneTimeAccessTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data OneTimeAccessTokenResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "configuration", knownAs("user", data.UserID)) {
		return
	}

	// The schema validates ttl at plan time; a value that was unknown then
	// is checked here, before calling the API.
	ttlStr := data.TTL.ValueString()
	if err := checkOneTimeTokenTTL(ttlStr); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("ttl"), "Invalid ttl", err.Error())
		return
	}
	ttl, _ := time.ParseDuration(ttlStr)

	tflog.Debug(ctx, "creating one-time access token", map[string]interface{}{
		"user_id": data.UserID.ValueString(),
	})

	userID := data.UserID.ValueString()
	token, err := r.client.CreateOneTimeAccessToken(ctx, userID, &client.OneTimeAccessTokenRequest{TTL: ttlStr})
	if err == nil && token.Token == "" {
		err = fmt.Errorf("one-time access token for user %s: %w: the response held no token", userID, client.ErrResultUnread)
	}
	if err != nil {
		// Nothing is recorded without a token. When the request may have
		// created one, say so: it cannot be read back or revoked, and the
		// request is not repeated.
		switch {
		case errors.Is(err, client.ErrResultUnread):
			resp.Diagnostics.AddError("One-time access token creation result uncertain",
				fmt.Sprintf("Pocket ID accepted the request for user %s, but its response held no usable token. A token may have been created; "+
					"it stays valid until it is used or expires (ttl %s), and Pocket ID offers no way to read it back or revoke it. "+
					"Nothing was recorded in state and the request was not repeated. Details: %s", userID, ttlStr, err))
		case client.IsNotFound(err, client.ResourceUser):
			resp.Diagnostics.AddAttributeError(path.Root("user_id"), "Error creating one-time access token",
				fmt.Sprintf("Pocket ID reports that user %s does not exist; no token was created.", userID))
		case !writeRefused(err):
			resp.Diagnostics.AddError("One-time access token creation result uncertain",
				fmt.Sprintf("Creating a one-time access token for user %s failed without a definite answer (%s). A token may have been created; "+
					"it stays valid until it is used or expires (ttl %s), and Pocket ID offers no way to read it back or revoke it. "+
					"Nothing was recorded in state and the request was not repeated.", userID, err, ttlStr))
		default:
			resp.Diagnostics.AddError("Error creating one-time access token",
				fmt.Sprintf("Could not create one-time access token for user %s: %s", userID, err))
		}
		return
	}

	// The API only returns the token value, so derive the remaining attributes locally.
	created := time.Now().UTC()
	data.ID = data.UserID
	data.Token = types.StringValue(token.Token)
	data.CreatedAt = types.StringValue(created.Format(time.RFC3339))
	data.ExpiresAt = types.StringValue(created.Add(ttl).Format(time.RFC3339))

	tflog.Trace(ctx, "created one-time access token", map[string]interface{}{
		"user_id": data.UserID.ValueString(),
	})

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *OneTimeAccessTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data OneTimeAccessTokenResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// pocket-id exposes no endpoint to read a one-time access token back, and the
	// token is consumed on use. There is nothing to refresh, so the prior state is
	// preserved as-is.
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("user", data.UserID)) {
		return
	}
	tflog.Trace(ctx, "one-time access token is write-only, preserving state", map[string]interface{}{
		"user_id": data.UserID.ValueString(),
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *OneTimeAccessTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// All configurable attributes force replacement, so Update is never expected to run.
	resp.Diagnostics.AddError(
		"Update not supported",
		"One-time access tokens cannot be updated. To change a token, delete and recreate it.",
	)
}

func (r *OneTimeAccessTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// pocket-id exposes no endpoint to revoke a one-time access token; it remains
	// valid until used or expired. Removing it from Terraform state is all we can do.
	tflog.Trace(ctx, "one-time access token cannot be revoked via API, removing from state only")
}

func (r *OneTimeAccessTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by user ID. The token value cannot be recovered from the API.
	if err := r.client.ValidateIdentifier("user", req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", "Import a pocketid_one_time_access_token by the user's ID (a UUID). "+err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("user_id"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
