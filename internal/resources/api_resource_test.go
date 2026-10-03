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
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// apiTestNotFound is Pocket ID's structured not-found error for an API.
const apiTestNotFound = `{"error":"API not found","code":"not_found","details":{"resource":"API"},"request_id":"r"}`

// apiTestFailure makes the fake answer one route with status. With
// afterApply the change is made first, as when a response is lost after
// the server committed. With hangUp (the grant fake only) the connection is
// closed without any response instead, a transport failure.
type apiTestFailure struct {
	status     int
	afterApply bool
	hangUp     bool
}

// apiTestPocketID is an in-memory Pocket ID that follows the server's API
// rules (Service.Create, UpdatePermissions and SetCIMDAccess in v2.17.0):
// trailing slashes are trimmed, permission IDs are kept per key, CIMD IDs
// that are not the API's own are ignored.
type apiTestPocketID struct {
	t       *testing.T
	mu      sync.Mutex
	version string
	apis    map[string]*client.API
	order   []string
	nextID  int
	calls   []string
	// responses records "route status" for every answer sent.
	responses []string
	failures  map[string]apiTestFailure
	// tamper, when set, may change what a route stores (simulating a
	// server that ignores part of a request).
	tamper func(route string, api *client.API)
	// beforeList, when set, runs before each list is answered.
	beforeList func()
	// requests counts every request received, the version check included.
	requests int
}

func newAPITestPocketID(t *testing.T) (*apiTestPocketID, *client.Client) {
	return newAPITestPocketIDWithKey(t, "synthetic-token")
}

// newAPITestPocketIDWithKey serves the fake to a client that authenticates
// with key. A UUID-shaped key models Pocket ID's static API keys, which can
// pass the identifier check.
func newAPITestPocketIDWithKey(t *testing.T, key string) (*apiTestPocketID, *client.Client) {
	f := &apiTestPocketID{t: t, version: "2.17.0", apis: map[string]*client.API{}, failures: map[string]apiTestFailure{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, key, false, 5)
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

// apiTestRecorder wraps a response writer and keeps the status the HTTP
// server really sends: the first header written, or 200 for a body written
// without one. Later WriteHeader calls are ignored by net/http, and so here.
type apiTestRecorder struct {
	http.ResponseWriter
	status int
}

func (r *apiTestRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *apiTestRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(body)
}

// serve answers one request. The status is decided once, after the
// request has been applied and any configured failure looked up, and the
// status the server really sent is recorded in responses ("route status").
func (f *apiTestPocketID) serve(rw http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	recorder := &apiTestRecorder{ResponseWriter: rw}
	var w http.ResponseWriter = recorder
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
		defer func() { f.responses = append(f.responses, fmt.Sprintf("%s %d", route, recorder.status)) }()
	}
	status, body := f.apply(route, api, r)
	if failure, failing := f.failures[route]; failing {
		status, body = failure.status, nil
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// apply carries out a request, unless a configured failure says it fails
// before the server commits, and returns the status and body a server that
// did not fail would send.
func (f *apiTestPocketID) apply(route string, api *client.API, r *http.Request) (int, []byte) {
	if failure, failing := f.failures[route]; failing && !failure.afterApply {
		return failure.status, nil
	}
	if api == nil && route != "GET version" && route != "GET apis" && route != "POST apis" {
		return http.StatusNotFound, []byte(apiTestNotFound)
	}
	status := http.StatusOK
	var body []byte
	switch route {
	case "GET version":
		return status, []byte(fmt.Sprintf(`{"currentVersion":%q}`, f.version))
	case "GET apis":
		if f.beforeList != nil {
			f.beforeList()
		}
		list := []client.API{}
		for _, id := range f.order {
			if a, ok := f.apis[id]; ok {
				list = append(list, *a)
			}
		}
		body, _ = json.Marshal(map[string]any{"data": list, "pagination": map[string]int{"totalPages": 1, "totalItems": len(list), "currentPage": 1, "itemsPerPage": 100}})
		return status, body
	case "POST apis":
		var in client.APICreateRequest
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		api = f.add(client.API{Name: in.Name, Resource: strings.TrimRight(in.Resource, "/")})
		status = http.StatusCreated
	case "GET api":
	case "PUT api":
		var in client.APIUpdateRequest
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		api.Name = in.Name
	case "DELETE api":
		delete(f.apis, api.ID)
		return http.StatusNoContent, nil
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
		return http.StatusBadRequest, nil
	}
	if f.tamper != nil {
		f.tamper(route, api)
	}
	body, _ = json.Marshal(api)
	return status, body
}

// received is the number of requests the server got, of any kind.
func (f *apiTestPocketID) received() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// stored is the number of APIs the server holds.
func (f *apiTestPocketID) stored() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.apis)
}

func (f *apiTestPocketID) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.responses...)
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

