package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// apiTestNotFound is Pocket ID's structured not-found error for an API.
const apiTestNotFound = `{"error":"API not found","code":"not_found","details":{"resource":"API"},"request_id":"r"}`

// apiTestFailure makes the fake answer one route with status. With
// afterApply the change is made first, as when a response is lost after
// the server committed.
type apiTestFailure struct {
	status     int
	afterApply bool
}

// apiTestPocketID is an in-memory Pocket ID that follows the server's API
// rules (Service.Create, UpdatePermissions and SetCIMDAccess in v2.17.0):
// trailing slashes are trimmed, permission IDs are kept per key, CIMD IDs
// that are not the API's own are ignored.
type apiTestPocketID struct {
	t        *testing.T
	mu       sync.Mutex
	version  string
	apis     map[string]*client.API
	order    []string
	nextID   int
	calls    []string
	failures map[string]apiTestFailure
	// tamper, when set, may change what a route stores (simulating a
	// server that ignores part of a request).
	tamper func(route string, api *client.API)
}

func newAPITestPocketID(t *testing.T) (*apiTestPocketID, *client.Client) {
	f := &apiTestPocketID{t: t, version: "2.17.0", apis: map[string]*client.API{}, failures: map[string]apiTestFailure{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return f, c
}

func (f *apiTestPocketID) newID() string {
	f.nextID++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", f.nextID)
}

func (f *apiTestPocketID) add(api client.API) *client.API {
	if api.ID == "" {
		api.ID = f.newID()
	}
	if api.CreatedAt == "" {
		api.CreatedAt = "2026-01-01T00:00:00Z"
	}
	if api.Permissions == nil {
		api.Permissions = []client.APIPermission{}
	}
	f.apis[api.ID] = &api
	f.order = append(f.order, api.ID)
	return &api
}

func (f *apiTestPocketID) routes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *apiTestPocketID) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	route := r.Method + " " + parts[0]
	var api *client.API
	if parts[0] == "apis" && len(parts) >= 2 {
		route = r.Method + " api"
		if len(parts) == 3 {
			route = r.Method + " " + parts[2]
		}
		api = f.apis[parts[1]]
	}
	if route != "GET version" {
		f.calls = append(f.calls, route)
	}
	failure, failing := f.failures[route]
	if failing && !failure.afterApply {
		w.WriteHeader(failure.status)
		return
	}
	if api == nil && strings.HasPrefix(route, r.Method+" ") && route != "GET version" && route != "GET apis" && route != "POST apis" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, apiTestNotFound)
		return
	}

	var body []byte
	switch route {
	case "GET version":
		_, _ = fmt.Fprintf(w, `{"currentVersion":%q}`, f.version)
		return
	case "GET apis":
		list := []client.API{}
		for _, id := range f.order {
			if a, ok := f.apis[id]; ok {
				list = append(list, *a)
			}
		}
		body, _ = json.Marshal(map[string]any{"data": list, "pagination": map[string]int{"totalPages": 1, "totalItems": len(list), "currentPage": 1, "itemsPerPage": 100}})
	case "POST apis":
		var in client.APICreateRequest
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		api = f.add(client.API{Name: in.Name, Resource: strings.TrimRight(in.Resource, "/")})
		w.WriteHeader(http.StatusCreated)
	case "GET api":
	case "PUT api":
		var in client.APIUpdateRequest
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		api.Name = in.Name
	case "DELETE api":
		delete(f.apis, api.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	case "PUT permissions":
		var in struct {
			Permissions []client.APIPermissionInput `json:"permissions"`
		}
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		require.NotNil(f.t, in.Permissions, "permissions are never sent as null")
		existing := map[string]client.APIPermission{}
		for _, p := range api.Permissions {
			existing[p.Key] = p
		}
		next := []client.APIPermission{}
		for _, p := range in.Permissions {
			kept, ok := existing[p.Key]
			if !ok {
				kept = client.APIPermission{ID: f.newID(), Key: p.Key}
			}
			kept.Name, kept.Description = p.Name, p.Description
			next = append(next, kept)
		}
		api.Permissions = next
	case "PUT cimd-access":
		var in struct {
			Enabled       bool     `json:"enabled"`
			PermissionIDs []string `json:"permissionIds"`
		}
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		for i := range api.Permissions {
			api.Permissions[i].AllowedForCIMDClients = false
			for _, id := range in.PermissionIDs {
				if id == api.Permissions[i].ID {
					api.Permissions[i].AllowedForCIMDClients = true
				}
			}
		}
		api.AllowCIMDClients = in.Enabled
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if api != nil && f.tamper != nil {
		f.tamper(route, api)
	}
	if failing {
		w.WriteHeader(failure.status)
		return
	}
	if body == nil {
		body, _ = json.Marshal(api)
	}
	_, _ = w.Write(body)
}

func apiTestSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	var sr resource.SchemaResponse
	(&apiResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
	require.False(t, sr.Diagnostics.HasError())
	return sr
}

// apiTestPermission builds one entry of the permissions map; id "" is
// unknown, as planned for a new key.
func apiTestPermission(id, name string, description *string, cimd bool) attr.Value {
	idValue := types.StringUnknown()
	if id != "" {
		idValue = types.StringValue(id)
	}
	return types.ObjectValueMust(apiPermissionAttrTypes, map[string]attr.Value{
		"id": idValue, "name": types.StringValue(name), "description": types.StringPointerValue(description),
		"allowed_for_cimd_clients": types.BoolValue(cimd),
	})
}

func apiTestModel(id, name, resourceID string, cimd bool, permissions map[string]attr.Value) apiResourceModel {
	idValue, created := types.StringUnknown(), types.StringUnknown()
	if id != "" {
		idValue, created = types.StringValue(id), types.StringValue("2026-01-01T00:00:00Z")
	}
	if permissions == nil {
		permissions = map[string]attr.Value{}
	}
	return apiResourceModel{
		ID: idValue, Name: types.StringValue(name), Resource: types.StringValue(resourceID), CreatedAt: created,
		AllowCIMDClients: types.BoolValue(cimd),
		Permissions:      types.MapValueMust(types.ObjectType{AttrTypes: apiPermissionAttrTypes}, permissions),
	}
}

func apiTestCreate(t *testing.T, c *client.Client, plan apiResourceModel) (resource.CreateResponse, *apiResourceModel) {
	t.Helper()
	ctx := context.Background()
	sr := apiTestSchema(t)
	p := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, p.Set(ctx, &plan).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: p.Raw.Copy()}}
	resp.State.RemoveResource(ctx)
	(&apiResource{client: c}).Create(ctx, resource.CreateRequest{Plan: p}, &resp)
	if resp.State.Raw.IsNull() {
		return resp, nil
	}
	var state apiResourceModel
	require.False(t, resp.State.Get(ctx, &state).HasError())
	return resp, &state
}

