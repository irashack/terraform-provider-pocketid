package resources

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

var (
	_ resource.Resource                = &apiResource{}
	_ resource.ResourceWithConfigure   = &apiResource{}
	_ resource.ResourceWithImportState = &apiResource{}
	_ resource.ResourceWithModifyPlan  = &apiResource{}
)

func init() { register(NewAPIResource) }

// NewAPIResource returns the pocketid_api resource.
func NewAPIResource() resource.Resource {
	return &apiResource{}
}

// apiResource manages one Pocket ID API (protected resource), its
// permissions and its CIMD access.
type apiResource struct {
	client *client.Client
}

type apiResourceModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	Resource         types.String `tfsdk:"resource"`
	CreatedAt        types.String `tfsdk:"created_at"`
	AllowCIMDClients types.Bool   `tfsdk:"allow_cimd_clients"`
	Permissions      types.Map    `tfsdk:"permissions"`
}

type apiPermissionModel struct {
	ID                    types.String `tfsdk:"id"`
	Name                  types.String `tfsdk:"name"`
	Description           types.String `tfsdk:"description"`
	AllowedForCIMDClients types.Bool   `tfsdk:"allowed_for_cimd_clients"`
}

var apiPermissionAttrTypes = map[string]attr.Type{
	"id":                       types.StringType,
	"name":                     types.StringType,
	"description":              types.StringType,
	"allowed_for_cimd_clients": types.BoolType,
}

func (r *apiResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api"
}

func (r *apiResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Pocket ID API (a protected resource that clients request access tokens for), its permissions and its CIMD access. Requires Pocket ID 2.14.0 or later.",
		MarkdownDescription: "Manages a Pocket ID API: a protected resource (an OAuth 2.0 resource server) that clients " +
			"request access tokens for by sending its `resource` identifier ([RFC 8707](https://www.rfc-editor.org/rfc/rfc8707)). " +
			"The identifier becomes the token's audience and the granted permissions its scopes. Requires Pocket ID 2.14.0 or later.\n\n" +
			"Which clients may use the API, and with which permissions, is managed with `pocketid_api_client_access`.\n\n" +
			"**Permissions** are a map keyed by the permission key (the scope a client requests). Pocket ID keeps a permission's " +
			"ID as long as its key stays the same, so changing a permission's name or description leaves every client's grant " +
			"of it in place. Removing a key, or renaming it, deletes that permission together with every client's grant of it; " +
			"a client that held no other permission of this API for the same kind of access (user-delegated or client) loses that " +
			"access too, even if it was granted without permissions.\n\n" +
			"**CIMD access** (`allow_cimd_clients`) lets every client registered through a Client ID Metadata Document request " +
			"user-delegated tokens for this API, with the permissions marked `allowed_for_cimd_clients`. It is off unless configured.\n\n" +
			"~> **Deleting or replacing an API removes access to it.** Pocket ID deletes an API's permissions and every client's " +
			"grants on it together with the API. `resource` cannot be changed in place: changing it replaces the API, and the " +
			"replacement has a new ID, so `pocketid_api_client_access` resources that refer to it are replaced as well.\n\n" +
			"**Unsupported values.** The provider never stores or prints the admin API key it authenticates with. A `name`, " +
			"`resource`, or permission key, `name` or `description` that contains that key is therefore refused at plan time, " +
			"although Pocket ID would accept it; choose another value.\n\n" +
			"If creating an API ends without a definite answer from Pocket ID (a timeout or a server error), nothing is recorded " +
			"in state: an API holding the same `resource` afterwards may be someone else's. The error names that API's ID and " +
			"name; import it if it is the intended one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID of the API.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The display name of the API, 1 to 50 characters.",
				Required:    true,
				Validators: []validator.String{
					apiRuneLengthValidator{min: 1, max: client.APINameMaxLength},
				},
			},
			"resource": schema.StringAttribute{
				Description: "The resource identifier clients send to request tokens for this API, which becomes the token audience: " +
					"an absolute URI such as `https://api.example.com`, without whitespace, a fragment or a trailing slash, at most 350 " +
					"characters long. It must be unique and must not be Pocket ID's own issuer URL. Changing it replaces the API.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					apiResourceValidator{},
				},
			},
			"created_at": schema.StringAttribute{
				Description: "When the API was created, as reported by Pocket ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"allow_cimd_clients": schema.BoolAttribute{
				Description: "Whether every client registered through a Client ID Metadata Document (CIMD) may request user-delegated " +
					"tokens for this API, with the permissions that have `allowed_for_cimd_clients` set. Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"permissions": schema.MapNestedAttribute{
				Description: "The API's permissions, keyed by permission key: the scope a client requests, 1 to 128 characters that are " +
					"valid in an OAuth scope (printable ASCII other than space, `\"` and `\\`). The keys `openid`, `profile`, `email`, " +
					"`email_verified`, `groups` and `offline_access` are reserved in any case. This list is authoritative: a permission " +
					"not listed is deleted, with every client's grant of it. Defaults to no permissions.",
				Optional: true,
				Computed: true,
				Default:  mapdefault.StaticValue(types.MapValueMust(types.ObjectType{AttrTypes: apiPermissionAttrTypes}, map[string]attr.Value{})),
				Validators: []validator.Map{
					mapvalidator.KeysAre(apiPermissionKeyValidator{}),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The permission's ID. It stays the same while the key does.",
							Computed:    true,
							// A key new to this API has no ID in state (null); the
							// plain UseStateForUnknown would plan that null, and
							// the ID the server then assigns would contradict the
							// plan. Only a known ID is carried over.
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.UseNonNullStateForUnknown(),
							},
						},
						"name": schema.StringAttribute{
							Description: "The permission's display name, shown on the consent screen, 1 to 50 characters.",
							Required:    true,
							Validators: []validator.String{
								apiRuneLengthValidator{min: 1, max: client.APIPermissionNameMaxLength},
							},
						},
						"description": schema.StringAttribute{
							Description: "A description of the permission, at most 200 characters.",
							Optional:    true,
							Validators: []validator.String{
								apiRuneLengthValidator{min: 0, max: client.APIPermissionDescriptionMaxLength},
							},
						},
						"allowed_for_cimd_clients": schema.BoolAttribute{
							Description: "Whether clients registered through a Client ID Metadata Document may request this permission " +
								"when `allow_cimd_clients` is set. Defaults to `false`.",
							Optional: true,
							Computed: true,
							Default:  booldefault.StaticBool(false),
						},
					},
				},
			},
		},
	}
}

