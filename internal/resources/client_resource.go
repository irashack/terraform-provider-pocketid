package resources

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                   = &clientResource{}
	_ resource.ResourceWithConfigure      = &clientResource{}
	_ resource.ResourceWithImportState    = &clientResource{}
	_ resource.ResourceWithValidateConfig = &clientResource{}
	_ resource.ResourceWithModifyPlan     = &clientResource{}
)

func init() { register(NewClientResource) }

// NewClientResource is a helper function to simplify the provider implementation.
func NewClientResource() resource.Resource {
	return &clientResource{}
}

// clientResource is the resource implementation.
type clientResource struct {
	client *client.Client
}

// clientResourceModel maps the resource schema data.
type clientResourceModel struct {
	ID                                  types.String `tfsdk:"id"`
	Name                                types.String `tfsdk:"name"`
	ClientID                            types.String `tfsdk:"client_id"`
	CallbackURLs                        types.List   `tfsdk:"callback_urls"`
	LogoutCallbackURLs                  types.List   `tfsdk:"logout_callback_urls"`
	BackchannelLogoutURL                types.String `tfsdk:"backchannel_logout_url"`
	IsPublic                            types.Bool   `tfsdk:"is_public"`
	PkceEnabled                         types.Bool   `tfsdk:"pkce_enabled"`
	AllowedUserGroups                   types.Set    `tfsdk:"allowed_user_groups"`
	HasLogo                             types.Bool   `tfsdk:"has_logo"`
	RequiresReauthentication            types.Bool   `tfsdk:"requires_reauthentication"`
	RequiresPushedAuthorizationRequests types.Bool   `tfsdk:"requires_pushed_authorization_requests"`
	LaunchURL                           types.String `tfsdk:"launch_url"`
	FederatedIdentities                 types.List   `tfsdk:"federated_identities"`
	GenerateSecret                      types.Bool   `tfsdk:"generate_secret"`
	ClientSecret                        types.String `tfsdk:"client_secret"`
	ClientSecretID                      types.String `tfsdk:"client_secret_id"`
}

// clientFederatedIdentityModel maps a single federated identity nested object.
type clientFederatedIdentityModel struct {
	Issuer           types.String `tfsdk:"issuer"`
	Subject          types.String `tfsdk:"subject"`
	Audience         types.String `tfsdk:"audience"`
	JWKS             types.String `tfsdk:"jwks"`
	PublicKeys       types.List   `tfsdk:"public_keys"`
	ReplayProtection types.Bool   `tfsdk:"replay_protection"`
}

// federatedIdentityAttrTypes is the attribute-type map for a federated identity object.
var federatedIdentityAttrTypes = map[string]attr.Type{
	"issuer":            types.StringType,
	"subject":           types.StringType,
	"audience":          types.StringType,
	"jwks":              types.StringType,
	"public_keys":       publicKeysListType,
	"replay_protection": types.BoolType,
}

// Metadata returns the resource type name.
func (r *clientResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_client"
}

