package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &scimServiceProviderResource{}
	_ resource.ResourceWithConfigure   = &scimServiceProviderResource{}
	_ resource.ResourceWithImportState = &scimServiceProviderResource{}
)

func init() { register(NewScimServiceProviderResource) }

// NewScimServiceProviderResource is a helper function to simplify the provider implementation.
func NewScimServiceProviderResource() resource.Resource {
	return &scimServiceProviderResource{}
}

// scimServiceProviderResource is the resource implementation.
type scimServiceProviderResource struct {
	client *client.Client
}

// scimServiceProviderResourceModel maps the resource schema data.
type scimServiceProviderResourceModel struct {
	ID           types.String `tfsdk:"id"`
	ClientID     types.String `tfsdk:"client_id"`
	Endpoint     types.String `tfsdk:"endpoint"`
	Token        types.String `tfsdk:"token"`
	LastSyncedAt types.String `tfsdk:"last_synced_at"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

// Metadata returns the resource type name.
func (r *scimServiceProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scim_service_provider"
}

// Schema defines the schema for the resource.
func (r *scimServiceProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the SCIM service provider configuration for an OIDC client in Pocket-ID.",
		MarkdownDescription: "Manages the SCIM service provider configuration for an OIDC client in Pocket-ID. " +
			"This enables Pocket-ID to provision users and groups to an external service via SCIM. " +
			"Each OIDC client may have a single SCIM service provider configuration.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the SCIM service provider configuration.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"client_id": schema.StringAttribute{
				Description: "The ID of the OIDC client this SCIM service provider configuration belongs to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"endpoint": schema.StringAttribute{
				Description: "The SCIM endpoint base URL of the external service to provision to.",
				Required:    true,
				Validators: []validator.String{
					urlValidator{},
				},
			},
			"token": schema.StringAttribute{
				Description: "The bearer token used to authenticate against the SCIM endpoint. This value is sensitive. " +
					"The configuration is authoritative: leaving it out (or setting it to an empty string) configures no token, " +
					"and a token that was set or cleared outside Terraform shows as a change on the next plan.",
				Optional:  true,
				Sensitive: true,
			},
			"last_synced_at": schema.StringAttribute{
				Description: "The timestamp of the last successful SCIM synchronization.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the SCIM service provider configuration was created.",
				Computed:    true,
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *scimServiceProviderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create creates the resource and sets the initial Terraform state.
func (r *scimServiceProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan scimServiceProviderResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := &client.ScimServiceProviderCreateRequest{
		Endpoint:     plan.Endpoint.ValueString(),
		Token:        plan.Token.ValueString(),
		OidcClientID: plan.ClientID.ValueString(),
	}

	tflog.Debug(ctx, "Creating SCIM service provider", map[string]any{
		"client_id": createReq.OidcClientID,
		"endpoint":  createReq.Endpoint,
	})

	providerResp, err := r.client.CreateScimServiceProvider(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating SCIM service provider",
			"Could not create SCIM service provider, unexpected error: "+err.Error(),
		)
		return
	}

	r.mapToState(&plan, providerResp)

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// Read refreshes the Terraform state with the latest data.
func (r *scimServiceProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state scimServiceProviderResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading SCIM service provider", map[string]any{
		"id":        state.ID.ValueString(),
		"client_id": state.ClientID.ValueString(),
	})

	providerResp, err := r.client.GetClientScimServiceProvider(ctx, state.ClientID.ValueString())
	if err != nil {
		// Only Pocket ID's own "SCIM service provider not found" removes the
		// resource from state. It also answers for a client that no longer
		// exists, which takes its SCIM configuration with it. Any other
		// failure, including a 404 from a proxy or an unknown route, is an
		// error: guessing "gone" would plan a re-creation of something that may
		// still exist.
		if client.IsNotFound(err, client.ResourceSCIMServiceProvider) {
			tflog.Warn(ctx, "SCIM service provider no longer exists, removing it from state", map[string]any{
				"id":        state.ID.ValueString(),
				"client_id": state.ClientID.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading SCIM service provider",
			"Could not read SCIM service provider for client ID "+state.ClientID.ValueString()+": "+err.Error(),
		)
		return
	}

	r.mapToState(&state, providerResp)

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *scimServiceProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan scimServiceProviderResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state scimServiceProviderResourceModel
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateReq := &client.ScimServiceProviderCreateRequest{
		Endpoint:     plan.Endpoint.ValueString(),
		Token:        plan.Token.ValueString(),
		OidcClientID: plan.ClientID.ValueString(),
	}

	tflog.Debug(ctx, "Updating SCIM service provider", map[string]any{
		"id":        state.ID.ValueString(),
		"client_id": updateReq.OidcClientID,
		"endpoint":  updateReq.Endpoint,
	})

	providerResp, err := r.client.UpdateScimServiceProvider(ctx, state.ID.ValueString(), updateReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating SCIM service provider",
			"Could not update SCIM service provider, unexpected error: "+err.Error(),
		)
		return
	}

	r.mapToState(&plan, providerResp)

	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *scimServiceProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state scimServiceProviderResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SCIM service provider", map[string]any{
		"id": state.ID.ValueString(),
	})

	err := r.client.DeleteScimServiceProvider(ctx, state.ID.ValueString())
	if err != nil {
		// Already gone is the state Delete is after, but only Pocket ID's own
		// "SCIM service provider not found" proves it.
		if client.IsNotFound(err, client.ResourceSCIMServiceProvider) {
			tflog.Debug(ctx, "SCIM service provider was already deleted", map[string]any{
				"id": state.ID.ValueString(),
			})
			return
		}
		resp.Diagnostics.AddError(
			"Error deleting SCIM service provider",
			"Could not delete SCIM service provider, unexpected error: "+err.Error(),
		)
		return
	}
}

// ImportState imports an existing resource into Terraform using the OIDC client ID.
func (r *scimServiceProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("client_id"), req, resp)
}

// scimTokenState returns what state records for the token, given what the
// state held before and what the server returns. Pocket ID always returns the
// token field (decrypted), and "" means no token is configured, which it
// stores identically for an omitted token and an explicitly empty one.
//
//   - A non-empty server token is recorded as is, so a token that was changed or
//     set outside Terraform shows against the configuration.
//   - An empty server token leaves a null or empty prior value alone, so an
//     unconfigured token and an explicitly empty one both plan empty.
//   - An empty server token over a non-empty prior value means the token was
//     cleared outside Terraform: the empty value is recorded so the plan shows
//     the change instead of keeping the obsolete credential.
func scimTokenState(prior types.String, server string) types.String {
	if server != "" {
		return types.StringValue(server)
	}
	if prior.IsNull() || prior.IsUnknown() {
		return types.StringNull()
	}
	return types.StringValue("")
}

// mapToState maps an API response onto the resource model.
func (r *scimServiceProviderResource) mapToState(model *scimServiceProviderResourceModel, provider *client.ScimServiceProvider) {
	model.ID = types.StringValue(provider.ID)
	model.Endpoint = types.StringValue(provider.Endpoint)

	if provider.OidcClient != nil && provider.OidcClient.ID != "" {
		model.ClientID = types.StringValue(provider.OidcClient.ID)
	}

	model.Token = scimTokenState(model.Token, provider.Token)

	if provider.LastSyncedAt != nil {
		model.LastSyncedAt = types.StringValue(*provider.LastSyncedAt)
	} else {
		model.LastSyncedAt = types.StringNull()
	}

	model.CreatedAt = types.StringValue(provider.CreatedAt)
}