func apiTestUpdate(t *testing.T, c *client.Client, prior, plan apiResourceModel) (resource.UpdateResponse, apiResourceModel) {
	t.Helper()
	ctx := context.Background()
	sr := apiTestSchema(t)
	p := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, p.Set(ctx, &plan).HasError())
	s := tfsdk.State{Schema: sr.Schema}
	require.False(t, s.Set(ctx, &prior).HasError())
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: s.Raw.Copy()}}
	(&apiResource{client: c}).Update(ctx, resource.UpdateRequest{Plan: p, State: s}, &resp)
	var state apiResourceModel
	require.False(t, resp.State.Get(ctx, &state).HasError())
	return resp, state
}

func apiTestPermissions(t *testing.T, m apiResourceModel) map[string]apiPermissionModel {
	t.Helper()
	var out map[string]apiPermissionModel
	require.False(t, m.Permissions.ElementsAs(context.Background(), &out, false).HasError())
	return out
}

func apiTestCreateDiag(resp resource.CreateResponse) string {
	var parts []string
	for _, d := range resp.Diagnostics {
		parts = append(parts, d.Summary()+": "+d.Detail())
	}
	return strings.Join(parts, "\n")
}

func apiTestUpdateDiag(resp resource.UpdateResponse) string {
	var parts []string
	for _, d := range resp.Diagnostics {
		parts = append(parts, d.Summary()+": "+d.Detail())
	}
	return strings.Join(parts, "\n")
}