// Schema defines the schema for the resource.
func (r *clientResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OIDC client in Pocket-ID.",
		MarkdownDescription: `Manages an OIDC client in Pocket-ID. OIDC clients are applications that can authenticate users through Pocket-ID.

~> **Note** Pocket ID returns a client secret's value only when the secret is created. The secret this resource generates is stored in state as ` + "`client_secret`" + ` and cannot be recovered by import. To keep secrets out of this resource (for example to rotate them, or to keep them out of state), set ` + "`generate_secret = false`" + ` and manage them with ` + "`pocketid_client_secret`" + `.`,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID of the OIDC client.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The display name of the OIDC client.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 50),
				},
			},
			"client_id": schema.StringAttribute{
				Description: "The client ID: 2 to 128 letters, digits, `.`, `_` or `-`. When omitted, Pocket ID generates one. " +
					"Always equal to `id` once the client exists, including after import. Pocket ID cannot change a client's ID, so configuring a different value replaces the client.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					clientIDValidator{},
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					clientIDReplace{},
				},
			},
			"callback_urls": schema.ListAttribute{
				Description: "List of allowed callback URLs for the OIDC client.",
				Required:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.ValueStringsAre(urlValidator{}),
				},
			},
			"logout_callback_urls": schema.ListAttribute{
				Description: "List of allowed logout callback URLs for the OIDC client. Omitting it and setting it to `[]` both mean none.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.ValueStringsAre(urlValidator{}),
				},
			},
			"backchannel_logout_url": schema.StringAttribute{
				Description: "URL to which Pocket ID sends an OpenID Connect Back-Channel Logout token when a user's access to this client is revoked: the user is disabled or deleted, loses access through a group change, or revokes the authorization, or the client is deleted. " +
					"Must be an absolute http or https URL without a fragment; a public client (is_public = true) requires https. " +
					"Requires Pocket ID 2.17.0 or later. When omitted, the client has no back-channel logout URL.",
				Optional: true,
				Validators: []validator.String{
					backchannelLogoutURLValidator{},
				},
			},
			"is_public": schema.BoolAttribute{
				Description: "Whether this is a public client (no client secret). Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"requires_reauthentication": schema.BoolAttribute{
				Description: "Whether this client requires reauthentication for certain flows. Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"requires_pushed_authorization_requests": schema.BoolAttribute{
				Description: "Whether this client requires Pushed Authorization Requests (PAR, RFC 9126). Defaults to false. " +
					"Public clients can require PAR on Pocket ID 2.10.0 and later; Pocket ID 2.9.0 ignores it for a public client, so the provider refuses that combination there before changing anything. " +
					"Enforced only by Pocket ID versions that support PAR (2.9.0 and later); on older versions the value is stored in state but not enforced.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"launch_url": schema.StringAttribute{
				Description: "The URL the Pocket ID dashboard opens for this client. When omitted, the client keeps the launch URL it has (set in the admin UI, or earlier by Terraform) and state shows it; an update never clears it. Set it to `\"\"` to remove it.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"federated_identities": schema.ListNestedAttribute{
				Description: "List of federated identities (workload identity federation) allowed to authenticate as this client.",
				Optional:    true,
				PlanModifiers: []planmodifier.List{
					federatedReplayProtectionModifier{},
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"issuer": schema.StringAttribute{
							Description: "The issuer of the federated identity token.",
							Required:    true,
						},
						"subject": schema.StringAttribute{
							Description: "The expected subject of the federated identity token.",
							Optional:    true,
						},
						"audience": schema.StringAttribute{
							Description: "The expected audience of the federated identity token.",
							Optional:    true,
						},
						"jwks": schema.StringAttribute{
							Description: "URL of the JWKS used to validate the federated identity token. When neither this nor `public_keys` is set, Pocket ID discovers the keys from the issuer. Conflicts with `public_keys`.",
							Optional:    true,
						},
						"public_keys": schema.ListAttribute{
							Description: "Explicit public keys used to validate the federated identity token, each a JSON-encoded JWK (for example `jsonencode({...})`). Every key must be an asymmetric public key with a unique `kid`, and `use` must be `sig` or absent. Requires Pocket ID 2.15.0 or later. Conflicts with `jwks`.",
							ElementType: jsontypes.NormalizedType{},
							Optional:    true,
							Validators: []validator.List{
								listvalidator.SizeAtLeast(1),
								listvalidator.ConflictsWith(path.MatchRelative().AtParent().AtName("jwks")),
								listvalidator.ValueStringsAre(publicJWKValidator{}),
								uniquePublicKeyIDValidator{},
							},
						},
						"replay_protection": schema.BoolAttribute{
							Description: "Whether a federated identity token may be used only once. When omitted, an identity already managed keeps its current value and a new identity gets `true`, matching the Pocket ID admin UI. Disable it only for an issuer whose tokens are legitimately presented more than once.",
							Optional:    true,
							Computed:    true,
						},
					},
				},
			},
			"pkce_enabled": schema.BoolAttribute{
				Description: "Whether PKCE is enabled for this client. Defaults to true. Pocket ID always requires PKCE for a public client, so `is_public = true` with `pkce_enabled = false` is rejected.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"allowed_user_groups": schema.SetAttribute{
				Description: "IDs of the user groups whose members may use this client. If empty, all users can use this client. Omitting it and setting it to `[]` both mean none.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"has_logo": schema.BoolAttribute{
				Description: "Whether the client has a logo configured.",
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"generate_secret": schema.BoolAttribute{
				Description: "Whether this resource generates a client secret for a confidential client and stores it in `client_secret`. Defaults to true. " +
					"Set it to false when the client's secrets are managed elsewhere, for example by `pocketid_client_secret`; the client then holds no secret from this resource. " +
					"Changing it from true to false revokes the secret this resource generated (a client whose secret cannot be told apart from its other secrets is left unchanged, with an error listing them); " +
					"changing it from false to true generates one. A client imported, or created before this attribute existed, without a secret in state does not get one generated. " +
					"Pocket ID 2.17.0 and later also create a secret of their own for a new confidential client; this resource always revokes that one.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"client_secret": schema.StringAttribute{
				Description: "The client secret this resource generated, when `generate_secret` is true and the client is confidential. Pocket ID returns the value only when it creates the secret, so it is null for an imported client.",
				Computed:    true,
				Sensitive:   true,
			},
			"client_secret_id": schema.StringAttribute{
				Description: "The ID of the secret stored in `client_secret` (Pocket ID 2.14.0 and later). For state written before this attribute existed it is filled in on refresh when the secret can be identified by the prefix Pocket ID keeps of it.",
				Computed:    true,
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *clientResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig rejects configurations the API would silently coerce, giving a
// clear plan-time error instead of an inconsistent-result error after apply.
func (r *clientResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config clientResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Pocket ID requires https for a public client's back-channel logout URL.
	// Other problems with the URL are reported by its attribute validator.
	if !config.BackchannelLogoutURL.IsNull() && !config.BackchannelLogoutURL.IsUnknown() && !config.IsPublic.IsUnknown() {
		value := config.BackchannelLogoutURL.ValueString()
		if backchannelLogoutURLProblem(value, false) == "" {
			if problem := backchannelLogoutURLProblem(value, config.IsPublic.ValueBool()); problem != "" {
				resp.Diagnostics.AddAttributeError(path.Root("backchannel_logout_url"), "Invalid back-channel logout URL", "backchannel_logout_url "+problem+".")
			}
		}
	}

	// Pocket ID forces PKCE on for a public client (updateOIDCClientModelFromDto:
	// PkceEnabled = IsPublic || PkceEnabled), so this combination would never
	// converge.
	if config.IsPublic.ValueBool() && !config.PkceEnabled.IsNull() && !config.PkceEnabled.IsUnknown() && !config.PkceEnabled.ValueBool() {
		resp.Diagnostics.AddAttributeError(
			path.Root("pkce_enabled"),
			"Invalid PKCE configuration",
			"Pocket ID always requires PKCE for a public client. Remove pkce_enabled = false, or set is_public = false.",
		)
	}
}

// publicPARMinVersion is the first Pocket ID that stores
// requiresPushedAuthorizationRequests for a public client; 2.9.0 forced it to
// false (RequiresPushedAuthorizationRequests = !IsPublic && ...).
const publicPARMinVersion = "2.10.0"

// checkPublicPARSupport refuses, before any mutation, a public client that
// requires PAR on a server that would silently drop the setting.
func checkPublicPARSupport(ctx context.Context, api *client.Client, isPublic, par bool) error {
	if !isPublic || !par {
		return nil
	}
	supported, err := api.VersionAtLeast(ctx, publicPARMinVersion)
	if err != nil {
		return fmt.Errorf("could not verify that the server accepts PAR for a public client: %w", err)
	}
	if !supported {
		return fmt.Errorf("a public client requiring PAR needs Pocket ID %s or later; no mutation was attempted", publicPARMinVersion)
	}
	return nil
}

// Create creates the resource and sets the initial Terraform state.
func (r *clientResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan clientResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Convert from Terraform types to Go types
	var callbackURLs []string
	diags = plan.CallbackURLs.ElementsAs(ctx, &callbackURLs, false)
	resp.Diagnostics.Append(diags...)

	var logoutCallbackURLs []string
	if !plan.LogoutCallbackURLs.IsNull() {
		diags = plan.LogoutCallbackURLs.ElementsAs(ctx, &logoutCallbackURLs, false)
		resp.Diagnostics.Append(diags...)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// Build the create request using helper
	createReq := buildCreateRequestFromPlan(ctx, &plan)
	if !plan.ClientID.IsNull() && !plan.ClientID.IsUnknown() && plan.ClientID.ValueString() != "" {
		cid := plan.ClientID.ValueString()
		createReq.ClientID = &cid
	}

	tflog.Debug(ctx, "Creating OIDC client", map[string]any{
		"name":     createReq.Name,
		"isPublic": createReq.IsPublic,
	})

	// Resolve the API contract before making any client or secret mutation.
	if err := checkFederatedPublicKeysSupport(ctx, r.client, createReq.Credentials); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("federated_identities"), "Unsupported federated identity configuration", err.Error())
		return
	}
	if err := checkBackchannelLogoutSupport(ctx, r.client, createReq.BackchannelLogoutURL); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("backchannel_logout_url"), "Unsupported back-channel logout configuration", err.Error())
		return
	}
	if err := checkPublicPARSupport(ctx, r.client, createReq.IsPublic, createReq.RequiresPushedAuthorizationRequests); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("requires_pushed_authorization_requests"), "Unsupported PAR configuration", err.Error())
		return
	}
	if !plan.IsPublic.ValueBool() && plan.GenerateSecret.ValueBool() {
		if err := r.client.CheckSecretAPI(ctx); err != nil {
			resp.Diagnostics.AddError("Cannot verify secret API compatibility", err.Error())
			return
		}
	}
	// A caller-supplied ID is checked before creation. Never claim or clean up
	// an existing client just because POST failed (including a conflict).
	if createReq.ClientID != nil {
		_, err := r.client.GetClient(ctx, *createReq.ClientID)
		var status *client.HTTPError
		if err == nil || !errors.As(err, &status) || status.StatusCode != 404 {
			resp.Diagnostics.AddError("Cannot create fixed-ID OIDC client", "The client already exists or its absence could not be verified. Import an existing client instead; no mutation was attempted.")
			return
		}
	}
	clientResp, err := r.client.CreateClient(ctx, createReq)
	if err != nil {
		detail := "Client creation failed: " + err.Error()
		if !client.IsDefiniteRejection(err) {
			detail += ". The POST result is uncertain; inspect read-only before retrying. No cleanup was attempted."
			if createReq.ClientID != nil {
				plan.ID = types.StringValue(*createReq.ClientID)
				plan.ClientID = plan.ID
				plan.ClientSecret = types.StringNull()
				plan.ClientSecretID = types.StringNull()
				plan.HasLogo = types.BoolValue(false)
				if plan.LaunchURL.IsUnknown() {
					plan.LaunchURL = types.StringNull()
				}
				if found, readErr := r.client.GetClient(ctx, *createReq.ClientID); readErr == nil {
					plan.HasLogo = types.BoolValue(found.HasLogo)
					detail += " A read found the fixed-ID client; its identity is retained in state."
				} else {
					detail += " The fixed ID is retained for recovery; its existence could not be confirmed."
				}
				resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			} else {
				detail += " No server-generated ID was received; list clients before deciding recovery."
			}
		}
		resp.Diagnostics.AddError("Error creating OIDC client", detail)

		return
	}

	tflog.Debug(ctx, "Created OIDC client", map[string]any{
		"id": clientResp.ID,
	})

	// Map API response to Terraform model and preserve fields
	apiModel := mapAPIClientToModel(ctx, clientResp)
	plan.ID = apiModel.ID
	plan.ClientID = apiModel.ID
	plan.HasLogo = apiModel.HasLogo
	plan.RequiresReauthentication = apiModel.RequiresReauthentication
	plan.FederatedIdentities = apiModel.FederatedIdentities
	if plan.LaunchURL.IsUnknown() {
		plan.LaunchURL = apiModel.LaunchURL
	}
	// Preserve the configured PAR value when the server does not return the field
	// (Pocket-ID <= v2.8.0). Only override from the API when it is present.
	if clientResp.RequiresPushedAuthorizationRequests != nil {
		plan.RequiresPushedAuthorizationRequests = types.BoolValue(*clientResp.RequiresPushedAuthorizationRequests)
	}

	// Pocket ID 2.17.0+ may generate a secret while creating the client and
	// return it once. The provider records only the secret it generates
	// below, so the server's is revoked first; left alone it would stay valid
	// without Terraform knowing it exists. Revoking before generating means
	// the client never holds two valid secrets, and a failure here leaves the
	// same outcome as a failed secret generation.
	plan.ClientSecret = types.StringNull()
	plan.ClientSecretID = types.StringNull()
	if clientResp.CreatedSecret != nil {
		if err := r.revokeServerCreatedSecret(ctx, clientResp.ID, clientResp.CreatedSecret.ID); err != nil {
			r.failedCreate(ctx, &plan, err, resp)

			return
		}
	}

	// Generate the secret this resource holds, for a confidential client
	// that asks for one.
	if !plan.IsPublic.ValueBool() && plan.GenerateSecret.ValueBool() {
		tflog.Debug(ctx, "Generating client secret for non-public client")
		secret, err := r.client.GenerateClientSecret(ctx, clientResp.ID, nil)
		if err != nil {
			r.failedCreate(ctx, &plan, err, resp)

			return
		}
		plan.ClientSecret = types.StringValue(secret.Value)
		plan.ClientSecretID = optionalString(secret.ID)
	}

	// Handle allowed user groups
	if !plan.AllowedUserGroups.IsNull() && !plan.AllowedUserGroups.IsUnknown() {
		var groupIDs []string
		diags = plan.AllowedUserGroups.ElementsAs(ctx, &groupIDs, false)
		resp.Diagnostics.Append(diags...)
		if !resp.Diagnostics.HasError() && len(groupIDs) > 0 {
			tflog.Debug(ctx, "Updating allowed user groups", map[string]any{
				"groups": groupIDs,
			})
			// TODO(association-check): the first result is the set of group IDs
			// the server now holds; it drops IDs that name no group. Not
			// compared yet, and an unreadable result is not an error here.
			_, err = r.client.UpdateClientAllowedUserGroups(ctx, clientResp.ID, groupIDs)
			if err != nil && !errors.Is(err, client.ErrResultUnread) {
				r.failedCreate(ctx, &plan, err, resp)

				return
			}
		}
	}

	// Set the state
	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// Read refreshes the Terraform state with the latest data.