// A create that failed without a definite answer records nothing, even when
// an API now holds the identifier: it may be someone else's. The error names
// that API (ID and name) so the operator can import it. A definite rejection
// records nothing and reads nothing.
func TestAPIResourceCreate_UncertainResult(t *testing.T) {
	for name, tc := range map[string]struct {
		failure   apiTestFailure
		committed bool // whether the fake server created the API
		summary   string
		detail    []string
	}{
		"committed then 503": {apiTestFailure{status: 503, afterApply: true}, true, "API creation result uncertain",
			[]string{"(ID 00000000-0000-4000-8000-000000000001, name \"Inventory\")", "not recorded as managed", "import it"}},
		"503 before commit": {apiTestFailure{status: 503}, false, "API creation result uncertain",
			[]string{"found no API", "Nothing was recorded"}},
		"rejected": {apiTestFailure{status: 409}, false, "Error creating API", nil},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPITestPocketID(t)
			f.failures["POST apis"] = tc.failure
			resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", false, map[string]attr.Value{
				"read": apiTestPermission("", "Read", nil, false),
			}))
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, tc.summary, resp.Diagnostics[len(resp.Diagnostics)-1].Summary())
			for _, want := range tc.detail {
				assert.Contains(t, apiTestCreateDiag(resp), want)
			}
			// The status the server really sent for the POST (recorded from the
			// first header it wrote) is the injected one, and for the post-commit
			// failure the API exists: a commit followed by a lost 5xx, not an
			// empty success.
			assert.Contains(t, f.sent(), fmt.Sprintf("POST apis %d", tc.failure.status), "the injected status is what the provider received")
			assert.NotContains(t, f.sent(), "POST apis 201")
			assert.Equal(t, tc.committed, f.stored() == 1, "whether the create committed")
			assert.Nil(t, state, "nothing is recorded as owned")
			assert.NotContains(t, f.routes(), "PUT permissions", "no follow-up write after an uncertain create")
			assert.NotContains(t, f.routes(), "DELETE api", "nothing is cleaned up")
			if tc.failure.status == 409 {
				assert.Equal(t, []string{"GET apis", "POST apis"}, f.routes())
			}
		})
	}
}

// The recorder keeps the status the server really sends, not the one a
// handler meant to send: an early header wins over a later one, as in net/http.
func TestAPITestRecorder_FirstHeaderWins(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := &apiTestRecorder{ResponseWriter: inner}
	rec.WriteHeader(http.StatusCreated)
	rec.WriteHeader(http.StatusServiceUnavailable)
	_, _ = rec.Write([]byte("x"))
	assert.Equal(t, http.StatusCreated, rec.status)
	assert.Equal(t, http.StatusCreated, inner.Code)

	rec = &apiTestRecorder{ResponseWriter: httptest.NewRecorder()}
	_, _ = rec.Write([]byte("x"))
	assert.Equal(t, http.StatusOK, rec.status)
}

// An API someone else created between the provider's check and its failed
// create is reported, never adopted.
func TestAPIResourceCreate_UncertainResultDoesNotAdoptAnother(t *testing.T) {
	f, c := newAPITestPocketID(t)
	f.failures["POST apis"] = apiTestFailure{status: 502}
	lists := 0
	// The first list (the provider's check) finds nothing; another actor's
	// API appears before the recovery read.
	f.beforeList = func() {
		lists++
		if lists == 2 {
			f.add(client.API{Name: "Someone else's", Resource: "https://inventory.example"})
		}
	}
	resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", false, nil))
	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, apiTestCreateDiag(resp), `name "Someone else's"`)
	assert.Nil(t, state)
	assert.Equal(t, []string{"GET apis", "POST apis", "GET apis"}, f.routes())
}