func (r *apiResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// apiDesired is what the configuration asks the server to hold.
type apiDesired struct {
	Name             string
	Resource         string
	AllowCIMDClients bool
	Permissions      map[string]apiDesiredPermission
}

type apiDesiredPermission struct {
	Name                  string
	Description           *string
	AllowedForCIMDClients bool
}

func apiDesiredFromModel(ctx context.Context, m apiResourceModel) (apiDesired, diag.Diagnostics) {
	desired := apiDesired{
		Name:             m.Name.ValueString(),
		Resource:         m.Resource.ValueString(),
		AllowCIMDClients: m.AllowCIMDClients.ValueBool(),
		Permissions:      map[string]apiDesiredPermission{},
	}
	var permissions map[string]apiPermissionModel
	diags := m.Permissions.ElementsAs(ctx, &permissions, false)
	for key, p := range permissions {
		desired.Permissions[key] = apiDesiredPermission{
			Name:                  p.Name.ValueString(),
			Description:           p.Description.ValueStringPointer(),
			AllowedForCIMDClients: p.AllowedForCIMDClients.ValueBool(),
		}
	}
	return desired, diags
}

// permissionInputs is the body of the permission PUT, sorted by key.
func (d apiDesired) permissionInputs() []client.APIPermissionInput {
	inputs := make([]client.APIPermissionInput, 0, len(d.Permissions))
	for _, key := range apiSortedKeys(d.Permissions) {
		p := d.Permissions[key]
		inputs = append(inputs, client.APIPermissionInput{Key: key, Name: p.Name, Description: p.Description})
	}
	return inputs
}

func apiSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// apiPermissionsNeedUpdate reports whether the server's permissions differ
// from the desired ones in key, name or description.
func apiPermissionsNeedUpdate(d apiDesired, api *client.API) bool {
	if len(api.Permissions) != len(d.Permissions) {
		return true
	}
	for _, p := range api.Permissions {
		want, ok := d.Permissions[p.Key]
		if !ok || want.Name != p.Name || !apiEqualStringPointers(want.Description, p.Description) {
			return true
		}
	}
	return false
}

// apiCIMDNeedsUpdate reports whether the server's CIMD access differs from
// the desired one.
func apiCIMDNeedsUpdate(d apiDesired, api *client.API) bool {
	if api.AllowCIMDClients != d.AllowCIMDClients {
		return true
	}
	for _, p := range api.Permissions {
		if p.AllowedForCIMDClients != d.Permissions[p.Key].AllowedForCIMDClients {
			return true
		}
	}
	return false
}

// apiCIMDSelection returns the IDs of the server's permissions that the
// configuration opens to CIMD clients.
func apiCIMDSelection(d apiDesired, api *client.API) []string {
	ids := []string{}
	for _, p := range api.Permissions {
		if d.Permissions[p.Key].AllowedForCIMDClients {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// apiDifferences lists every way the server's API differs from the desired
// one, so a write the server changed or partly ignored is reported rather
// than recorded as done.
func apiDifferences(d apiDesired, api *client.API) []string {
	var diffs []string
	if api.Name != d.Name {
		diffs = append(diffs, fmt.Sprintf("name is %q, not %q", api.Name, d.Name))
	}
	if api.Resource != d.Resource {
		diffs = append(diffs, fmt.Sprintf("resource is %q, not %q", api.Resource, d.Resource))
	}
	if api.AllowCIMDClients != d.AllowCIMDClients {
		diffs = append(diffs, fmt.Sprintf("allow_cimd_clients is %t, not %t", api.AllowCIMDClients, d.AllowCIMDClients))
	}
	held := map[string]client.APIPermission{}
	for _, p := range api.Permissions {
		held[p.Key] = p
	}
	for _, key := range apiSortedKeys(d.Permissions) {
		want := d.Permissions[key]
		got, ok := held[key]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("permission %q is missing", key))
			continue
		}
		if got.Name != want.Name {
			diffs = append(diffs, fmt.Sprintf("permission %q has name %q, not %q", key, got.Name, want.Name))
		}
		if !apiEqualStringPointers(got.Description, want.Description) {
			diffs = append(diffs, fmt.Sprintf("permission %q has description %s, not %s", key, apiDescribePointer(got.Description), apiDescribePointer(want.Description)))
		}
		if got.AllowedForCIMDClients != want.AllowedForCIMDClients {
			diffs = append(diffs, fmt.Sprintf("permission %q has allowed_for_cimd_clients %t, not %t", key, got.AllowedForCIMDClients, want.AllowedForCIMDClients))
		}
	}
	for _, key := range apiSortedKeys(held) {
		if _, ok := d.Permissions[key]; !ok {
			diffs = append(diffs, fmt.Sprintf("permission %q is present but not configured", key))
		}
	}
	return diffs
}

func apiEqualStringPointers(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func apiDescribePointer(s *string) string {
	if s == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *s)
}

// apiModelFromServer maps an API as Pocket ID reports it onto the resource
// model.
func apiModelFromServer(api *client.API) (apiResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	permissions := map[string]attr.Value{}
	for _, p := range api.Permissions {
		if _, dup := permissions[p.Key]; dup {
			diags.AddError("Unexpected API response", fmt.Sprintf("Pocket ID reported permission key %q twice for API %s.", p.Key, api.ID))
			continue
		}
		description := types.StringNull()
		if p.Description != nil {
			description = types.StringValue(*p.Description)
		}
		object, d := types.ObjectValue(apiPermissionAttrTypes, map[string]attr.Value{
			"id":                       types.StringValue(p.ID),
			"name":                     types.StringValue(p.Name),
			"description":              description,
			"allowed_for_cimd_clients": types.BoolValue(p.AllowedForCIMDClients),
		})
		diags.Append(d...)
		permissions[p.Key] = object
	}
	permissionMap, d := types.MapValue(types.ObjectType{AttrTypes: apiPermissionAttrTypes}, permissions)
	diags.Append(d...)
	return apiResourceModel{
		ID:               types.StringValue(api.ID),
		Name:             types.StringValue(api.Name),
		Resource:         types.StringValue(api.Resource),
		CreatedAt:        types.StringValue(api.CreatedAt),
		AllowCIMDClients: types.BoolValue(api.AllowCIMDClients),
		Permissions:      permissionMap,
	}, diags
}

// apiFindByResource returns the API whose resource identifier is resource,
// or nil when there is none. The identifier is unique on the server.
func (r *apiResource) apiFindByResource(ctx context.Context, resource string) (*client.API, error) {
	apis, err := r.client.ListAPIs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range apis {
		if apis[i].Resource == resource {
			return &apis[i], nil
		}
	}
	return nil, nil
}

// apiWriteRefused reports whether a failed write was definitely refused, so
// that it changed nothing. A result that could not be read means the write may
// have been applied, whatever else the error says, so that is checked first.
// Every write of the API resources classifies its error with it.
func apiWriteRefused(err error) bool {
	return !errors.Is(err, client.ErrResultUnread) && client.IsDefiniteRejection(err)
}

// apiWriteStep runs one write against an existing API and checks that the
// response names that API. A response that does not is treated as an
// unreadable result: the write may have been applied.
func apiWriteStep(id string, write func() (*client.API, error)) (*client.API, error) {
	api, err := write()
	if err != nil {
		return nil, err
	}
	if !client.SameUUID(api.ID, id) {
		return nil, fmt.Errorf("%w: the response did not describe API %s", client.ErrResultUnread, id)
	}
	return api, nil
}

// ModifyPlan refuses configured text that contains the API key at plan time,
// so no apply is attempted with it (see apiCredentialTextDiag). Values that are
// not known yet are checked when the resource is created or updated.
func (r *apiResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan apiResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client.ContainsAPIKey(apiModelTexts(ctx, plan)...) {
		apiCredentialTextDiag(&resp.Diagnostics)
	}
}

func (r *apiResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	desired, diags := apiDesiredFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Before anything is sent or printed: Pocket ID would accept text that
	// carries the API key, and the provider would then refuse its answer.
	if r.client.ContainsAPIKey(apiModelTexts(ctx, plan)...) {
		apiCredentialTextDiag(&resp.Diagnostics)
		return
	}

	if err := checkAPISupport(ctx, r.client); err != nil {
		resp.Diagnostics.AddError("Cannot create API", err.Error())
		return
	}

	// The resource identifier is unique. An API that already holds it is
	// refused here, before anything is written, with a pointer to import.
	existing, err := r.apiFindByResource(ctx, desired.Resource)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create API", fmt.Sprintf("Could not check whether an API with resource %q already exists: %s; no mutation was attempted.", desired.Resource, err))
		return
	}
	if existing != nil {
		resp.Diagnostics.AddAttributeError(path.Root("resource"), "API already exists", fmt.Sprintf("An API with resource %q already exists (ID %s). Import it instead; no mutation was attempted.", desired.Resource, existing.ID))
		return
	}

	tflog.Debug(ctx, "Creating API", map[string]any{"resource": desired.Resource})
	created, err := r.client.CreateAPI(ctx, &client.APICreateRequest{Name: desired.Name, Resource: desired.Resource})
	if err != nil {
		if apiWriteRefused(err) {
			resp.Diagnostics.AddError("Error creating API", "Pocket ID refused to create the API: "+err.Error())
			return
		}
		r.apiReportUncertainCreate(ctx, desired.Resource, err, resp)
		return
	}

	api := created
	if len(desired.Permissions) > 0 {
		api, err = apiWriteStep(created.ID, func() (*client.API, error) {
			return r.client.UpdateAPIPermissions(ctx, created.ID, desired.permissionInputs())
		})
		if err != nil {
			r.apiFailedCreateStep(ctx, created, "setting its permissions", err, resp)
			return
		}
	}
	if apiCIMDNeedsUpdate(desired, api) {
		current := api
		api, err = apiWriteStep(created.ID, func() (*client.API, error) {
			return r.client.UpdateAPICIMDAccess(ctx, created.ID, desired.AllowCIMDClients, apiCIMDSelection(desired, current))
		})
		if err != nil {
			r.apiFailedCreateStep(ctx, current, "setting its CIMD access", err, resp)
			return
		}
	}

	model, diags := apiModelFromServer(api)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	if diffs := apiDifferences(desired, api); len(diffs) > 0 {
		resp.Diagnostics.AddError("Pocket ID stored a different API",
			fmt.Sprintf("API %s was created, but the server holds something other than the configuration: %s. It is kept in state as the server holds it.", api.ID, strings.Join(diffs, "; ")))
	}
}

// apiReportUncertainCreate handles a create request that failed without a
// definite rejection (a server error, a timeout, an unusable response): the
// API may or may not exist. An API that holds the identifier afterwards
// cannot be told apart from one another actor created after the provider's
// check, and Pocket ID offers nothing that would prove which, so nothing is
// recorded as managed: adopting it would let a later replacement delete
// someone else's API with every grant on it. The error names the API that
// holds the identifier, if any, so the operator can inspect and import it.
func (r *apiResource) apiReportUncertainCreate(ctx context.Context, resourceID string, cause error, resp *resource.CreateResponse) {
	found, err := r.apiFindByResource(ctx, resourceID)
	var seen string
	switch {
	case err != nil:
		seen = fmt.Sprintf("Whether an API with resource %q exists could not be checked (%s).", resourceID, err)
	case found == nil:
		seen = fmt.Sprintf("A read afterwards found no API with resource %q; the create may still complete.", resourceID)
	case client.ValidateUUID("API", found.ID) != nil:
		seen = fmt.Sprintf("An API with resource %q exists, but Pocket ID reported no usable ID for it.", resourceID)
	default:
		seen = fmt.Sprintf("An API with resource %q exists now (ID %s, name %q). It may be the one this request created, or one someone else created after the provider checked, so it is not recorded as managed by this configuration.", resourceID, found.ID, found.Name)
	}
	resp.Diagnostics.AddError("API creation result uncertain",
		fmt.Sprintf("The create request failed without a definite answer (%s). %s Nothing was recorded in state and nothing was cleaned up. "+
			"Inspect the API in Pocket ID: if it is the intended one, import it into this resource (terraform import, or an import block) "+
			"before applying again; otherwise choose another resource identifier.", cause, seen))
}

// apiFailedCreateStep records a created API whose permissions or CIMD access
// could not be set. Nothing is rolled back: the API stays in state as the
// server holds it (re-read when possible), and Terraform replaces it on the
// next apply.
func (r *apiResource) apiFailedCreateStep(ctx context.Context, last *client.API, step string, cause error, resp *resource.CreateResponse) {
	api := last
	readNote := ""
	if read, err := r.client.GetAPI(ctx, last.ID); err == nil && client.SameUUID(read.ID, last.ID) {
		api = read
	} else if err != nil {
		readNote = " Its current settings could not be read back (" + err.Error() + "), so state shows what was last confirmed."
	}
	model, diags := apiModelFromServer(api)
	resp.Diagnostics.Append(diags...)
	if !diags.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	}
	resp.Diagnostics.AddError("API created with errors",
		fmt.Sprintf("API %s was created, but %s failed: %s. It is kept in state and will be replaced on the next apply; nothing was rolled back.%s", last.ID, step, cause, readNote))
}