func (r *clientResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Get current state
	var state clientResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading OIDC client", map[string]any{
		"id": state.ID.ValueString(),
	})

	// Get client from API
	clientResp, err := r.client.GetClient(ctx, state.ID.ValueString())
	if client.IsOIDCClientNotFound(err) {
		// Only Pocket ID's own not-found error for the client proves it is
		// gone; the next plan then creates it again.
		tflog.Warn(ctx, "OIDC client no longer exists; removing it from state", map[string]any{"id": state.ID.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading OIDC client",
			"Could not read OIDC client ID "+state.ID.ValueString()+": "+err.Error(),
		)
		return
	}

	// Update state from API response
	state.ClientID = types.StringValue(clientResp.ID)
	state.Name = types.StringValue(clientResp.Name)
	state.IsPublic = types.BoolValue(clientResp.IsPublic)
	state.PkceEnabled = types.BoolValue(clientResp.PkceEnabled)
	state.HasLogo = types.BoolValue(clientResp.HasLogo)
	state.RequiresReauthentication = types.BoolValue(clientResp.RequiresReauthentication)
	// Only refresh PAR from the API when the server returns the field; otherwise
	// preserve the existing state value (Pocket-ID <= v2.8.0 omits it). On import
	// there is no prior value, so fall back to the default of false.
	if clientResp.RequiresPushedAuthorizationRequests != nil {
		state.RequiresPushedAuthorizationRequests = types.BoolValue(*clientResp.RequiresPushedAuthorizationRequests)
	} else if state.RequiresPushedAuthorizationRequests.IsNull() || state.RequiresPushedAuthorizationRequests.IsUnknown() {
		state.RequiresPushedAuthorizationRequests = types.BoolValue(false)
	}
	state.FederatedIdentities = federatedIdentitiesToList(ctx, clientResp.Credentials.FederatedIdentities)
	state.LaunchURL = launchURLFromServer(clientResp.LaunchURL, state.LaunchURL)
	state.BackchannelLogoutURL = optionalString(clientResp.BackchannelLogoutURL)

	// Update callback URLs
	callbackURLs, diags := types.ListValueFrom(ctx, types.StringType, clientResp.CallbackURLs)
	resp.Diagnostics.Append(diags...)
	state.CallbackURLs = callbackURLs

	// An empty list reads as null unless state holds an explicit empty
	// value, so that omitting the attribute and setting it to [] are both
	// stable.
	state.LogoutCallbackURLs = stringListFromServer(clientResp.LogoutCallbackURLs, state.LogoutCallbackURLs)
	state.AllowedUserGroups = groupSetFromServer(clientResp.AllowedUserGroups, state.AllowedUserGroups)

	// client_secret is never returned by Pocket ID after creation, so it
	// stays as stored. State written before generate_secret existed always
	// generated a secret; state written before client_secret_id existed gets
	// it when the stored secret can be identified.
	if state.GenerateSecret.IsNull() || state.GenerateSecret.IsUnknown() {
		state.GenerateSecret = types.BoolValue(true)
	}
	r.fillSecretID(ctx, &state)

	// Set the state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// preserveUnmanagedClientFields copies the settings the provider does not
// expose as attributes from the current server state into an update request,
// so that updating a managed attribute does not reset them.
func preserveUnmanagedClientFields(req *client.OIDCClientCreateRequest, current *client.OIDCClient) {
	req.Description = current.Description
	req.SkipConsent = current.SkipConsent
	req.AccessTokenDurationMinutes = current.AccessTokenDurationMinutes
	req.RefreshTokenDurationMinutes = current.RefreshTokenDurationMinutes
	req.HasLogo = current.HasLogo
	req.HasDarkLogo = current.HasDarkLogo
	req.LogoURL = current.LogoURL
	req.DarkLogoURL = current.DarkLogoURL
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *clientResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Retrieve values from plan
	var plan clientResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)

	// Retrieve current state
	var state clientResourceModel
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)

	// The configuration says which optional settings are managed.
	var config clientResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Convert from Terraform types to Go types
	var callbackURLs []string
	diags = plan.CallbackURLs.ElementsAs(ctx, &callbackURLs, false)
	resp.Diagnostics.Append(diags...)

	var logoutCallbackURLs []string
	if !plan.LogoutCallbackURLs.IsNull() {
		diags = plan.LogoutCallbackURLs.ElementsAs(ctx, &logoutCallbackURLs, false)
		resp.Diagnostics.Append(diags...)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// Determine if group restriction is enabled based on allowed_user_groups
	var isGroupRestricted bool
	if !plan.AllowedUserGroups.IsNull() && !plan.AllowedUserGroups.IsUnknown() {
		var groupIDs []string
		_ = plan.AllowedUserGroups.ElementsAs(ctx, &groupIDs, false)
		isGroupRestricted = len(groupIDs) > 0
	}

	// The update endpoint replaces the client in full, so the current client
	// is read first. Settings the provider does not expose are sent back
	// unchanged; without this a description is cleared, skip_consent reverts
	// to false and the token durations fall back to their defaults. Pocket ID
	// likewise replaces the whole federated identity list, so a
	// replay_protection value the plan could not determine is taken from here.
	current, err := r.client.GetClient(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading OIDC client",
			"Could not read the current OIDC client before updating; no mutation was attempted: "+err.Error(),
		)
		return
	}
	var currentIdentities []client.OIDCClientFederatedIdentity
	if federatedIdentitiesNeedServerValues(ctx, plan.FederatedIdentities) {
		currentIdentities = current.Credentials.FederatedIdentities
	}

	// Update the client
	updateReq := &client.OIDCClientCreateRequest{
		Name:                                plan.Name.ValueString(),
		CallbackURLs:                        callbackURLs,
		LogoutCallbackURLs:                  logoutCallbackURLs,
		BackchannelLogoutURL:                backchannelLogoutURLForUpdate(plan.BackchannelLogoutURL, state.BackchannelLogoutURL, current.BackchannelLogoutURL),
		IsPublic:                            plan.IsPublic.ValueBool(),
		RequiresReauthentication:            plan.RequiresReauthentication.ValueBool(),
		RequiresPushedAuthorizationRequests: plan.RequiresPushedAuthorizationRequests.ValueBool(),
		LaunchURL:                           launchURLForUpdate(config.LaunchURL, plan.LaunchURL, current.LaunchURL),
		PkceEnabled:                         plan.PkceEnabled.ValueBool(),
		IsGroupRestricted:                   isGroupRestricted,
		Credentials:                         buildCredentialsFromPlan(ctx, &plan, currentIdentities),
	}
	if err := checkFederatedPublicKeysSupport(ctx, r.client, updateReq.Credentials); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("federated_identities"), "Unsupported federated identity configuration", err.Error())
		return
	}
	if err := checkBackchannelLogoutSupport(ctx, r.client, stringPointer(plan.BackchannelLogoutURL)); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("backchannel_logout_url"), "Unsupported back-channel logout configuration", err.Error())
		return
	}
	if err := checkPublicPARSupport(ctx, r.client, updateReq.IsPublic, updateReq.RequiresPushedAuthorizationRequests); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("requires_pushed_authorization_requests"), "Unsupported PAR configuration", err.Error())
		return
	}
	// A secret to revoke is identified before anything changes: if it cannot
	// be told apart from the client's other secrets, nothing is changed.
	secretAction, _ := planSecretAction(state, plan)
	var revokeID string
	revokeGone := false
	if secretAction == secretRevoke {
		revokeID, revokeGone, err = r.heldSecretID(ctx, plan.ID.ValueString(), state)
		if err != nil {
			resp.Diagnostics.AddError("Cannot revoke the client secret", err.Error()+". No change was made.")
			return
		}
	}

	preserveUnmanagedClientFields(updateReq, current)

	tflog.Debug(ctx, "Updating OIDC client", map[string]any{
		"id":   plan.ID.ValueString(),
		"name": updateReq.Name,
	})

	clientResp, err := r.client.UpdateClient(ctx, plan.ID.ValueString(), updateReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating OIDC client",
			"Could not update OIDC client, unexpected error: "+err.Error(),
		)
		return
	}

	// Update state values. Planned values that were known are kept: an
	// unconfigured setting changed outside Terraform since the last refresh
	// was sent back unchanged and shows up on the next refresh.
	if plan.HasLogo.IsUnknown() {
		plan.HasLogo = types.BoolValue(clientResp.HasLogo)
	}
	plan.RequiresReauthentication = types.BoolValue(clientResp.RequiresReauthentication)
	plan.FederatedIdentities = federatedIdentitiesToList(ctx, clientResp.Credentials.FederatedIdentities)
	// Preserve the configured PAR value unless the server returns the field.
	if clientResp.RequiresPushedAuthorizationRequests != nil {
		plan.RequiresPushedAuthorizationRequests = types.BoolValue(*clientResp.RequiresPushedAuthorizationRequests)
	}
	if plan.LaunchURL.IsUnknown() {
		plan.LaunchURL = optionalString(clientResp.LaunchURL)
	}

	// Handle allowed user groups
	var plannedGroupIDs []string
	if !plan.AllowedUserGroups.IsNull() && !plan.AllowedUserGroups.IsUnknown() {
		diags = plan.AllowedUserGroups.ElementsAs(ctx, &plannedGroupIDs, false)
		resp.Diagnostics.Append(diags...)
	}

	var currentGroupIDs []string
	if !state.AllowedUserGroups.IsNull() && !state.AllowedUserGroups.IsUnknown() {
		diags = state.AllowedUserGroups.ElementsAs(ctx, &currentGroupIDs, false)
		resp.Diagnostics.Append(diags...)
	}

	if !resp.Diagnostics.HasError() {
		// Check if groups have changed
		groupsChanged := false
		if len(plannedGroupIDs) != len(currentGroupIDs) {
			groupsChanged = true
		} else {
			// Check if group IDs are different
			groupMap := make(map[string]bool)
			for _, id := range currentGroupIDs {
				groupMap[id] = true
			}
			for _, id := range plannedGroupIDs {
				if !groupMap[id] {
					groupsChanged = true
					break
				}
			}
		}

		if groupsChanged {
			tflog.Debug(ctx, "Updating allowed user groups", map[string]any{
				"groups": plannedGroupIDs,
			})
			// TODO(association-check): the first result is the set of group IDs
			// the server now holds; it drops IDs that name no group. Not
			// compared yet, and an unreadable result is not an error here.
			_, err = r.client.UpdateClientAllowedUserGroups(ctx, plan.ID.ValueString(), plannedGroupIDs)
			if err != nil && !errors.Is(err, client.ErrResultUnread) {
				resp.Diagnostics.AddError(
					"Error updating allowed user groups",
					"Could not update allowed user groups: "+err.Error(),
				)
				return
			}
		}
	}

	if err := r.applySecretAction(ctx, secretAction, revokeID, revokeGone, state, &plan); err != nil {
		resp.Diagnostics.AddError("Error updating the client secret", "The client itself was updated. "+err.Error())
	}

	// Set the state
	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// ModifyPlan plans the attributes that depend on several others.