// An API key that looks like a UUID, reflected by the server as the created
// API's ID, is refused, and the recovery read that follows (which lists the
// APIs and could name a matching one) never puts the key into a diagnostic or
// state. The same holds when the list is where the key first appears.
func TestAPIResourceCreate_ReflectedKeyNeverReachesDiagnostics(t *testing.T) {
	const key = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	for name, setup := range map[string]func(f *apiTestPocketID){
		// The POST answers 201 with the key as the new API's ID, and the
		// recovery list holds that API.
		"reflected by the create answer": func(f *apiTestPocketID) {
			f.tamper = func(route string, api *client.API) {
				if route == "POST apis" {
					api.ID = key
				}
			}
		},
		// The POST fails after committing; the recovery list holds an API with
		// the identifier whose ID is the key.
		"first seen in the recovery list": func(f *apiTestPocketID) {
			f.failures["POST apis"] = apiTestFailure{status: 503, afterApply: true}
			f.tamper = func(route string, api *client.API) {
				if route == "POST apis" {
					api.ID = key
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPITestPocketIDWithKey(t, key)
			setup(f)
			resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", false, map[string]attr.Value{
				"read": apiTestPermission("", "Read", nil, false),
			}))
			require.True(t, resp.Diagnostics.HasError())
			assert.NotContains(t, apiTestCreateDiag(resp), key, "the key is never printed")
			assert.Equal(t, "API creation result uncertain", resp.Diagnostics[len(resp.Diagnostics)-1].Summary())
			assert.Nil(t, state, "nothing is recorded as owned")
			assert.Equal(t, []string{"GET apis", "POST apis", "GET apis"}, f.routes(), "the recovery read ran, and nothing was written after it")
		})
	}
}

// A creation time that carries the API key is refused like any other text of
// an API: it never reaches state or a diagnostic, on create, read or update.
func TestAPIResource_ReflectedCreatedAtNeverReachesState(t *testing.T) {
	const key = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	const reflected = "created at " + key
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		f, c := newAPITestPocketIDWithKey(t, key)
		f.tamper = func(route string, api *client.API) { api.CreatedAt = reflected }
		resp, state := apiTestCreate(t, c, apiTestModel("", "Inventory", "https://inventory.example", false, nil))
		require.True(t, resp.Diagnostics.HasError())
		assert.NotContains(t, apiTestCreateDiag(resp), key)
		assert.Nil(t, state)
	})

	t.Run("read", func(t *testing.T) {
		f, c := newAPITestPocketIDWithKey(t, key)
		existing := f.add(client.API{Name: "Inventory", Resource: "https://inventory.example", CreatedAt: reflected})
		sr := apiTestSchema(t)
		prior := apiTestModel(existing.ID, "Inventory", "https://inventory.example", false, nil)
		state := tfsdk.State{Schema: sr.Schema}
		require.False(t, state.Set(ctx, &prior).HasError())
		resp := resource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: state.Raw.Copy()}}
		(&apiResource{client: c}).Read(ctx, resource.ReadRequest{State: state}, &resp)
		require.True(t, resp.Diagnostics.HasError())
		var text []string
		for _, d := range resp.Diagnostics {
			text = append(text, d.Summary(), d.Detail())
		}
		assert.NotContains(t, strings.Join(text, "\n"), key)
		var after apiResourceModel
		require.False(t, resp.State.Get(ctx, &after).HasError())
		assert.NotContains(t, after.CreatedAt.ValueString(), key, "state keeps the prior value")
	})

	t.Run("update", func(t *testing.T) {
		f, c := newAPITestPocketIDWithKey(t, key)
		existing := f.add(client.API{Name: "Inventory", Resource: "https://inventory.example", CreatedAt: reflected})
		prior := apiTestModel(existing.ID, "Inventory", "https://inventory.example", false, nil)
		plan := apiTestModel(existing.ID, "Renamed", "https://inventory.example", false, nil)
		resp, state := apiTestUpdate(t, c, prior, plan)
		require.True(t, resp.Diagnostics.HasError())
		assert.NotContains(t, apiTestUpdateDiag(resp), key)
		assert.NotContains(t, state.CreatedAt.ValueString(), key)
		assert.NotContains(t, f.routes(), "PUT api", "nothing was written")
	})
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
				if r.URL.Path == "/api/version/current" {
					_, _ = fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
					return
				}
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