// Create sends the API, then its permissions, then its CIMD access with the
// IDs the permission write returned, and records what the server holds.
func TestAPIResourceCreate(t *testing.T) {
	f, c := newAPITestPocketID(t)
	description := "Read items"
	resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", true, map[string]attr.Value{
		"inventory.read":  apiTestPermission("", "Read", &description, true),
		"inventory.write": apiTestPermission("", "Write", nil, false),
	}))
	require.False(t, resp.Diagnostics.HasError(), apiTestCreateDiag(resp))
	assert.Equal(t, []string{"GET apis", "POST apis", "PUT permissions", "PUT cimd-access"}, f.routes())
	require.NotNil(t, state)
	assert.Equal(t, "00000000-0000-4000-8000-000000000001", state.ID.ValueString())
	assert.True(t, state.AllowCIMDClients.ValueBool())
	perms := apiTestPermissions(t, *state)
	require.Len(t, perms, 2)
	assert.True(t, perms["inventory.read"].AllowedForCIMDClients.ValueBool())
	assert.False(t, perms["inventory.write"].AllowedForCIMDClients.ValueBool())
	assert.Equal(t, "Read items", perms["inventory.read"].Description.ValueString())
	assert.True(t, perms["inventory.write"].Description.IsNull())
	assert.NotEqual(t, perms["inventory.read"].ID, perms["inventory.write"].ID)
	assert.False(t, perms["inventory.read"].ID.IsUnknown())
}

// An API with no permissions and CIMD access off needs only the create.
func TestAPIResourceCreate_Minimal(t *testing.T) {
	f, c := newAPITestPocketID(t)
	resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "urn:example:inventory", false, nil))
	require.False(t, resp.Diagnostics.HasError(), apiTestCreateDiag(resp))
	assert.Equal(t, []string{"GET apis", "POST apis"}, f.routes())
	require.NotNil(t, state)
	assert.Empty(t, apiTestPermissions(t, *state))
}

// Nothing is written when the server is too old, when its version cannot be
// read, or when an API already holds the resource identifier.
func TestAPIResourceCreate_GuardsBeforeMutation(t *testing.T) {
	for name, setup := range map[string]func(f *apiTestPocketID){
		"old server":      func(f *apiTestPocketID) { f.version = "2.13.0" },
		"version error":   func(f *apiTestPocketID) { f.version = "not-a-version" },
		"existing":        func(f *apiTestPocketID) { f.add(client.API{Name: "Other", Resource: "https://inventory.example"}) },
		"list unreadable": func(f *apiTestPocketID) { f.failures["GET apis"] = apiTestFailure{status: 403} },
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPITestPocketID(t)
			setup(f)
			resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", false, nil))
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, apiTestCreateDiag(resp), "no mutation was attempted")
			assert.Nil(t, state)
			for _, route := range f.routes() {
				assert.True(t, strings.HasPrefix(route, "GET "), "only reads: %v", f.routes())
			}
		})
	}
}

// A create that failed without a definite answer keeps the API's identity
// when a read finds it, and records nothing when it finds none; a definite
// rejection records nothing and reads nothing.
func TestAPIResourceCreate_UncertainResult(t *testing.T) {
	for name, tc := range map[string]struct {
		failure  apiTestFailure
		retained bool
		summary  string
	}{
		"committed then 503": {apiTestFailure{status: 503, afterApply: true}, true, "API creation result uncertain"},
		"503 before commit":  {apiTestFailure{status: 503}, false, "Error creating API"},
		"rejected":           {apiTestFailure{status: 409}, false, "Error creating API"},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPITestPocketID(t)
			f.failures["POST apis"] = tc.failure
			resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", false, map[string]attr.Value{
				"read": apiTestPermission("", "Read", nil, false),
			}))
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, tc.summary, resp.Diagnostics[len(resp.Diagnostics)-1].Summary())
			assert.NotContains(t, f.routes(), "PUT permissions", "no follow-up write after an uncertain create")
			if tc.retained {
				require.NotNil(t, state)
				assert.Equal(t, "00000000-0000-4000-8000-000000000001", state.ID.ValueString())
			} else {
				assert.Nil(t, state)
			}
			if tc.failure.status == 409 {
				assert.Equal(t, []string{"GET apis", "POST apis"}, f.routes())
			}
		})
	}
}