func (r *clientResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan clientResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state *clientResourceModel
	if !req.State.Raw.IsNull() {
		state = &clientResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	planSecretAttributes(state, &plan)

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *clientResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Retrieve values from state
	var state clientResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting OIDC client", map[string]any{
		"id": state.ID.ValueString(),
	})

	// Delete the client
	err := r.client.DeleteClient(ctx, state.ID.ValueString())
	if client.IsOIDCClientNotFound(err) {
		tflog.Debug(ctx, "OIDC client was already deleted", map[string]any{"id": state.ID.ValueString()})
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting OIDC client",
			"Could not delete OIDC client, unexpected error: "+err.Error(),
		)
		return
	}

	tflog.Debug(ctx, "Deleted OIDC client", map[string]any{
		"id": state.ID.ValueString(),
	})
}

// ImportState imports an existing resource into Terraform.
func (r *clientResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Retrieve import ID and set it as the resource ID
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// urlValidator validates that a string is a valid URL
type urlValidator struct{}

func (v urlValidator) Description(ctx context.Context) string {
	return "string must be a valid URL"
}

func (v urlValidator) MarkdownDescription(ctx context.Context) string {
	return "string must be a valid URL"
}

func (v urlValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := strings.TrimSpace(req.ConfigValue.ValueString())

	// Reject empty strings after trimming
	if value == "" {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"invalid callback URL",
			"Callback URL must not be empty",
		)
		return
	}

	// Allow wildcard patterns containing '*'
	if strings.Contains(value, "*") {
		return
	}

	// Parse and require a scheme and some content (host or path)
	u, err := url.Parse(value)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"invalid callback URL",
			fmt.Sprintf("The value %q is not a valid URL: %s", value, err),
		)
		return
	}

	if u.Scheme == "" || (u.Host == "" && u.Path == "" && u.Opaque == "") {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"invalid callback URL",
			fmt.Sprintf("The value %q is not a valid URL: must include a scheme and a host, path, or opaque data", value),
		)
		return
	}
}

