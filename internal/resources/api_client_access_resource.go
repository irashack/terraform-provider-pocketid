package resources

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

var (
	_ resource.Resource                   = &apiClientAccessResource{}
	_ resource.ResourceWithConfigure      = &apiClientAccessResource{}
	_ resource.ResourceWithImportState    = &apiClientAccessResource{}
	_ resource.ResourceWithModifyPlan     = &apiClientAccessResource{}
	_ resource.ResourceWithValidateConfig = &apiClientAccessResource{}
)

func init() { register(NewAPIClientAccessResource) }

// NewAPIClientAccessResource returns the pocketid_api_client_access resource.
func NewAPIClientAccessResource() resource.Resource {
	return &apiClientAccessResource{}
}

// apiClientAccessResource manages one OIDC client's grant on one API with
// the per-pair PUT and DELETE, so no shared list is read and written back.
type apiClientAccessResource struct {
	client *client.Client
}

type apiClientAccessModel struct {
	ID                       types.String `tfsdk:"id"`
	APIID                    types.String `tfsdk:"api_id"`
	ClientID                 types.String `tfsdk:"client_id"`
	UserDelegatedAccess      types.Bool   `tfsdk:"user_delegated_access"`
	UserDelegatedPermissions types.Set    `tfsdk:"user_delegated_permissions"`
	ClientAccess             types.Bool   `tfsdk:"client_access"`
	ClientPermissions        types.Set    `tfsdk:"client_permissions"`
}

var (
	apiAccessIDValidator = apiIdentifierValidator{
		description: "must be an API ID (a UUID)",
		check:       func(id string) error { return client.ValidateUUID("API", id) },
	}
	apiAccessClientIDValidator = apiIdentifierValidator{
		description: "must be an OIDC client ID: 2 to 128 letters, digits, '.', '_' or '-'",
		check:       client.ValidateClientID,
	}
)

func (r *apiClientAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_client_access"
}

func (r *apiClientAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	emptySet := setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{}))
	resp.Schema = schema.Schema{
		Description: "Grants one OIDC client access to one Pocket ID API, with the permissions it may request. Requires Pocket ID 2.14.0 or later.",
		MarkdownDescription: "Grants one OIDC client access to one Pocket ID API (see `pocketid_api`), with the permissions it may " +
			"request. Requires Pocket ID 2.14.0 or later.\n\n" +
			"There are two kinds of access. **User-delegated access** lets the client request tokens for the API on behalf of a " +
			"signed-in user. **Client access** lets the client request tokens for itself with the client credentials grant " +
			"(machine to machine). Each comes with its own set of permission keys. Granting a permission turns its kind of " +
			"access on; access without permissions gives tokens for the API that carry no scope.\n\n" +
			"This resource owns exactly one (API, client) pair and writes it with Pocket ID's per-pair endpoints, so it never " +
			"touches the client's grants on other APIs or other clients' grants on this API. Do not manage the same pair with two " +
			"resources.\n\n" +
			"A permission key that contains the admin API key this provider authenticates with is refused (the provider never stores " +
			"or prints that credential), before anything is sent.\n\n" +
			"Before writing, the provider checks that every permission key exists on the API and that a client given client " +
			"access is not public (Pocket ID silently drops both). After writing, it compares what the server stored with the " +
			"configuration and fails, naming the difference, instead of recording access the server did not confirm.\n\n" +
			"Refer to the API as `pocketid_api.<name>.id` so the API's permission changes are applied first. Removing a permission " +
			"from the API deletes its grants; if it was the client's last permission of that kind, Pocket ID also removes that " +
			"kind of access, and the next plan shows it. Deleting the API or the client removes the grant.\n\n" +
			"If an update ends without a confirmed result (the connection failed, or the response was lost or unreadable) and the " +
			"grant cannot be read back either, the provider does not guess: state keeps the last grant it confirmed, the resource " +
			"is marked unresolved, and plans and writes for it are refused until a read succeeds. Run `terraform plan` or " +
			"`terraform apply` without `-refresh=false` to read it, or `terraform state rm` the resource and import it again as " +
			"`<api_id>/<client_id>`. A destroy is never refused.\n\n" +
			"A create that ends that way records the pair without its access flags and permissions, which are unknown until a " +
			"refresh reads them, and Terraform marks the resource tainted. The marker does not apply to that case: Terraform " +
			"plans the replacement of a tainted resource from nothing, so the replacement's destroy revokes whatever grant the " +
			"server holds for the pair and its create then writes the configured grant again. A refresh before that only " +
			"shows what the server holds; to keep the grant that exists instead, `terraform untaint` the resource or " +
			"`terraform state rm` it and import it again.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The resource ID, `<api_id>/<client_id>`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"api_id": schema.StringAttribute{
				Description: "The ID of the API. Changing it replaces the resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{apiAccessIDValidator},
			},
			"client_id": schema.StringAttribute{
				Description: "The ID of the OIDC client. Clients registered through a Client ID Metadata Document cannot be " +
					"addressed here; give them access on the API with `allow_cimd_clients`. Changing it replaces the resource.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{apiAccessClientIDValidator},
			},
			"user_delegated_access": schema.BoolAttribute{
				Description: "Whether the client may request tokens for the API on behalf of a signed-in user. When unset, it is " +
					"`true` exactly when `user_delegated_permissions` is not empty. It cannot be `false` while permissions are listed.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					apiAccessFromPermissions{permissions: path.Root("user_delegated_permissions")},
				},
			},
			"user_delegated_permissions": schema.SetAttribute{
				Description: "The keys of the API's permissions the client may request on behalf of a user. Defaults to none.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     emptySet,
				Validators:  []validator.Set{setvalidator.ValueStringsAre(apiPermissionKeyValidator{})},
			},
			"client_access": schema.BoolAttribute{
				Description: "Whether the client may request tokens for the API for itself with the client credentials grant. " +
					"Public clients cannot be given client access. When unset, it is `true` exactly when `client_permissions` is " +
					"not empty. It cannot be `false` while permissions are listed.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					apiAccessFromPermissions{permissions: path.Root("client_permissions")},
				},
			},
			"client_permissions": schema.SetAttribute{
				Description: "The keys of the API's permissions the client may request for itself. Defaults to none.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     emptySet,
				Validators:  []validator.Set{setvalidator.ValueStringsAre(apiPermissionKeyValidator{})},
			},
		},
	}
}