// Delete is gated like Create and Update: on a server older than 2.14.0, or
// one whose version cannot be read, no DELETE is sent.
func TestAPIResourceDelete_VersionGate(t *testing.T) {
	for name, setup := range map[string]func(f *apiTestPocketID){
		"old server":         func(f *apiTestPocketID) { f.version = "2.13.0" },
		"malformed version":  func(f *apiTestPocketID) { f.version = "not-a-version" },
		"version unreadable": func(f *apiTestPocketID) { f.failures["GET version"] = apiTestFailure{status: 403} },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, c := newAPITestPocketID(t)
			existing := f.add(client.API{Name: "Inventory", Resource: "https://inventory.example"})
			setup(f)
			sr := apiTestSchema(t)
			prior := apiTestModel(existing.ID, "Inventory", "https://inventory.example", false, nil)
			state := tfsdk.State{Schema: sr.Schema}
			require.False(t, state.Set(ctx, &prior).HasError())
			resp := resource.DeleteResponse{State: state}
			(&apiResource{client: c}).Delete(ctx, resource.DeleteRequest{State: state}, &resp)
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, resp.Diagnostics[0].Detail(), "no mutation was attempted")
			assert.Empty(t, f.routes(), "no request other than the version check")
			assert.Contains(t, f.apis, existing.ID)
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

// apiPlanConfig is the configuration of an API named Inventory with the
// given permission keys, as Terraform sends it: computed values null.
func apiPlanConfig(keys ...string) apiResourceModel {
	permissions := map[string]attr.Value{}
	for _, key := range keys {
		permissions[key] = types.ObjectValueMust(apiPermissionAttrTypes, map[string]attr.Value{
			"id": types.StringNull(), "name": types.StringValue("Permission " + key), "description": types.StringNull(),
			"allowed_for_cimd_clients": types.BoolValue(false),
		})
	}
	return apiResourceModel{
		ID: types.StringNull(), Name: types.StringValue("Inventory"), Resource: types.StringValue("https://inventory.example"),
		CreatedAt: types.StringNull(), AllowCIMDClients: types.BoolValue(false),
		Permissions: types.MapValueMust(types.ObjectType{AttrTypes: apiPermissionAttrTypes}, permissions),
	}
}

// apiPlanProposed builds Terraform's proposed new state from a configuration
// and the prior state: computed values the configuration leaves null come
// from the prior state, matched by permission key, and stay null for a key
// the prior state does not have.
func apiPlanProposed(t *testing.T, config apiResourceModel, prior *apiResourceModel) apiResourceModel {
	t.Helper()
	proposed := config
	if prior == nil {
		return proposed
	}
	proposed.ID, proposed.CreatedAt = prior.ID, prior.CreatedAt
	priorPermissions := apiTestPermissions(t, *prior)
	permissions := map[string]attr.Value{}
	for key, p := range apiTestPermissions(t, config) {
		if old, ok := priorPermissions[key]; ok {
			p.ID = old.ID
		}
		permissions[key] = types.ObjectValueMust(apiPermissionAttrTypes, map[string]attr.Value{
			"id": p.ID, "name": p.Name, "description": p.Description, "allowed_for_cimd_clients": p.AllowedForCIMDClients,
		})
	}
	proposed.Permissions = types.MapValueMust(types.ObjectType{AttrTypes: apiPermissionAttrTypes}, permissions)
	return proposed
}

// apiPlanAndApply plans the configuration against the prior state through
// the protocol server, checks the plan, applies it to the fake server, and
// checks that every value the plan knew is what the apply returned (what
// Terraform enforces as "inconsistent result after apply").
func apiPlanAndApply(t *testing.T, h *apiHarness, prior *apiResourceModel, config apiResourceModel) (planned, applied apiResourceModel) {
	t.Helper()
	r := &apiResource{}
	typ := h.types["pocketid_api"]
	proposed := apiPlanProposed(t, config, prior)
	priorValue := apiHarnessValue(t, r, prior)
	configValue := apiHarnessValue(t, r, &config)
	plan := h.plan("pocketid_api", priorValue, configValue, apiHarnessValue(t, r, &proposed), nil)
	require.Empty(t, apiHarnessErrors(plan.Diagnostics))
	planned, _ = apiHarnessDecode[apiResourceModel](t, r, typ, plan.PlannedState)
	result := h.apply("pocketid_api", priorValue, configValue, plan.PlannedState, plan.PlannedPrivate)
	require.Empty(t, apiHarnessErrors(result.Diagnostics))
	applied, ok := apiHarnessDecode[apiResourceModel](t, r, typ, result.NewState)
	require.True(t, ok)
	apiHarnessAssertApplied(t, typ, plan.PlannedState, result.NewState)
	return planned, applied
}

// Permission IDs through the framework's planning: unknown for a key the API
// does not have yet (at creation, when a key is added, and when a removed key
// comes back), and carried over for a kept key. Planning a key new to an
// existing API as null made the ID the server assigned contradict the plan.
func TestAPIResourcePlan_PermissionIDs(t *testing.T) {
	_, c := newAPITestPocketID(t)
	h := newAPIHarness(t, c)

	planned, created := apiPlanAndApply(t, h, nil, apiPlanConfig("read"))
	assert.True(t, apiTestPermissions(t, planned)["read"].ID.IsUnknown(), "a new API's permission ID is unknown")
	readID := apiTestPermissions(t, created)["read"].ID
	require.False(t, readID.IsNull() || readID.IsUnknown())

	planned, added := apiPlanAndApply(t, h, &created, apiPlanConfig("read", "write"))
	assert.True(t, apiTestPermissions(t, planned)["write"].ID.IsUnknown(), "an added key's ID is unknown, not null")
	assert.Equal(t, readID, apiTestPermissions(t, planned)["read"].ID, "a kept key's ID is planned from state")
	assert.Equal(t, readID, apiTestPermissions(t, added)["read"].ID)
	assert.False(t, apiTestPermissions(t, added)["write"].ID.IsNull())

	_, removed := apiPlanAndApply(t, h, &added, apiPlanConfig("write"))
	planned, readded := apiPlanAndApply(t, h, &removed, apiPlanConfig("read", "write"))
	assert.True(t, apiTestPermissions(t, planned)["read"].ID.IsUnknown(), "a re-added key's ID is unknown")
	assert.NotEqual(t, readID, apiTestPermissions(t, readded)["read"].ID, "a re-added key is a new permission")
	assert.Equal(t, apiTestPermissions(t, added)["write"].ID, apiTestPermissions(t, readded)["write"].ID)
}

// An API ID that carries the API key is refused wherever it comes from: the
// import ID, and the ID in state for read, update and delete. The error is
// fixed text, nothing is stored, and no request is sent.
func TestAPIResource_KeyBearingIdentity(t *testing.T) {
	const key = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	ctx := context.Background()
	f, c := newAPITestPocketIDWithKey(t, key)
	sr := apiTestSchema(t)
	text := func(diags diag.Diagnostics) string {
		var parts []string
		for _, d := range diags.Errors() {
			parts = append(parts, d.Summary()+": "+d.Detail())
		}
		return strings.Join(parts, "\n")
	}

	imported := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&apiResource{client: c}).ImportState(ctx, resource.ImportStateRequest{ID: key}, &imported)
	require.True(t, imported.Diagnostics.HasError())
	assert.NotContains(t, text(imported.Diagnostics), key)
	assert.True(t, imported.State.Raw.IsNull(), "nothing is stored")

	prior := apiTestModel(key, "Inventory", "https://inventory.example", false, nil)
	state := tfsdk.State{Schema: sr.Schema}
	require.False(t, state.Set(ctx, &prior).HasError())

	read := resource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: state.Raw.Copy()}}
	(&apiResource{client: c}).Read(ctx, resource.ReadRequest{State: state}, &read)
	require.True(t, read.Diagnostics.HasError())
	assert.NotContains(t, text(read.Diagnostics), key)
	assert.False(t, read.State.Raw.IsNull(), "state is left as it was")

	deleted := resource.DeleteResponse{State: state}
	(&apiResource{client: c}).Delete(ctx, resource.DeleteRequest{State: state}, &deleted)
	require.True(t, deleted.Diagnostics.HasError())
	assert.NotContains(t, text(deleted.Diagnostics), key)

	updated, _ := apiTestUpdate(t, c, prior, apiTestModel(key, "Renamed", "https://inventory.example", false, nil))
	require.True(t, updated.Diagnostics.HasError())
	assert.NotContains(t, apiTestUpdateDiag(updated), key)

	assert.Empty(t, f.routes(), "no request carried the identity")
}