// buildCreateRequestFromPlan converts the Terraform plan model into an API create request.
func buildCreateRequestFromPlan(ctx context.Context, plan *clientResourceModel) *client.OIDCClientCreateRequest {
	var callbackURLs []string
	_ = plan.CallbackURLs.ElementsAs(ctx, &callbackURLs, false)

	var logoutCallbackURLs []string
	if !plan.LogoutCallbackURLs.IsNull() {
		_ = plan.LogoutCallbackURLs.ElementsAs(ctx, &logoutCallbackURLs, false)
	}

	var launchPtr *string
	if !plan.LaunchURL.IsNull() && !plan.LaunchURL.IsUnknown() && plan.LaunchURL.ValueString() != "" {
		v := plan.LaunchURL.ValueString()
		launchPtr = &v
	}

	// Determine if group restriction is enabled based on allowed_user_groups
	var isGroupRestricted bool
	if !plan.AllowedUserGroups.IsNull() && !plan.AllowedUserGroups.IsUnknown() {
		var groupIDs []string
		_ = plan.AllowedUserGroups.ElementsAs(ctx, &groupIDs, false)
		isGroupRestricted = len(groupIDs) > 0
	}

	return &client.OIDCClientCreateRequest{
		Name:                                plan.Name.ValueString(),
		CallbackURLs:                        callbackURLs,
		LogoutCallbackURLs:                  logoutCallbackURLs,
		BackchannelLogoutURL:                stringPointer(plan.BackchannelLogoutURL),
		IsPublic:                            plan.IsPublic.ValueBool(),
		RequiresReauthentication:            plan.RequiresReauthentication.ValueBool(),
		RequiresPushedAuthorizationRequests: plan.RequiresPushedAuthorizationRequests.ValueBool(),
		LaunchURL:                           launchPtr,
		PkceEnabled:                         plan.PkceEnabled.ValueBool(),
		IsGroupRestricted:                   isGroupRestricted,
		Credentials:                         buildCredentialsFromPlan(ctx, plan, nil),
	}
}