// apiAccessFromPermissions plans an unset access flag as what Pocket ID will
// store: access is on exactly when permissions of its kind are granted
// (Service.SetAPIClientAccess turns access on for any granted permission).
// A configured value is left alone; ValidateConfig refuses false with
// permissions.
type apiAccessFromPermissions struct {
	permissions path.Path
}

func (m apiAccessFromPermissions) Description(context.Context) string {
	return "when unset, true exactly when permissions of this kind are granted"
}

func (m apiAccessFromPermissions) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m apiAccessFromPermissions) PlanModifyBool(ctx context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if !req.ConfigValue.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var permissions types.Set
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, m.permissions, &permissions)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if permissions.IsUnknown() {
		resp.PlanValue = types.BoolUnknown()
		return
	}
	resp.PlanValue = types.BoolValue(len(permissions.Elements()) > 0)
}

func (r *apiClientAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig refuses grants Pocket ID would store differently: access
// switched off while permissions of its kind are listed (the server turns it
// on), and a grant with nothing in it (the server stores no rows, so the
// resource would never exist).
func (r *apiClientAccessResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg apiClientAccessModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	known := true
	grants := false
	for _, kind := range []struct {
		access      types.Bool
		permissions types.Set
		accessName  string
		listName    string
	}{
		{cfg.UserDelegatedAccess, cfg.UserDelegatedPermissions, "user_delegated_access", "user_delegated_permissions"},
		{cfg.ClientAccess, cfg.ClientPermissions, "client_access", "client_permissions"},
	} {
		if kind.access.IsUnknown() || kind.permissions.IsUnknown() {
			known = false
			continue
		}
		listed := !kind.permissions.IsNull() && len(kind.permissions.Elements()) > 0
		if listed && !kind.access.IsNull() && !kind.access.ValueBool() {
			resp.Diagnostics.AddAttributeError(path.Root(kind.accessName), "Access cannot be off with permissions",
				fmt.Sprintf("%s lists permissions, and Pocket ID turns %s on for any permission granted. Set %s = true or leave it unset.", kind.listName, kind.accessName, kind.accessName))
		}
		if listed || kind.access.ValueBool() {
			grants = true
		}
	}
	if known && !grants {
		resp.Diagnostics.AddError("Grant gives nothing",
			"Set user_delegated_access or client_access, or list permissions. Pocket ID stores nothing for an empty grant; to revoke the client's access, remove this resource.")
	}
}

// apiAccessGrant is a grant expressed with permission keys.
type apiAccessGrant struct {
	UserAccess   bool
	UserKeys     []string
	ClientAccess bool
	ClientKeys   []string
}

func (g apiAccessGrant) grantsClientAccess() bool {
	return g.ClientAccess || len(g.ClientKeys) > 0
}

func apiAccessGrantFromModel(ctx context.Context, m apiClientAccessModel) (apiAccessGrant, diag.Diagnostics) {
	var diags diag.Diagnostics
	grant := apiAccessGrant{UserAccess: m.UserDelegatedAccess.ValueBool(), ClientAccess: m.ClientAccess.ValueBool()}
	diags.Append(m.UserDelegatedPermissions.ElementsAs(ctx, &grant.UserKeys, false)...)
	diags.Append(m.ClientPermissions.ElementsAs(ctx, &grant.ClientKeys, false)...)
	sort.Strings(grant.UserKeys)
	sort.Strings(grant.ClientKeys)
	return grant, diags
}

// apiAccessKeysFromIDs names permission IDs by key with the API's
// permissions. An ID the API does not have is kept as the ID itself, so it
// still shows as a difference from the configuration rather than vanishing
// from state.
func apiAccessKeysFromIDs(ids []string, permissions []client.APIPermission) (keys []string, unnamed []string) {
	keyOf := map[string]string{}
	for _, p := range permissions {
		keyOf[p.ID] = p.Key
	}
	for _, id := range ids {
		if key, ok := keyOf[id]; ok {
			keys = append(keys, key)
		} else {
			keys = append(keys, id)
			unnamed = append(unnamed, id)
		}
	}
	sort.Strings(keys)
	return keys, unnamed
}

// apiAccessFromServer expresses a stored grant with permission keys.
func apiAccessFromServer(g client.APIClientGrant, permissions []client.APIPermission) (apiAccessGrant, []string) {
	userKeys, unnamedUser := apiAccessKeysFromIDs(g.UserDelegatedPermissionIDs, permissions)
	clientKeys, unnamedClient := apiAccessKeysFromIDs(g.ClientPermissionIDs, permissions)
	return apiAccessGrant{UserAccess: g.UserDelegatedAccess, UserKeys: userKeys, ClientAccess: g.ClientAccess, ClientKeys: clientKeys},
		append(unnamedUser, unnamedClient...)
}

func apiAccessModel(apiID, clientID string, g apiAccessGrant) apiClientAccessModel {
	toSet := func(keys []string) types.Set {
		values := make([]attr.Value, 0, len(keys))
		for _, key := range keys {
			values = append(values, types.StringValue(key))
		}
		return types.SetValueMust(types.StringType, values)
	}
	return apiClientAccessModel{
		ID:                       types.StringValue(apiID + "/" + clientID),
		APIID:                    types.StringValue(apiID),
		ClientID:                 types.StringValue(clientID),
		UserDelegatedAccess:      types.BoolValue(g.UserAccess),
		UserDelegatedPermissions: toSet(g.UserKeys),
		ClientAccess:             types.BoolValue(g.ClientAccess),
		ClientPermissions:        toSet(g.ClientKeys),
	}
}

// apiAccessDifferences names every way the stored grant differs from the
// requested one.
func apiAccessDifferences(want, got apiAccessGrant) []string {
	var diffs []string
	flag := func(name string, want, got bool) {
		if want != got {
			diffs = append(diffs, fmt.Sprintf("%s is %t, not %t", name, got, want))
		}
	}
	keys := func(name string, want, got []string) {
		have := map[string]bool{}
		for _, key := range got {
			have[key] = true
		}
		asked := map[string]bool{}
		for _, key := range want {
			asked[key] = true
			if !have[key] {
				diffs = append(diffs, fmt.Sprintf("%s %q was not granted", name, key))
			}
		}
		for _, key := range got {
			if !asked[key] {
				diffs = append(diffs, fmt.Sprintf("%s %q was granted without being requested", name, key))
			}
		}
	}
	flag("user_delegated_access", want.UserAccess, got.UserAccess)
	keys("user-delegated permission", want.UserKeys, got.UserKeys)
	flag("client_access", want.ClientAccess, got.ClientAccess)
	keys("client permission", want.ClientKeys, got.ClientKeys)
	return diffs
}

// apiAccessOutcome is what a write left on the server, for the caller to
// record. At most one field is set; none means nothing was sent or the
// server refused the write, so the prior state still holds.
//
//   - stored: the grant the server confirmed, from its response or a read.
//   - empty: the server confirmed that it holds no grant for the pair.
//   - unresolved: the write may have changed the pair (it was sent and not
//     definitely refused) and nothing confirmed what the pair holds now.
//     Nothing may be recorded as known: an update keeps the last confirmed
//     state, a create keeps only the pair's identity, and both mark the
//     resource so that no plan or write is trusted until a read succeeds.
type apiAccessOutcome struct {
	stored     *apiAccessGrant
	empty      bool
	unresolved bool
}

// apiAccessUnresolvedKey is the private-state key of the unresolved-outcome
// marker. Private state is saved with the resource even when the apply that set
// it failed.
const apiAccessUnresolvedKey = "unresolved_write"

// apiAccessPrivate is the part of the framework's private provider data this
// resource uses (the concrete type is internal to the framework).
type apiAccessPrivate interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

func apiAccessMarkUnresolved(ctx context.Context, private apiAccessPrivate, diags *diag.Diagnostics) {
	diags.Append(private.SetKey(ctx, apiAccessUnresolvedKey, []byte(`{"unresolved":true}`))...)
}

func apiAccessClearUnresolved(ctx context.Context, private apiAccessPrivate, diags *diag.Diagnostics) {
	if apiAccessIsUnresolved(ctx, private, diags) {
		diags.Append(private.SetKey(ctx, apiAccessUnresolvedKey, nil)...)
	}
}

// apiAccessIsUnresolved reports whether an earlier write left the marker.
func apiAccessIsUnresolved(ctx context.Context, private apiAccessPrivate, diags *diag.Diagnostics) bool {
	value, d := private.GetKey(ctx, apiAccessUnresolvedKey)
	diags.Append(d...)
	return len(value) > 0
}

// apiAccessRecovery names the two ways out of an unresolved outcome.
func apiAccessRecovery(apiID, clientID string) string {
	again := "import it again with its API ID and client ID"
	if apiID != apiNotShown && clientID != apiNotShown {
		again = fmt.Sprintf("import it again as %s/%s", apiID, clientID)
	}
	return "Run terraform plan or terraform apply without -refresh=false, which reads the grant and clears this, or run terraform state rm on the resource and " + again + "."
}

func apiAccessUnresolvedDiagnostics(diags *diag.Diagnostics, apiID, clientID string) {
	diags.AddError("API access outcome unresolved",
		fmt.Sprintf("An earlier write of the grant of API %s to client %s ended without a confirmed result. State shows the last grant the provider confirmed, which may not be what the server holds, so no plan or write is accepted for this resource until a read succeeds. %s", apiID, clientID, apiAccessRecovery(apiID, clientID)))
}

// apiAccessWrite checks the request against the server, writes it with the
// per-pair PUT and verifies what was stored. Errors go to diags; the outcome
// says what to record.
func (r *apiClientAccessResource) apiAccessWrite(ctx context.Context, apiID, clientID string, want apiAccessGrant, diags *diag.Diagnostics) apiAccessOutcome {
	// Before any request: a missing key is named in a diagnostic below.
	if !apiPermissionKeysOK(r.client, diags, want.UserKeys, want.ClientKeys) {
		return apiAccessOutcome{}
	}
	if err := checkAPISupport(ctx, r.client); err != nil {
		diags.AddError("Cannot grant API access", err.Error())
		return apiAccessOutcome{}
	}

	api, err := r.client.GetAPI(ctx, apiID)
	if err != nil {
		if client.IsNotFound(err, client.ResourceAPI) {
			diags.AddAttributeError(path.Root("api_id"), "API not found", fmt.Sprintf("API %s does not exist; no mutation was attempted.", apiID))
		} else {
			diags.AddError("Cannot grant API access", fmt.Sprintf("Could not read API %s to resolve permission keys: %s; no mutation was attempted.", apiID, err))
		}
		return apiAccessOutcome{}
	}
	idOf := map[string]string{}
	for _, p := range api.Permissions {
		idOf[p.Key] = p.ID
	}
	resolve := func(keys []string) ([]string, []string) {
		var ids, missing []string
		for _, key := range keys {
			if id, ok := idOf[key]; ok {
				ids = append(ids, id)
			} else {
				missing = append(missing, key)
			}
		}
		return ids, missing
	}
	userIDs, missingUser := resolve(want.UserKeys)
	clientIDs, missingClient := resolve(want.ClientKeys)
	if missing := append(missingUser, missingClient...); len(missing) > 0 {
		diags.AddError("Unknown permission",
			fmt.Sprintf("API %s has no permission %s. Pocket ID would drop it without an error; add it to the API first; no mutation was attempted.", apiID, strings.Join(apiQuoteAll(missing), ", ")))
		return apiAccessOutcome{}
	}

	if want.grantsClientAccess() {
		isPublic, err := r.client.IsOIDCClientPublic(ctx, clientID)
		switch {
		case client.IsNotFound(err, client.ResourceOIDCClient):
			diags.AddAttributeError(path.Root("client_id"), "OIDC client not found", fmt.Sprintf("OIDC client %s does not exist; no mutation was attempted.", clientID))
			return apiAccessOutcome{}
		case err != nil:
			diags.AddError("Cannot grant API access", fmt.Sprintf("Could not read OIDC client %s to check that it may receive client access: %s; no mutation was attempted.", clientID, err))
			return apiAccessOutcome{}
		case isPublic:
			diags.AddAttributeError(path.Root("client_access"), "Public clients cannot receive client access",
				fmt.Sprintf("OIDC client %s is public, so it cannot use the client credentials grant, and Pocket ID drops client access and client permissions for it without an error. Remove client_access and client_permissions, or make the client confidential; no mutation was attempted.", clientID))
			return apiAccessOutcome{}
		}
	}

	tflog.Debug(ctx, "Writing API access", map[string]any{"api_id": apiID, "client_id": clientID})
	applied, err := r.client.SetAPIClientAccess(ctx, apiID, clientID, client.APIClientGrant{
		UserDelegatedAccess:        want.UserAccess,
		ClientAccess:               want.ClientAccess,
		UserDelegatedPermissionIDs: userIDs,
		ClientPermissionIDs:        clientIDs,
	})
	if err != nil {
		if apiWriteRefused(err) {
			diags.AddError("Error granting API access", fmt.Sprintf("Pocket ID refused the grant of API %s to client %s: %s", apiID, clientID, err))
			return apiAccessOutcome{}
		}
		return r.apiAccessRecoverUncertain(ctx, apiID, clientID, err, diags)
	}

	stored, unnamed := apiAccessFromServer(*applied, api.Permissions)
	if applied.IsEmpty() {
		diags.AddError("API access not granted",
			fmt.Sprintf("Pocket ID stored no grant of API %s to client %s although one was requested.", apiID, clientID))
		return apiAccessOutcome{empty: true}
	}
	if diffs := apiAccessDifferences(want, stored); len(diffs) > 0 || len(unnamed) > 0 {
		if len(unnamed) > 0 {
			diffs = append(diffs, fmt.Sprintf("the server reports permission IDs the API does not list: %s", strings.Join(unnamed, ", ")))
		}
		diags.AddError("Pocket ID stored a different grant",
			fmt.Sprintf("The grant of API %s to client %s differs from the configuration: %s. State shows the grant the server holds.", apiID, clientID, strings.Join(diffs, "; ")))
	}
	return apiAccessOutcome{stored: &stored}
}

// apiAccessRecoverUncertain handles a write that failed without a definite
// rejection: it may have been applied. A read decides what is recorded. When
// the read fails too, nothing about the grant is known and the outcome is
// unresolved.
func (r *apiClientAccessResource) apiAccessRecoverUncertain(ctx context.Context, apiID, clientID string, cause error, diags *diag.Diagnostics) apiAccessOutcome {
	entry, err := r.client.FindClientAPIGrant(ctx, clientID, apiID)
	switch {
	case err != nil:
		diags.AddError("API access result uncertain",
			fmt.Sprintf("Writing the grant of API %s to client %s failed (%s), and it could not be read back (%s). The grant may or may not have been changed, so what the server holds is not known; no access is recorded as confirmed. %s", apiID, clientID, cause, err, apiAccessRecovery(apiID, clientID)))
		return apiAccessOutcome{unresolved: true}
	case entry == nil:
		diags.AddError("Error granting API access",
			fmt.Sprintf("Writing the grant of API %s to client %s failed (%s). A read afterwards found no grant.", apiID, clientID, cause))
		return apiAccessOutcome{empty: true}
	default:
		stored, _ := apiAccessFromServer(entry.APIClientGrant, entry.API.Permissions)
		diags.AddError("API access result uncertain",
			fmt.Sprintf("Writing the grant of API %s to client %s failed (%s), but the client now holds a grant on the API. State shows the grant the server holds.", apiID, clientID, cause))
		return apiAccessOutcome{stored: &stored}
	}
}

func apiQuoteAll(values []string) []string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return quoted
}