// When a follow-up write fails, the created API stays in state as the
// server holds it; nothing is deleted.
func TestAPIResourceCreate_FailedStepKeepsAPI(t *testing.T) {
	for _, route := range []string{"PUT permissions", "PUT cimd-access"} {
		t.Run(route, func(t *testing.T) {
			f, c := newAPITestPocketID(t)
			f.failures[route] = apiTestFailure{status: 500}
			resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", true, map[string]attr.Value{
				"read": apiTestPermission("", "Read", nil, true),
			}))
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, apiTestCreateDiag(resp), "nothing was rolled back")
			assert.NotContains(t, f.routes(), "DELETE api")
			require.NotNil(t, state)
			assert.Equal(t, "00000000-0000-4000-8000-000000000001", state.ID.ValueString())
			assert.False(t, state.AllowCIMDClients.ValueBool(), "state shows the server, not the plan")
			if route == "PUT cimd-access" {
				assert.Len(t, apiTestPermissions(t, *state), 1, "the permission write that succeeded is recorded")
			}
		})
	}
}

// A server that ignores part of a write is reported, and state shows what it
// holds instead of what was asked for.
func TestAPIResourceCreate_ServerIgnoredPart(t *testing.T) {
	f, c := newAPITestPocketID(t)
	f.tamper = func(route string, api *client.API) {
		if route == "PUT cimd-access" {
			for i := range api.Permissions {
				api.Permissions[i].AllowedForCIMDClients = false
			}
		}
	}
	resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", true, map[string]attr.Value{
		"read": apiTestPermission("", "Read", nil, true),
	}))
	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, apiTestCreateDiag(resp), `permission "read" has allowed_for_cimd_clients false, not true`)
	require.NotNil(t, state)
	assert.False(t, apiTestPermissions(t, *state)["read"].AllowedForCIMDClients.ValueBool())
}

// An update sends only the requests whose part changed, and a permission
// keeps its ID while its key stays.
func TestAPIResourceUpdate_OnlyChangedParts(t *testing.T) {
	f, c := newAPITestPocketID(t)
	_, created := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", false, map[string]attr.Value{
		"read":  apiTestPermission("", "Read", nil, false),
		"write": apiTestPermission("", "Write", nil, false),
	}))
	require.NotNil(t, created)
	before := apiTestPermissions(t, *created)
	f.calls = nil

	description := "Now described"
	plan := apiTestModel(created.ID.ValueString(), "Inventory", "https://inventory.example", false, map[string]attr.Value{
		"read":  apiTestPermission(before["read"].ID.ValueString(), "Read", &description, false),
		"write": apiTestPermission(before["write"].ID.ValueString(), "Write", nil, false),
	})
	resp, state := apiTestUpdate(t, c, *created, plan)
	require.False(t, resp.Diagnostics.HasError(), apiTestUpdateDiag(resp))
	assert.Equal(t, []string{"GET api", "PUT permissions"}, f.routes())
	after := apiTestPermissions(t, state)
	assert.Equal(t, before["read"].ID, after["read"].ID)
	assert.Equal(t, before["write"].ID, after["write"].ID)
	assert.Equal(t, "Now described", after["read"].Description.ValueString())

	f.calls = nil
	plan.Name = types.StringValue("Renamed")
	resp, state = apiTestUpdate(t, c, state, plan)
	require.False(t, resp.Diagnostics.HasError(), apiTestUpdateDiag(resp))
	assert.Equal(t, []string{"GET api", "PUT api"}, f.routes())
	assert.Equal(t, "Renamed", state.Name.ValueString())
}