// buildCredentialsFromPlan converts the federated_identities plan list into API credentials.
// current holds the server's identities, consulted only for a replay_protection
// value the plan could not determine; it is nil when creating a client.
func buildCredentialsFromPlan(ctx context.Context, plan *clientResourceModel, current []client.OIDCClientFederatedIdentity) client.OIDCClientCredentials {
	if plan.FederatedIdentities.IsNull() || plan.FederatedIdentities.IsUnknown() {
		return client.OIDCClientCredentials{}
	}

	var identities []clientFederatedIdentityModel
	_ = plan.FederatedIdentities.ElementsAs(ctx, &identities, false)

	if len(identities) == 0 {
		return client.OIDCClientCredentials{}
	}

	replayProtection := resolveReplayProtections(identities, current)
	federated := make([]client.OIDCClientFederatedIdentity, 0, len(identities))
	for i, identity := range identities {
		federated = append(federated, client.OIDCClientFederatedIdentity{
			Issuer:           identity.Issuer.ValueString(),
			Subject:          identity.Subject.ValueString(),
			Audience:         identity.Audience.ValueString(),
			JWKS:             identity.JWKS.ValueString(),
			PublicKeys:       publicKeysToAPI(identity.PublicKeys),
			ReplayProtection: replayProtection[i],
		})
	}

	return client.OIDCClientCredentials{FederatedIdentities: federated}
}

