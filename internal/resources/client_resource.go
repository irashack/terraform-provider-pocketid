package resources

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
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
	IsGroupRestricted                   types.Bool   `tfsdk:"is_group_restricted"`
	HasLogo                             types.Bool   `tfsdk:"has_logo"`
	RequiresReauthentication            types.Bool   `tfsdk:"requires_reauthentication"`
	RequiresPushedAuthorizationRequests types.Bool   `tfsdk:"requires_pushed_authorization_requests"`
	LaunchURL                           types.String `tfsdk:"launch_url"`
	FederatedIdentities                 types.List   `tfsdk:"federated_identities"`
	Description                         types.String `tfsdk:"description"`
	SkipConsent                         types.Bool   `tfsdk:"skip_consent"`
	AccessTokenDurationMinutes          types.Int64  `tfsdk:"access_token_duration_minutes"`
	RefreshTokenDurationMinutes         types.Int64  `tfsdk:"refresh_token_duration_minutes"`
	HasDarkLogo                         types.Bool   `tfsdk:"has_dark_logo"`
	ClientType                          types.String `tfsdk:"client_type"`
	PkceSupported                       types.Bool   `tfsdk:"pkce_supported"`
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
				Description: "Whether this is a public client (no client secret). Defaults to false. Changes in place: a client that becomes confidential gets a secret generated when `generate_secret` is true, and one that becomes public has the secret this resource generated revoked.",
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
				Description: "IDs of the user groups whose members may use this client (when `is_group_restricted` is true). Omitting it and setting it to `[]` both mean none. " +
					"Pocket ID silently ignores an ID that names no group; the apply then fails and names it.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"is_group_restricted": schema.BoolAttribute{
				Description: "Whether only members of `allowed_user_groups` may sign in to this client. When omitted, the client is restricted if `allowed_user_groups` is not empty or if it is restricted already: " +
					"giving a client groups restricts it, and removing them never opens a restricted client to everyone (it then admits nobody). " +
					"Set it to false to let every user sign in; together with a non-empty `allowed_user_groups` that is an error. Set it to true with no groups to admit nobody.",
				Optional: true,
				Computed: true,
			},
			"has_logo": schema.BoolAttribute{
				Description: "Whether the client has a logo configured.",
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"description": schema.StringAttribute{
				Description: "A description of the client, at most 150 characters. When omitted, the client keeps the description it has (for example one set in the admin UI) and state shows it. Set it to `\"\"` to remove it.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthAtMost(150),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"skip_consent": schema.BoolAttribute{
				Description: "Whether users are not asked to consent before signing in to this client. When omitted, the client keeps its current setting and state shows it.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"access_token_duration_minutes": schema.Int64Attribute{
				Description: "Lifetime of the client's access tokens in minutes, 1 to 525600 (Pocket ID's default is 60). When omitted, the client keeps its current setting and state shows it.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.Int64{
					int64validator.Between(clientTokenMinutesMin, clientTokenMinutesMax),
				},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"refresh_token_duration_minutes": schema.Int64Attribute{
				Description: "Lifetime of the client's refresh tokens in minutes, 1 to 525600 (Pocket ID's default is 43200, 30 days). When omitted, the client keeps its current setting and state shows it.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.Int64{
					int64validator.Between(clientTokenMinutesMin, clientTokenMinutesMax),
				},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"has_dark_logo": schema.BoolAttribute{
				Description: "Whether the client has a logo for dark mode.",
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"client_type": schema.StringAttribute{
				Description: "How the client was registered: `standard`. (Clients registered from a Client ID Metadata Document, `cimd`, are not managed by this resource.)",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"pkce_supported": schema.BoolAttribute{
				Description: "Whether Pocket ID saw this client use PKCE although `pkce_enabled` is false; a hint that PKCE can be enabled. An update with `pkce_enabled = false` resets it.",
				Computed:    true,
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

	// An unrestricted client admits every user: groups would mean nothing,
	// and Pocket ID clears them.
	if !config.IsGroupRestricted.IsNull() && !config.IsGroupRestricted.IsUnknown() && !config.IsGroupRestricted.ValueBool() && knownSetSize(config.AllowedUserGroups) > 0 {
		resp.Diagnostics.AddAttributeError(
			path.Root("allowed_user_groups"),
			"Conflicting group restriction",
			"allowed_user_groups has no effect with is_group_restricted = false: every user may sign in. Remove the groups, or set is_group_restricted = true (or omit it).",
		)
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
		if !definitelyRejected(err) {
			detail += ". The POST result is uncertain; inspect read-only before retrying. No cleanup was attempted."
			if createReq.ClientID != nil {
				plan.ID = types.StringValue(*createReq.ClientID)
				plan.ClientID = plan.ID
				plan.IsGroupRestricted = types.BoolValue(createReq.IsGroupRestricted)
				plan.ClientSecret = types.StringNull()
				plan.ClientSecretID = types.StringNull()
				if found, readErr := r.client.GetClient(ctx, *createReq.ClientID); readErr == nil {
					fillComputedFromServer(&plan, found)
					detail += " A read found the fixed-ID client; its identity is retained in state."
				} else {
					fillComputedFromServer(&plan, &client.OIDCClient{})
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
	plan.IsGroupRestricted = types.BoolValue(createReq.IsGroupRestricted)
	plan.RequiresReauthentication = apiModel.RequiresReauthentication
	plan.FederatedIdentities = apiModel.FederatedIdentities
	fillComputedFromServer(&plan, clientResp)
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
	//
	// From that revocation (with its confirming list) to the generation of
	// this resource's own secret, no other secret change of this process may
	// touch the client (client_secret_lock.go): the lock is taken as soon as
	// the client's ID exists, and released before the group update and any
	// cleanup, which touch no secret.
	unlock := lockClientSecrets(clientResp.ID)
	plan.ClientSecret = types.StringNull()
	plan.ClientSecretID = types.StringNull()
	if clientResp.CreatedSecret != nil {
		if err := r.revokeServerCreatedSecret(ctx, clientResp.ID, clientResp.CreatedSecret.ID); err != nil {
			unlock()
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
			unlock()
			r.failedCreate(ctx, &plan, err, resp)

			return
		}
		plan.ClientSecret = types.StringValue(secret.Value)
		plan.ClientSecretID = optionalString(secret.ID)
	}
	unlock()

	// The client was created with its restriction already in place, so it
	// never admits more users than planned; its groups follow. A new client
	// has no signed-in users to notify.
	if groupIDs := setStrings(plan.AllowedUserGroups); len(groupIDs) > 0 {
		if _, err := r.writeAllowedGroups(ctx, clientResp.ID, groupIDs); err != nil {
			r.failedCreate(ctx, &plan, err, resp)

			return
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

	if clientResp.ClientType == client.ClientTypeCIMD {
		resp.Diagnostics.AddError("Cannot manage a Client ID Metadata Document client", errCIMDClient.Error()+". Remove it from state with `terraform state rm`.")
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
	state.IsGroupRestricted = types.BoolValue(clientResp.IsGroupRestricted)
	state.Description = types.StringValue(clientResp.Description)
	state.SkipConsent = types.BoolValue(clientResp.SkipConsent)
	state.AccessTokenDurationMinutes = optionalMinutes(clientResp.AccessTokenDurationMinutes)
	state.RefreshTokenDurationMinutes = optionalMinutes(clientResp.RefreshTokenDurationMinutes)
	state.HasDarkLogo = types.BoolValue(clientResp.HasDarkLogo)
	state.ClientType = optionalString(clientResp.ClientType)
	state.PkceSupported = types.BoolValue(clientResp.PkceSupported)

	// client_secret is never returned by Pocket ID after creation, so it
	// stays as stored. State written before generate_secret existed always
	// generated a secret; state written before client_secret_id existed gets
	// it when the stored secret can be identified.
	if state.GenerateSecret.IsNull() || state.GenerateSecret.IsUnknown() {
		state.GenerateSecret = types.BoolValue(true)
	}
	r.fillSecretID(ctx, &state)
	if privateFlag(ctx, req.Private, pendingRevocationKey) && !r.reconcilePendingRevocation(ctx, &state) && resp.Private != nil {
		resp.Diagnostics.Append(setPrivateFlag(ctx, resp.Private, pendingRevocationKey, false)...)
	}

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
	if current.ClientType == client.ClientTypeCIMD {
		resp.Diagnostics.AddError("Cannot manage a Client ID Metadata Document client", errCIMDClient.Error()+". No change was made.")
		return
	}
	var currentIdentities []client.OIDCClientFederatedIdentity
	if federatedIdentitiesNeedServerValues(ctx, plan.FederatedIdentities) {
		currentIdentities = current.Credentials.FederatedIdentities
	}

	// Group restriction: never wider than planned. Restricting a client
	// sends its groups first; see the ordering below.
	wantGroups := setStrings(plan.AllowedUserGroups)
	isGroupRestricted := resolveGroupRestriction(plan.IsGroupRestricted, wantGroups, current.IsGroupRestricted)
	// An omitted is_group_restricted is planned false only from state that
	// recorded an open client. If the server has been restricted since, the
	// plan was made from stale state (an unrefreshed plan) and never showed
	// opening the client: refuse instead of opening it.
	if config.IsGroupRestricted.IsNull() && !isGroupRestricted && current.IsGroupRestricted {
		resp.Diagnostics.AddAttributeError(path.Root("is_group_restricted"), "Client restricted since the last refresh",
			"The client is group-restricted in Pocket ID, but this plan was made from state that recorded it unrestricted, so applying it would open the client to every user. "+
				"No change was made. Plan again with a refresh (without -refresh=false); to open the client, set is_group_restricted = false.")
		return
	}
	if !isGroupRestricted && len(wantGroups) > 0 {
		resp.Diagnostics.AddAttributeError(path.Root("allowed_user_groups"), "Conflicting group restriction",
			"allowed_user_groups has no effect with is_group_restricted = false: every user may sign in. No change was made.")
		return
	}
	currentGroups := userGroupIDList(current.AllowedUserGroups)

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
	pendingRevocation := privateFlag(ctx, req.Private, pendingRevocationKey)
	secretAction, _ := planSecretAction(state, plan, pendingRevocation)
	// recordPending keeps or clears a revocation still due, for the next plan.
	recordPending := func(pending bool) {
		if resp.Private != nil {
			resp.Diagnostics.Append(setPrivateFlag(ctx, resp.Private, pendingRevocationKey, pending)...)
		}
	}
	// The checks below read the client's secrets and interpret them, so no
	// other secret change of this process may touch the client meanwhile
	// (client_secret_lock.go). The lock is released before the group and
	// client updates, which touch no secret, and taken again for the secret
	// change itself; neither holder takes it again.
	unlockChecks := lockClientSecrets(plan.ID.ValueString())
	revokeID, revokeGone, checksOK := r.updateSecretChecks(ctx, state, plan, pendingRevocation, secretAction, req.Private, resp)
	unlockChecks()
	if !checksOK {
		return
	}

	preserveUnmanagedClientFields(updateReq, current)
	// Optional settings: a configured value is sent; an omitted one keeps
	// the value the server holds right now (preserveUnmanagedClientFields).
	if !config.Description.IsNull() {
		updateReq.Description = plan.Description.ValueString()
	}
	if !config.SkipConsent.IsNull() {
		updateReq.SkipConsent = plan.SkipConsent.ValueBool()
	}
	if !config.AccessTokenDurationMinutes.IsNull() {
		updateReq.AccessTokenDurationMinutes = plan.AccessTokenDurationMinutes.ValueInt64()
	}
	if !config.RefreshTokenDurationMinutes.IsNull() {
		updateReq.RefreshTokenDurationMinutes = plan.RefreshTokenDurationMinutes.ValueInt64()
	}

	// On Pocket ID 2.17, turning the restriction on signs out every user who
	// authorized the client and is in none of its allowed groups at that
	// moment (UpdateClient -> NotifyLostGroupAccess). The groups are therefore
	// written first, while the client's restriction is unchanged: an
	// unrestricted client admits everyone either way, and a restricted one
	// notifies only users who really lose access. Lifting the restriction
	// needs no group write: Pocket ID clears the groups itself.
	if isGroupRestricted && !sameMembers(wantGroups, currentGroups) {
		got, err := r.writeAllowedGroups(ctx, plan.ID.ValueString(), wantGroups)
		if err != nil {
			detail := "The client itself was not updated. "
			if got != nil {
				// The write happened; record what the server kept.
				state.AllowedUserGroups = groupSetFromServer(groupsFromIDs(got), plan.AllowedUserGroups)
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			} else if !definitelyRejected(err) {
				detail += "The allowed groups may have changed; refresh to see them. "
			}
			resp.Diagnostics.AddAttributeError(path.Root("allowed_user_groups"), "Error updating allowed user groups", detail+err.Error())
			return
		}
		state.AllowedUserGroups = plan.AllowedUserGroups
	}

	tflog.Debug(ctx, "Updating OIDC client", map[string]any{
		"id":   plan.ID.ValueString(),
		"name": updateReq.Name,
	})

	updateReq.IsGroupRestricted = isGroupRestricted
	clientResp, err := r.client.UpdateClient(ctx, plan.ID.ValueString(), updateReq)
	if err != nil {
		if !definitelyRejected(err) {
			// The update may have been applied although its answer was
			// lost (the client may now be confidential or public). Keep a
			// planned secret step due for the next plan, which the
			// refreshed state alone would no longer show.
			recordPending(skipSecretAction(secretAction, state, &state))
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		resp.Diagnostics.AddError(
			"Error updating OIDC client",
			"Could not update OIDC client, unexpected error: "+err.Error(),
		)
		return
	}

	// Update state values. Planned values that were known are kept: an
	// unconfigured setting changed outside Terraform since the last refresh
	// was sent back unchanged and shows up on the next refresh.
	fillComputedFromServer(&plan, clientResp)
	plan.RequiresReauthentication = types.BoolValue(clientResp.RequiresReauthentication)
	plan.FederatedIdentities = federatedIdentitiesToList(ctx, clientResp.Credentials.FederatedIdentities)
	// Preserve the configured PAR value unless the server returns the field.
	if clientResp.RequiresPushedAuthorizationRequests != nil {
		plan.RequiresPushedAuthorizationRequests = types.BoolValue(*clientResp.RequiresPushedAuthorizationRequests)
	}
	plan.IsGroupRestricted = types.BoolValue(isGroupRestricted)

	// Verify the restriction the server applied.
	if clientResp.IsGroupRestricted != isGroupRestricted || (!isGroupRestricted && len(clientResp.AllowedUserGroups) > 0) {
		plan.IsGroupRestricted = types.BoolValue(clientResp.IsGroupRestricted)
		plan.AllowedUserGroups = groupSetFromServer(clientResp.AllowedUserGroups, plan.AllowedUserGroups)
		resp.Diagnostics.AddAttributeError(path.Root("is_group_restricted"), "Group restriction not applied",
			fmt.Sprintf("Pocket ID reports is_group_restricted = %t with %d allowed groups after the update, which is not what was planned. State records what it reports; no further change was made.", clientResp.IsGroupRestricted, len(clientResp.AllowedUserGroups)))
		recordPending(skipSecretAction(secretAction, state, &plan))
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	unlockAction := lockClientSecrets(plan.ID.ValueString())
	outcome, err := r.applySecretAction(ctx, secretAction, revokeID, revokeGone, state, &plan)
	unlockAction()
	if err != nil {
		resp.Diagnostics.AddError("Error updating the client secret", "The client itself was updated. "+err.Error())
	}
	recordPending(outcome.pendingRevocation)
	if secretAction == secretGenerate && resp.Private != nil {
		resp.Diagnostics.Append(writeUncertainGeneration(ctx, resp.Private, outcome)...)
	}

	// Set the state
	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// updateSecretChecks runs Update's checks of the client's secrets before
// anything changes, with the caller holding lockClientSecrets: a pending
// revocation that this plan cancels keeps the secret only if it still
// exists (the revocation may have gone through although its answer was
// lost); a generation after one whose result was lost goes ahead only when
// the client has no secret this resource cannot account for; and a secret to
// revoke is identified, so that it can be told apart from the client's other
// secrets. It reports false, with an error diagnostic, when Update must stop
// before any change.
func (r *clientResource) updateSecretChecks(ctx context.Context, state, plan clientResourceModel, pendingRevocation bool, action secretAction, private privateGetter, resp *resource.UpdateResponse) (revokeID string, revokeGone, ok bool) {
	if pendingRevocation && action == secretKeep && holdsSecret(state) {
		if present, err := r.heldSecretPresent(ctx, state); err != nil || !present {
			detail := "it has been revoked"
			if err != nil {
				detail = "whether it still exists could not be established (" + err.Error() + ")"
			}
			resp.Diagnostics.AddError("Client secret in state may no longer exist",
				"An earlier apply could not confirm the revocation of the secret in state, and "+detail+". No change was made. "+
					"Plan again with a refresh (without -refresh=false): a refresh that finds the secret gone removes it from state, and the plan then generates a new one.")
			return "", false, false
		}
	}
	if marker, uncertain := readUncertainGeneration(ctx, private); uncertain && action == secretGenerate {
		if err := r.checkUncertainGeneration(ctx, plan.ID.ValueString(), marker); err != nil {
			resp.Diagnostics.AddError("Cannot create the client secret yet", err.Error()+". No change was made.")
			return "", false, false
		}
	}
	if action == secretRevoke {
		id, gone, err := r.heldSecretID(ctx, plan.ID.ValueString(), state)
		if err != nil {
			resp.Diagnostics.AddError("Cannot revoke the client secret", err.Error()+". No change was made.")
			return "", false, false
		}
		return id, gone, true
	}
	return "", false, true
}

// ModifyPlan plans the attributes that depend on several others.
func (r *clientResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan, config clientResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	var state *clientResourceModel
	if !req.State.Raw.IsNull() {
		state = &clientResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	if state != nil && clientIDOutOfDate(*state, config) {
		resp.Diagnostics.AddAttributeError(path.Root("client_id"), "Client ID in state is out of date",
			fmt.Sprintf("State records client_id %q, but the client's ID is %q: an earlier provider version recorded a change of client_id that Pocket ID never made. "+
				"Plan with a refresh (without -refresh=false); the plan then replaces the client, because Pocket ID cannot change a client's ID.",
				state.ClientID.ValueString(), state.ID.ValueString()))
		return
	}
	planSecretAttributes(state, &plan, privateFlag(ctx, req.Private, pendingRevocationKey))
	if _, uncertain := readUncertainGeneration(ctx, req.Private); uncertain && state != nil {
		if action, known := planSecretAction(*state, plan, privateFlag(ctx, req.Private, pendingRevocationKey)); known && action == secretGenerate {
			resp.Diagnostics.AddAttributeWarning(path.Root("client_secret"), "An earlier client secret generation is unresolved",
				"An earlier apply could not confirm whether it created a client secret. Before creating one, this apply checks the client's secrets and stops if there is one this resource cannot account for.")
		}
	}
	planGroupRestriction(state, config, &plan)
	planPkceSupported(state, &plan)
	warnOnOpening(state, plan, &resp.Diagnostics)

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
	if strings.Contains(req.ID, "://") {
		resp.Diagnostics.AddError("Cannot import a Client ID Metadata Document client", errCIMDClient.Error()+". Its ID is the URL of that document.")
		return
	}
	if err := client.ValidateClientID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", "Import a pocketid_client by its client ID. "+err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// errCIMDClient refuses a client registered from a Client ID Metadata
// Document: Pocket ID refreshes its registration from that document, and an
// admin update writes only a few local settings of it, so this resource
// cannot converge on such a client.
var errCIMDClient = errors.New("this client was registered from a Client ID Metadata Document (client_type \"cimd\"), which owns its registration; pocketid_client does not manage such clients; the pocketid_clients data source lists them")

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

	isGroupRestricted := resolveGroupRestriction(plan.IsGroupRestricted, setStrings(plan.AllowedUserGroups), false)

	// Unset optional settings are left to the server's defaults.
	return &client.OIDCClientCreateRequest{
		Description:                         plan.Description.ValueString(),
		SkipConsent:                         plan.SkipConsent.ValueBool(),
		AccessTokenDurationMinutes:          plan.AccessTokenDurationMinutes.ValueInt64(),
		RefreshTokenDurationMinutes:         plan.RefreshTokenDurationMinutes.ValueInt64(),
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
	if definitelyRejected(cause) || errors.Is(cause, errUnidentifiedCreatedSecret) || errors.Is(cause, errGroupsDropped) {
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