func (r *apiClientAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiClientAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	want, diags := apiAccessGrantFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiID, clientID := plan.APIID.ValueString(), plan.ClientID.ValueString()
	if !apiIdentityOK(r.client, &resp.Diagnostics, "configuration", apiID, clientID) || !apiPairOK(r.client, &resp.Diagnostics, apiID, clientID) {
		return
	}
	outcome := r.apiAccessWrite(ctx, apiID, clientID, want, &resp.Diagnostics)
	switch {
	case outcome.stored != nil:
		model := apiAccessModel(apiID, clientID, *outcome.stored)
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	case outcome.unresolved:
		// A grant may exist, and nothing says what it holds. Keep the pair's
		// identity so it can be refreshed, replaced or removed, and leave what
		// it grants unknown (null), not false or empty. Terraform keeps the
		// object, tainted, because the create failed.
		model := apiAccessModel(apiID, clientID, apiAccessGrant{})
		model.UserDelegatedAccess, model.ClientAccess = types.BoolNull(), types.BoolNull()
		model.UserDelegatedPermissions, model.ClientPermissions = types.SetNull(types.StringType), types.SetNull(types.StringType)
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
		apiAccessMarkUnresolved(ctx, resp.Private, &resp.Diagnostics)
	}
}

func (r *apiClientAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiClientAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiID, clientID := state.APIID.ValueString(), state.ClientID.ValueString()
	if !apiIdentityOK(r.client, &resp.Diagnostics, "state", apiID, clientID) || !apiPairOK(r.client, &resp.Diagnostics, apiID, clientID) {
		return
	}
	entry, err := r.client.FindClientAPIGrant(ctx, clientID, apiID)
	if err != nil {
		// A deleted client takes its grants with it.
		if client.IsNotFound(err, client.ResourceOIDCClient) {
			tflog.Debug(ctx, "OIDC client no longer exists, removing its API access from state", map[string]any{"api_id": apiID, "client_id": clientID})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading API access", fmt.Sprintf("Could not read the API grants of client %s: %s", clientID, err))
		return
	}
	if entry == nil {
		// The server's complete list of this client's API grants has no grant
		// on this API (it was revoked, or the API was deleted).
		tflog.Debug(ctx, "Client holds no grant on the API, removing it from state", map[string]any{"api_id": apiID, "client_id": clientID})
		resp.State.RemoveResource(ctx)
		return
	}
	stored, unnamed := apiAccessFromServer(entry.APIClientGrant, entry.API.Permissions)
	if len(unnamed) > 0 {
		resp.Diagnostics.AddWarning("Unnamed permissions in API grant",
			fmt.Sprintf("Client %s is granted permission IDs that API %s does not list (%s); they are shown by ID.", clientID, apiID, strings.Join(unnamed, ", ")))
	}
	model := apiAccessModel(apiID, clientID, stored)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	// The grant was read: whatever an earlier write left unresolved is settled.
	apiAccessClearUnresolved(ctx, resp.Private, &resp.Diagnostics)
}

// ModifyPlan refuses to plan changes to a grant whose last write ended
// without a confirmed result. State then holds the last grant the provider
// confirmed, which may no longer be what the server holds, so a plan made from
// it (for instance with -refresh=false) could hide access that exists or
// overwrite a grant nobody read. A read clears the marker. A plan to destroy
// is allowed: removing the grant never widens access.
func (r *apiClientAccessResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan apiClientAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Configured text that carries the API key is refused at plan time, for
	// the values that are known now (see apiPermissionKeysOK, apiPairOK).
	var keys []string
	for _, set := range []types.Set{plan.UserDelegatedPermissions, plan.ClientPermissions} {
		if set.IsNull() || set.IsUnknown() {
			continue
		}
		for _, element := range set.Elements() {
			if value, ok := element.(types.String); ok && !value.IsNull() && !value.IsUnknown() {
				keys = append(keys, value.ValueString())
			}
		}
	}
	if !apiPermissionKeysOK(r.client, &resp.Diagnostics, keys) {
		return
	}
	if !plan.APIID.IsNull() && !plan.APIID.IsUnknown() && !plan.ClientID.IsNull() && !plan.ClientID.IsUnknown() &&
		!apiPairOK(r.client, &resp.Diagnostics, plan.APIID.ValueString(), plan.ClientID.ValueString()) {
		return
	}

	if req.State.Raw.IsNull() {
		return
	}
	if !apiAccessIsUnresolved(ctx, req.Private, &resp.Diagnostics) {
		return
	}
	var state apiClientAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The identities come from state and are printed only when they pass the
	// check, each and as a pair.
	shownAPI, shownClient := apiShownIDs(r.client, state.APIID.ValueString(), state.ClientID.ValueString())
	apiAccessUnresolvedDiagnostics(&resp.Diagnostics, shownAPI, shownClient)
}

func (r *apiClientAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan apiClientAccessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiID, clientID := plan.APIID.ValueString(), plan.ClientID.ValueString()
	if !apiIdentityOK(r.client, &resp.Diagnostics, "configuration", apiID, clientID) || !apiPairOK(r.client, &resp.Diagnostics, apiID, clientID) {
		return
	}
	// ModifyPlan refuses to plan an unresolved resource; a plan made before the
	// marker was set must not write either.
	if apiAccessIsUnresolved(ctx, req.Private, &resp.Diagnostics) {
		apiAccessUnresolvedDiagnostics(&resp.Diagnostics, apiID, clientID)
		return
	}
	want, diags := apiAccessGrantFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	outcome := r.apiAccessWrite(ctx, apiID, clientID, want, &resp.Diagnostics)
	switch {
	case outcome.stored != nil:
		model := apiAccessModel(apiID, clientID, *outcome.stored)
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	case outcome.empty:
		// The server confirmed that the pair holds no grant.
		model := apiAccessModel(apiID, clientID, apiAccessGrant{})
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	case outcome.unresolved:
		// The write may have changed the grant and nothing says what it holds
		// now. State keeps the last confirmed grant (the framework starts it as
		// the prior state), never an access flag or permission set that no read
		// confirmed, and the marker keeps ordinary plans and writes away until
		// a read succeeds.
		apiAccessMarkUnresolved(ctx, resp.Private, &resp.Diagnostics)
	}
	// Otherwise nothing was sent, or the server refused the write: the prior
	// state still holds.
}

func (r *apiClientAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiClientAccessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiID, clientID := state.APIID.ValueString(), state.ClientID.ValueString()
	if !apiIdentityOK(r.client, &resp.Diagnostics, "state", apiID, clientID) {
		return
	}
	// An unresolved outcome does not stop a delete: removing every grant the
	// pair holds is correct whatever the last write did, and never widens access.
	if err := checkAPISupport(ctx, r.client); err != nil {
		resp.Diagnostics.AddError("Cannot revoke API access", err.Error())
		return
	}
	err := r.client.RemoveAPIClientAccess(ctx, apiID, clientID)
	// Pocket ID deletes grants together with their API or client, so its own
	// not-found error for either confirms the grant is gone.
	if err != nil && !client.IsNotFound(err, client.ResourceAPI) && !client.IsNotFound(err, client.ResourceOIDCClient) {
		resp.Diagnostics.AddError("Error revoking API access", fmt.Sprintf("Could not remove the grant of API %s to client %s: %s", apiID, clientID, err))
	}
}

// ImportState imports a grant as "<api_id>/<client_id>".
func (r *apiClientAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	apiID, clientID, ok := strings.Cut(req.ID, "/")
	// Both halves, and the whole ID, must be valid and free of the API key
	// before anything is stored; the value is never shown.
	if !ok || r.client.ContainsAPIKey(req.ID) || r.client.CheckAPIIdentifier(apiID) != nil || r.client.CheckClientIdentifier(clientID) != nil {
		resp.Diagnostics.AddError("Unexpected Import Identifier",
			"Import API access as <api_id>/<client_id>: an API ID (a UUID), a slash, and an OIDC client ID, none of which may contain the API key this provider authenticates with.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("api_id"), apiID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("client_id"), clientID)...)
}