// federatedIdentitiesToList converts API federated identities into a Terraform list value.
func federatedIdentitiesToList(ctx context.Context, identities []client.OIDCClientFederatedIdentity) types.List {
	objType := types.ObjectType{AttrTypes: federatedIdentityAttrTypes}
	if len(identities) == 0 {
		return types.ListNull(objType)
	}

	models := make([]clientFederatedIdentityModel, 0, len(identities))
	for _, identity := range identities {
		models = append(models, clientFederatedIdentityModel{
			Issuer:           types.StringValue(identity.Issuer),
			Subject:          optionalString(identity.Subject),
			Audience:         optionalString(identity.Audience),
			JWKS:             optionalString(identity.JWKS),
			PublicKeys:       publicKeysFromAPI(identity.PublicKeys),
			ReplayProtection: types.BoolValue(identity.ReplayProtection),
		})
	}

	list, _ := types.ListValueFrom(ctx, objType, models)
	return list
}

// optionalString returns a null string value when the input is empty.
func optionalString(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}

// mapAPIClientToModel maps an API OIDCClient response into the Terraform resource model.
func mapAPIClientToModel(ctx context.Context, api *client.OIDCClient) clientResourceModel {
	var model clientResourceModel
	model.ID = types.StringValue(api.ID)
	model.Name = types.StringValue(api.Name)
	model.IsPublic = types.BoolValue(api.IsPublic)
	model.PkceEnabled = types.BoolValue(api.PkceEnabled)
	model.HasLogo = types.BoolValue(api.HasLogo)
	model.RequiresReauthentication = types.BoolValue(api.RequiresReauthentication)
	model.FederatedIdentities = federatedIdentitiesToList(ctx, api.Credentials.FederatedIdentities)

	callbackURLs, _ := types.ListValueFrom(ctx, types.StringType, api.CallbackURLs)
	model.CallbackURLs = callbackURLs

	if len(api.LogoutCallbackURLs) > 0 {
		logoutURLs, _ := types.ListValueFrom(ctx, types.StringType, api.LogoutCallbackURLs)
		model.LogoutCallbackURLs = logoutURLs
	} else {
		model.LogoutCallbackURLs = types.ListNull(types.StringType)
	}

	model.AllowedUserGroups = groupSetFromServer(api.AllowedUserGroups, types.SetNull(types.StringType))

	if api.LaunchURL != "" {
		model.LaunchURL = types.StringValue(api.LaunchURL)
	} else {
		model.LaunchURL = types.StringNull()
	}
	model.BackchannelLogoutURL = optionalString(api.BackchannelLogoutURL)

	return model
}

