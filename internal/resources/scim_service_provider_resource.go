package resources

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/valuefree"
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
	TokenWO      types.String `tfsdk:"token_wo"`
	TokenWOVer   types.String `tfsdk:"token_wo_version"`
	LastSyncedAt types.String `tfsdk:"last_synced_at"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

// Metadata returns the resource type name.
func (r *scimServiceProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scim_service_provider"
}

// Schema defines the schema for the resource.
func (r *scimServiceProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	defer func() { resp.Schema = valuefree.ResourceSchema(resp.Schema) }()
	resp.Schema = schema.Schema{
		Description: "Manages the SCIM service provider configuration for an OIDC client in Pocket-ID.",
		MarkdownDescription: "Manages the SCIM service provider configuration for an OIDC client in Pocket-ID. " +
			"This enables Pocket-ID to provision users and groups to an external service via SCIM. " +
			"Each OIDC client may have a single SCIM service provider configuration.\n\n" +
			"Import with the OIDC client ID (`<client_id>`) for a configuration that uses `token`: the first refresh stores " +
			"the bearer token Pocket-ID holds in the state, so that it can be compared with the configuration. For a " +
			"configuration that uses `token_wo`, import with `<client_id>,token_wo_version=<version>` instead, using the " +
			"same version as the configuration: the version is set before the first refresh, which then never stores the " +
			"token.",
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
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.MatchRoot("token_wo")),
				},
			},
			"token_wo": schema.StringAttribute{
				Description: "Write-only variant of `token`: the bearer token used to authenticate against the SCIM endpoint, " +
					"sent to Pocket ID but never stored in the plan or the state, so it can come from an ephemeral resource. " +
					"It is sent when the resource is created and whenever `token_wo_version` changes; any other update keeps " +
					"the token Pocket ID already holds. Because the state holds no token, a token that is changed or cleared " +
					"outside Terraform is not detected: change `token_wo_version` to send the token again. " +
					"Conflicts with `token`. Requires Terraform or OpenTofu 1.11 or later.",
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.MatchRoot("token")),
					stringvalidator.AlsoRequires(path.MatchRoot("token_wo_version")),
				},
			},
			"token_wo_version": schema.StringAttribute{
				Description: "A value you change to make the provider send `token_wo` again, for example a rotation date or a " +
					"version label. Required with `token_wo`.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.AlsoRequires(path.MatchRoot("token_wo")),
				},
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
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "configuration", knownAs("OIDC client", plan.ClientID)) {
		return
	}

	// A write-only value exists only in the configuration, never in the plan.
	tokenWO, diags := r.configuredTokenWO(ctx, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	token := plan.Token.ValueString()
	if tokenWO != nil {
		token = *tokenWO
	}

	createReq := &client.ScimServiceProviderCreateRequest{
		Endpoint:     plan.Endpoint.ValueString(),
		Token:        token,
		OidcClientID: plan.ClientID.ValueString(),
	}

	// The endpoint is configured text and never goes to the log (it can
	// carry the API key by mistake); the client ID was checked above.
	tflog.Debug(ctx, "Creating SCIM service provider", map[string]any{
		"client_id": createReq.OidcClientID,
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
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("SCIM service provider", state.ID)) {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("OIDC client", state.ClientID)) {
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
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "configuration", knownAs("OIDC client", plan.ClientID)) {
		return
	}

	var state scimServiceProviderResourceModel
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("SCIM service provider", state.ID)) {
		return
	}

	tokenWO, diags := r.configuredTokenWO(ctx, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// PUT replaces the token: Pocket ID stores an omitted token as "" and
	// clears the one it held. With the write-only variant the plan carries no
	// token, so an update that does not send the value again must send back
	// the token the server holds, or it would erase it.
	token := plan.Token.ValueString()
	providerID := state.ID.ValueString()
	if tokenWO != nil {
		if plan.TokenWOVer.Equal(state.TokenWOVer) {
			current, heldID, err := r.client.GetScimServiceProviderToken(ctx, plan.ClientID.ValueString(), providerID)
			if err != nil {
				detail := "The update would replace the bearer token Pocket ID holds, which Terraform does not know " +
					"(token_wo is write-only), so it must read it first. Nothing was changed. "
				if errors.Is(err, client.ErrUnexpectedAnswer) {
					detail += "Pocket ID's answer to the read was not this SCIM service provider with its token, so it was not used."
				} else {
					detail += "The read failed: " + err.Error()
				}
				resp.Diagnostics.AddError("Error reading SCIM service provider before update", detail)
				return
			}
			// The PUT addresses the provider whose token was read, in the
			// server's spelling of its ID.
			token, providerID = current, heldID
		} else {
			token = *tokenWO
		}
	}

	updateReq := &client.ScimServiceProviderCreateRequest{
		Endpoint:     plan.Endpoint.ValueString(),
		Token:        token,
		OidcClientID: plan.ClientID.ValueString(),
	}

	tflog.Debug(ctx, "Updating SCIM service provider", map[string]any{
		"id":        providerID,
		"client_id": updateReq.OidcClientID,
	})

	providerResp, err := r.client.UpdateScimServiceProvider(ctx, providerID, updateReq)
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
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("SCIM service provider", state.ID)) {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("OIDC client", state.ClientID)) {
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

// scimImportVersionKey introduces the write-only import form,
// "<client_id>,token_wo_version=<version>". A client ID is 2 to 128 letters,
// digits, '.', '_' and '-', so neither ',' nor '=' can occur in one.
const scimImportVersionKey = "token_wo_version="

// parseScimImportID splits an import ID into the OIDC client ID and, for the
// write-only form, the token_wo_version to seed before the first Read. Nothing
// of the ID is put in the error: it is typed by the user and the form is fixed.
func parseScimImportID(id string) (clientID string, version types.String, err error) {
	clientPart, rest, hasVersion := strings.Cut(id, ",")
	if err := client.ValidateClientID(clientPart); err != nil {
		return "", types.StringNull(), errors.New("the import ID must start with a valid OIDC client ID")
	}
	if !hasVersion {
		return clientPart, types.StringNull(), nil
	}
	value, ok := strings.CutPrefix(rest, scimImportVersionKey)
	if !ok || value == "" {
		return "", types.StringNull(), errors.New("after the client ID, the import ID may only add ,token_wo_version=<version> with a non-empty version")
	}
	return clientPart, types.StringValue(value), nil
}

// ImportState imports an existing resource into Terraform. The import ID is
// the OIDC client ID, which stores the token Pocket ID holds in the state on
// the first Read (the ordinary form, for configurations that use `token`), or
// "<client_id>,token_wo_version=<version>", which seeds token_wo_version first
// so that the Read never stores the token (the write-only form, for
// configurations that use `token_wo`: set the same version there).
func (r *scimServiceProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clientID, version, err := parseScimImportID(req.ID)
	if err == nil {
		err = r.client.ValidateIdentifier("OIDC client", clientID)
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			err.Error()+". Use <client_id> for a configuration with `token`, or <client_id>,token_wo_version=<version> "+
				"for one with `token_wo`.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("client_id"), clientID)...)
	if !version.IsNull() {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("token_wo_version"), version)...)
	}
}

// configuredTokenWO returns the write-only token from the configuration, or
// nil when the configuration does not use it. The value is only ever passed to
// the API request: it is never put in a model, a log field or a diagnostic.
func (r *scimServiceProviderResource) configuredTokenWO(ctx context.Context, config tfsdk.Config) (*string, diag.Diagnostics) {
	var value types.String
	diags := config.GetAttribute(ctx, path.Root("token_wo"), &value)
	if diags.HasError() || value.IsNull() || value.IsUnknown() {
		return nil, diags
	}
	token := value.ValueString()
	return &token, diags
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

	// With the write-only variant the state never holds the token, whatever
	// the server returns.
	if model.TokenWOVer.IsNull() || model.TokenWOVer.IsUnknown() {
		model.Token = scimTokenState(model.Token, provider.Token)
	} else {
		model.Token = types.StringNull()
	}
	model.TokenWO = types.StringNull()

	if provider.LastSyncedAt != nil {
		model.LastSyncedAt = types.StringValue(*provider.LastSyncedAt)
	} else {
		model.LastSyncedAt = types.StringNull()
	}

	model.CreatedAt = types.StringValue(provider.CreatedAt)
}