// A failed update step leaves state as the server holds it: the steps that
// succeeded are recorded, the rest stays for the next plan.
func TestAPIResourceUpdate_FailedStepRecordsServer(t *testing.T) {
	f, c := newAPITestPocketID(t)
	_, created := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", false, nil))
	require.NotNil(t, created)
	f.failures["PUT permissions"] = apiTestFailure{status: 500}
	plan := apiTestModel(created.ID.ValueString(), "Renamed", "https://inventory.example", false, map[string]attr.Value{
		"read": apiTestPermission("", "Read", nil, false),
	})
	resp, state := apiTestUpdate(t, c, *created, plan)
	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, apiTestUpdateDiag(resp), "State now shows what the server holds")
	assert.Equal(t, "Renamed", state.Name.ValueString())
	assert.Empty(t, apiTestPermissions(t, state))
}

// Read drops the API only on Pocket ID's own not-found error for an API.
func TestAPIResourceRead_ConfirmedAbsence(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		body    string
		removed bool
	}{
		"api not found":    {404, apiTestNotFound, true},
		"bare 404":         {404, ``, false},
		"missing endpoint": {404, `{"error":"API endpoint not found"}`, false},
		"client not found": {404, `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`, false},
		"forbidden":        {403, ``, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			c, _ := client.NewClient(s.URL, "synthetic-token", false, 1)
			ctx := context.Background()
			sr := apiTestSchema(t)
			prior := apiTestModel("00000000-0000-4000-8000-000000000001", "Inventory", "https://inventory.example", false, nil)
			state := tfsdk.State{Schema: sr.Schema}
			require.False(t, state.Set(ctx, &prior).HasError())
			resp := resource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: state.Raw.Copy()}}
			(&apiResource{client: c}).Read(ctx, resource.ReadRequest{State: state}, &resp)
			assert.Equal(t, tc.removed, resp.State.Raw.IsNull())
			assert.Equal(t, !tc.removed, resp.Diagnostics.HasError())
		})
	}
}

// Delete succeeds on an API that is already gone only when Pocket ID says so.
func TestAPIResourceDelete_ConfirmedAbsence(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		ok   bool
	}{
		"api not found": {apiTestNotFound, true},
		"bare 404":      {``, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "DELETE", r.Method)
				w.WriteHeader(404)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			c, _ := client.NewClient(s.URL, "synthetic-token", false, 1)
			ctx := context.Background()
			sr := apiTestSchema(t)
			prior := apiTestModel("00000000-0000-4000-8000-000000000001", "Inventory", "https://inventory.example", false, nil)
			state := tfsdk.State{Schema: sr.Schema}
			require.False(t, state.Set(ctx, &prior).HasError())
			resp := resource.DeleteResponse{State: state}
			(&apiResource{client: c}).Delete(ctx, resource.DeleteRequest{State: state}, &resp)
			assert.Equal(t, !tc.ok, resp.Diagnostics.HasError())
		})
	}
}

func TestAPIResourceImport_RefusesNonUUID(t *testing.T) {
	ctx := context.Background()
	sr := apiTestSchema(t)
	resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema}}
	resp.State.RemoveResource(ctx)
	(&apiResource{}).ImportState(ctx, resource.ImportStateRequest{ID: "../users"}, &resp)
	require.True(t, resp.Diagnostics.HasError())
	assert.NotContains(t, resp.Diagnostics[0].Detail(), "../users")
}

func TestAPIDifferences(t *testing.T) {
	description := "d"
	desired := apiDesired{Name: "A", Resource: "urn:a", Permissions: map[string]apiDesiredPermission{
		"read": {Name: "Read", Description: &description}, "write": {Name: "Write"},
	}}
	api := &client.API{Name: "A", Resource: "urn:a", Permissions: []client.APIPermission{
		{Key: "read", Name: "Read"}, {Key: "admin", Name: "Admin"},
	}}
	diffs := apiDifferences(desired, api)
	sort.Strings(diffs)
	assert.Equal(t, []string{
		`permission "admin" is present but not configured`,
		`permission "read" has description null, not "d"`,
		`permission "write" is missing`,
	}, diffs)
	assert.True(t, apiPermissionsNeedUpdate(desired, api))
	assert.Empty(t, apiDifferences(apiDesired{Name: "A", Resource: "urn:a", Permissions: map[string]apiDesiredPermission{}}, &client.API{Name: "A", Resource: "urn:a"}))
}