// errUnidentifiedCreatedSecret means the create response reported a
// server-generated secret without its ID, so it cannot be revoked. No mutation
// result is in doubt, so the new client is rolled back like after a rejection.
var errUnidentifiedCreatedSecret = errors.New("the create response reported a client secret generated by Pocket ID without its ID, so it cannot be revoked")

// revokeServerCreatedSecret revokes the secret the server generated with a new
// client. The DELETE is never retried; only a secret confirmed absent counts as
// revoked (see revokeClientSecret). The returned error names the secret's ID,
// never its value.
func (r *clientResource) revokeServerCreatedSecret(ctx context.Context, clientID, secretID string) error {
	if secretID == "" {
		return errUnidentifiedCreatedSecret
	}
	tflog.Debug(ctx, "Revoking the client secret Pocket ID created with the client", map[string]any{
		"id":        clientID,
		"secret_id": secretID,
	})
	if err := r.revokeClientSecret(ctx, clientID, secretID); err != nil {
		return fmt.Errorf("the secret Pocket ID created with the client: %w", err)
	}
	return nil
}

// failedCreate only rolls back a newly created client after a definite API
// rejection. An ambiguous mutation is inspected, never retried or deleted.
func (r *clientResource) failedCreate(ctx context.Context, plan *clientResourceModel, cause error, resp *resource.CreateResponse) {
	id := plan.ID.ValueString()
	if client.IsDefiniteRejection(cause) || errors.Is(cause, errUnidentifiedCreatedSecret) {
		if cleanupErr := r.client.DeleteClient(ctx, id); cleanupErr == nil {
			resp.Diagnostics.AddError("OIDC client creation rolled back", "The newly created client was deleted after a rejected operation: "+cause.Error())
			return
		} else {
			// A failed DELETE might still have committed. Only Pocket ID's own
			// not-found error for the client confirms that; a bare, proxy or
			// missing-route 404 does not, and dropping the ID on one would
			// orphan the client together with any secret it holds.
			_, readErr := r.client.GetClient(ctx, id)
			if client.IsOIDCClientNotFound(readErr) {
				resp.Diagnostics.AddError("OIDC client rollback verified", "Cleanup returned an error, but a subsequent read confirmed the client is absent: "+cause.Error())
				return
			}
			outcome := "A read found the client still exists."
			if readErr != nil {
				outcome = "Whether the client still exists could not be confirmed (read: " + readErr.Error() + ")."
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
			resp.Diagnostics.AddError("OIDC client cleanup failed", "Client ID "+id+" remains in state. "+outcome+" Stop and inspect before recovery; do not retry apply blindly. Cleanup: "+cleanupErr.Error()+"; original operation: "+cause.Error())
			return
		}
	}
	_, readErr := r.client.GetClient(ctx, id)
	detail := "A read confirmed the client still exists."
	if readErr != nil {
		detail = "Read-only inspection could not confirm the client: " + readErr.Error()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	resp.Diagnostics.AddError("OIDC client creation result uncertain", "Client ID "+id+" is retained in state; no secret POST or cleanup was retried. "+detail+" Stop and inspect before recovery; a create-only secret cannot be recovered by import. "+cause.Error())
}