func (r *apiResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if !apiIdentityOK(r.client, &resp.Diagnostics, "state", id) {
		return
	}
	api, err := r.client.GetAPI(ctx, id)
	if err != nil {
		if client.IsNotFound(err, client.ResourceAPI) {
			tflog.Debug(ctx, "API no longer exists, removing it from state", map[string]any{"id": id})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading API", fmt.Sprintf("Could not read API %s: %s", id, err))
		return
	}
	if !client.SameUUID(api.ID, id) {
		resp.Diagnostics.AddError("Unexpected API response", fmt.Sprintf("Reading API %s returned a different or no API.", id))
		return
	}
	model, diags := apiModelFromServer(api)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *apiResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state apiResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	desired, diags := apiDesiredFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if !apiIdentityOK(r.client, &resp.Diagnostics, "state", id) {
		return
	}
	if r.client.ContainsAPIKey(apiModelTexts(ctx, plan)...) {
		apiCredentialTextDiag(&resp.Diagnostics)
		return
	}

	if err := checkAPISupport(ctx, r.client); err != nil {
		resp.Diagnostics.AddError("Cannot update API", err.Error())
		return
	}

	// Each step is decided against what the server holds now, so that only
	// the requests needed are sent and the CIMD selection uses current IDs.
	api, err := r.client.GetAPI(ctx, id)
	if err != nil {
		if client.IsNotFound(err, client.ResourceAPI) {
			resp.Diagnostics.AddError("API no longer exists", fmt.Sprintf("API %s was deleted outside Terraform. Refresh to plan its re-creation; no change was made.", id))
			return
		}
		resp.Diagnostics.AddError("Cannot update API", fmt.Sprintf("Could not read API %s before updating it: %s. No change was made.", id, err))
		return
	}
	if !client.SameUUID(api.ID, id) {
		resp.Diagnostics.AddError("Cannot update API", fmt.Sprintf("Reading API %s before updating it returned a different or no API. No change was made.", id))
		return
	}
	// The writes address the API the read described, in the server's own
	// spelling of its ID.
	id = api.ID

	if api.Name != desired.Name {
		api, err = apiWriteStep(id, func() (*client.API, error) {
			return r.client.UpdateAPI(ctx, id, &client.APIUpdateRequest{Name: desired.Name})
		})
		if err != nil {
			r.apiFailedUpdateStep(ctx, id, "renaming it", err, resp)
			return
		}
	}
	if apiPermissionsNeedUpdate(desired, api) {
		api, err = apiWriteStep(id, func() (*client.API, error) {
			return r.client.UpdateAPIPermissions(ctx, id, desired.permissionInputs())
		})
		if err != nil {
			r.apiFailedUpdateStep(ctx, id, "replacing its permissions", err, resp)
			return
		}
	}
	if apiCIMDNeedsUpdate(desired, api) {
		current := api
		api, err = apiWriteStep(id, func() (*client.API, error) {
			return r.client.UpdateAPICIMDAccess(ctx, id, desired.AllowCIMDClients, apiCIMDSelection(desired, current))
		})
		if err != nil {
			r.apiFailedUpdateStep(ctx, id, "setting its CIMD access", err, resp)
			return
		}
	}

	model, diags := apiModelFromServer(api)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	if diffs := apiDifferences(desired, api); len(diffs) > 0 {
		resp.Diagnostics.AddError("Pocket ID stored a different API",
			fmt.Sprintf("API %s was updated, but the server holds something other than the configuration: %s. State shows what the server holds.", id, strings.Join(diffs, "; ")))
	}
}

// apiFailedUpdateStep records what the server holds after an update step
// failed, so the steps that succeeded are in state and the next plan shows
// what is left. When the API cannot be read, the prior state is kept.
func (r *apiResource) apiFailedUpdateStep(ctx context.Context, id, step string, cause error, resp *resource.UpdateResponse) {
	detail := fmt.Sprintf("Updating API %s failed while %s: %s.", id, step, cause)
	read, err := r.client.GetAPI(ctx, id)
	if err == nil && client.SameUUID(read.ID, id) {
		model, diags := apiModelFromServer(read)
		resp.Diagnostics.Append(diags...)
		if !diags.HasError() {
			resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
			detail += " State now shows what the server holds; the next plan shows what is left to change."
		}
	} else {
		detail += " The API could not be read back, so the previous state is kept; refresh before applying again."
	}
	resp.Diagnostics.AddError("Error updating API", detail)
}

func (r *apiResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if !apiIdentityOK(r.client, &resp.Diagnostics, "state", id) {
		return
	}
	// Pocket ID had a DELETE for APIs before 2.14.0, but this provider
	// manages APIs only from 2.14.0; an imported API is not deleted on a
	// server that Create and Update refuse, or whose version is unknown.
	if err := checkAPISupport(ctx, r.client); err != nil {
		resp.Diagnostics.AddError("Cannot delete API", err.Error())
		return
	}
	tflog.Debug(ctx, "Deleting API", map[string]any{"id": id})
	if err := r.client.DeleteAPI(ctx, id); err != nil && !client.IsNotFound(err, client.ResourceAPI) {
		resp.Diagnostics.AddError("Error deleting API", fmt.Sprintf("Could not delete API %s: %s", id, err))
	}
}

// ImportState imports an API by its ID.
func (r *apiResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if err := r.client.CheckAPIIdentifier(req.ID); err != nil {
		resp.Diagnostics.AddError("Unexpected Import Identifier", "Import an API by its ID: "+err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