// Configured text that contains the API key is refused with a fixed
// diagnostic before any request, however it is configured: at plan time for
// known values, and at the top of create and update for the rest. Pocket ID
// would accept such text, and the provider would then refuse its answer.
func TestAPIResource_CredentialBearingConfiguration(t *testing.T) {
	const key = "synthetic-api-key-0123456789"
	permission := func(name string, description *string) map[string]attr.Value {
		return map[string]attr.Value{"read": apiTestPermission("", name, description, false)}
	}
	withKey := key
	for name, tc := range map[string]struct {
		model func() apiResourceModel
	}{
		"name": {func() apiResourceModel {
			return apiTestModel("", "Inv "+key, "https://inventory.example", false, nil)
		}},
		"name is the key": {func() apiResourceModel {
			return apiTestModel("", key, "https://inventory.example", false, nil)
		}},
		"resource": {func() apiResourceModel {
			return apiTestModel("", "Inventory", "https://inventory.example/"+key, false, nil)
		}},
		"permission key": {func() apiResourceModel {
			return apiTestModel("", "Inventory", "https://inventory.example", false, map[string]attr.Value{"scope-" + key: apiTestPermission("", "Read", nil, false)})
		}},
		"permission name": {func() apiResourceModel {
			return apiTestModel("", "Inventory", "https://inventory.example", false, permission("Read "+key, nil))
		}},
		"permission description": {func() apiResourceModel {
			d := "about " + withKey
			return apiTestModel("", "Inventory", "https://inventory.example", false, permission("Read", &d))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			// Create sends nothing.
			f, c := newAPITestPocketIDWithKey(t, key)
			resp, state := apiTestCreate(t, c, tc.model())
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, "Value not supported", resp.Diagnostics[0].Summary())
			assert.NotContains(t, apiTestCreateDiag(resp), key)
			assert.Nil(t, state)
			assert.Zero(t, f.received(), "no request of any kind")

			// So does update, against an API that exists.
			f, c = newAPITestPocketIDWithKey(t, key)
			existing := f.add(client.API{Name: "Inventory", Resource: "https://inventory.example"})
			plan := tc.model()
			plan.ID = types.StringValue(existing.ID)
			plan.CreatedAt = types.StringValue("2026-01-01T00:00:00Z")
			plan.Resource = types.StringValue("https://inventory.example")
			if name == "resource" {
				plan.Resource = types.StringValue("https://inventory.example/" + key)
			}
			prior := apiTestModel(existing.ID, "Inventory", "https://inventory.example", false, nil)
			updated, _ := apiTestUpdate(t, c, prior, plan)
			require.True(t, updated.Diagnostics.HasError())
			assert.Equal(t, "Value not supported", updated.Diagnostics[0].Summary())
			assert.NotContains(t, apiTestUpdateDiag(updated), key)
			assert.Zero(t, f.received(), "no request of any kind")

			// And planning refuses it, through the framework's plan.
			f, c = newAPITestPocketIDWithKey(t, key)
			h := newAPIHarness(t, c)
			r := &apiResource{}
			config := tc.model()
			proposed := apiPlanProposed(t, config, nil)
			plan2 := h.plan("pocketid_api", apiHarnessValue(t, r, (*apiResourceModel)(nil)), apiHarnessValue(t, r, &config), apiHarnessValue(t, r, &proposed), nil)
			errs := apiHarnessErrors(plan2.Diagnostics)
			assert.Contains(t, errs, "Value not supported")
			assert.NotContains(t, errs, key)
			assert.Zero(t, f.received())
		})
	}

	// Without the key, and for a destroy, planning is not held back.
	_, c := newAPITestPocketIDWithKey(t, key)
	h := newAPIHarness(t, c)
	r := &apiResource{}
	config := apiPlanConfig("read")
	proposed := apiPlanProposed(t, config, nil)
	plan := h.plan("pocketid_api", apiHarnessValue(t, r, (*apiResourceModel)(nil)), apiHarnessValue(t, r, &config), apiHarnessValue(t, r, &proposed), nil)
	assert.Empty(t, apiHarnessErrors(plan.Diagnostics))
	created := apiTestModel("00000000-0000-4000-8000-000000000001", "Inv "+key, "https://inventory.example", false, nil)
	null := apiHarnessValue(t, r, (*apiResourceModel)(nil))
	destroy := h.plan("pocketid_api", apiHarnessValue(t, r, &created), null, null, nil)
	assert.Empty(t, apiHarnessErrors(destroy.Diagnostics))
}
