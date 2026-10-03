package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	apiAccessTestAPI    = "00000000-0000-4000-8000-0000000000a1"
	apiAccessTestRead   = "00000000-0000-4000-8000-0000000000b1"
	apiAccessTestWrite  = "00000000-0000-4000-8000-0000000000b2"
	apiAccessTestClient = "app"
	// apiAccessTestClientGone is Pocket ID's not-found error for an OIDC client.
	apiAccessTestClientGone = `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"},"request_id":"r"}`
)

// apiAccessTestPocketID is an in-memory Pocket ID holding one API (with
// permissions read and write) and one client, applying the grant rules of
// Service.SetAPIClientAccess in v2.17.0: unknown permission IDs are dropped,
// a public client gets no client access, and a permission turns its access
// on.
type apiAccessTestPocketID struct {
	t        *testing.T
	mu       sync.Mutex
	public   bool
	grant    *client.APIClientGrant
	calls    []string
	failures map[string]apiTestFailure
	// tamper may change what a grant write stores.
	tamper func(g *client.APIClientGrant)
}

func newAPIAccessTestPocketID(t *testing.T) (*apiAccessTestPocketID, *client.Client) {
	f := &apiAccessTestPocketID{t: t, failures: map[string]apiTestFailure{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return f, c
}

func (f *apiAccessTestPocketID) routes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *apiAccessTestPocketID) api() client.API {
	return client.API{ID: apiAccessTestAPI, Name: "Inventory", Resource: "urn:inventory", Permissions: []client.APIPermission{
		{ID: apiAccessTestRead, Key: "read", Name: "Read"}, {ID: apiAccessTestWrite, Key: "write", Name: "Write"},
	}}
}

func (f *apiAccessTestPocketID) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	var route string
	switch r.URL.Path {
	case "/api/version/current":
		_, _ = fmt.Fprint(w, `{"currentVersion":"2.16.0"}`)
		return
	case "/api/apis/" + apiAccessTestAPI:
		route = r.Method + " api"
	case "/api/oidc/clients/" + apiAccessTestClient:
		route = r.Method + " client"
	case "/api/apis/" + apiAccessTestAPI + "/clients/" + apiAccessTestClient:
		route = r.Method + " grant"
	case "/api/api-access/" + apiAccessTestClient + "/apis":
		route = r.Method + " grants"
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.calls = append(f.calls, route)
	failure, failing := f.failures[route]
	if failing && !failure.afterApply {
		w.WriteHeader(failure.status)
		if failure.status == 404 && route == "GET grants" {
			_, _ = fmt.Fprint(w, apiAccessTestClientGone)
		}
		return
	}
	var out any
	switch route {
	case "GET api":
		out = f.api()
	case "GET client":
		out = map[string]any{"id": apiAccessTestClient, "name": "App", "isPublic": f.public}
	case "PUT grant":
		var in client.APIClientGrant
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		require.NotNil(f.t, in.UserDelegatedPermissionIDs, "lists are never sent as null")
		require.NotNil(f.t, in.ClientPermissionIDs, "lists are never sent as null")
		known := []string{apiAccessTestRead, apiAccessTestWrite}
		keep := func(ids []string) []string {
			kept := []string{}
			for _, id := range ids {
				if slices.Contains(known, id) {
					kept = append(kept, id)
				}
			}
			return kept
		}
		g := client.APIClientGrant{UserDelegatedPermissionIDs: keep(in.UserDelegatedPermissionIDs), ClientPermissionIDs: keep(in.ClientPermissionIDs)}
		if f.public {
			in.ClientAccess = false
			g.ClientPermissionIDs = []string{}
		}
		g.UserDelegatedAccess = in.UserDelegatedAccess || len(g.UserDelegatedPermissionIDs) > 0
		g.ClientAccess = in.ClientAccess || len(g.ClientPermissionIDs) > 0
		if f.tamper != nil {
			f.tamper(&g)
		}
		f.grant = &g
		if g.IsEmpty() {
			f.grant = nil
		}
		out = g
	case "DELETE grant":
		f.grant = nil
		if !failing {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	case "GET grants":
		list := []client.ClientAPIGrant{}
		if f.grant != nil {
			list = append(list, client.ClientAPIGrant{API: f.api(), APIClientGrant: *f.grant})
		}
		out = list
	}
	if failing {
		w.WriteHeader(failure.status)
		return
	}
	body, _ := json.Marshal(out)
	_, _ = w.Write(body)
}

func apiAccessTestSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	var sr resource.SchemaResponse
	(&apiClientAccessResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
	require.False(t, sr.Diagnostics.HasError())
	return sr
}

func apiAccessTestSet(keys ...string) types.Set {
	values := []attr.Value{}
	for _, key := range keys {
		values = append(values, types.StringValue(key))
	}
	return types.SetValueMust(types.StringType, values)
}

func apiAccessTestPlan(userAccess bool, userKeys []string, clientAccess bool, clientKeys []string) apiClientAccessModel {
	return apiClientAccessModel{
		ID: types.StringUnknown(), APIID: types.StringValue(apiAccessTestAPI), ClientID: types.StringValue(apiAccessTestClient),
		UserDelegatedAccess: types.BoolValue(userAccess), UserDelegatedPermissions: apiAccessTestSet(userKeys...),
		ClientAccess: types.BoolValue(clientAccess), ClientPermissions: apiAccessTestSet(clientKeys...),
	}
}

func apiAccessTestCreate(t *testing.T, c *client.Client, plan apiClientAccessModel) (string, *apiClientAccessModel) {
	t.Helper()
	ctx := context.Background()
	sr := apiAccessTestSchema(t)
	p := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, p.Set(ctx, &plan).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&apiClientAccessResource{client: c}).Create(ctx, resource.CreateRequest{Plan: p}, &resp)
	text := apiAccessTestText(resp.Diagnostics.Errors())
	if resp.State.Raw.IsNull() {
		return text, nil
	}
	var state apiClientAccessModel
	require.False(t, resp.State.Get(ctx, &state).HasError())
	return text, &state
}

func apiAccessTestText[D interface {
	Summary() string
	Detail() string
}](diags []D) string {
	var parts []string
	for _, d := range diags {
		parts = append(parts, d.Summary()+": "+d.Detail())
	}
	return strings.Join(parts, "\n")
}

func apiAccessTestKeys(t *testing.T, s types.Set) []string {
	t.Helper()
	var keys []string
	require.False(t, s.ElementsAs(context.Background(), &keys, false).HasError())
	slices.Sort(keys)
	return keys
}

// Keys are resolved to the API's permission IDs, written with the per-pair
// PUT, and recorded as the server stored them.
func TestAPIClientAccessCreate(t *testing.T) {
	f, c := newAPIAccessTestPocketID(t)
	errs, state := apiAccessTestCreate(t, c, apiAccessTestPlan(true, []string{"read"}, true, []string{"write"}))
	require.Empty(t, errs)
	require.NotNil(t, state)
	assert.Equal(t, []string{"GET api", "GET client", "PUT grant"}, f.routes())
	assert.Equal(t, apiAccessTestAPI+"/"+apiAccessTestClient, state.ID.ValueString())
	assert.Equal(t, []string{"read"}, apiAccessTestKeys(t, state.UserDelegatedPermissions))
	assert.Equal(t, []string{"write"}, apiAccessTestKeys(t, state.ClientPermissions))
	assert.Equal(t, []string{apiAccessTestRead}, f.grant.UserDelegatedPermissionIDs)
	assert.Equal(t, []string{apiAccessTestWrite}, f.grant.ClientPermissionIDs)
}

// A user-delegated grant needs no look at the client: public clients may
// have it.
func TestAPIClientAccessCreate_PublicClientUserDelegated(t *testing.T) {
	f, c := newAPIAccessTestPocketID(t)
	f.public = true
	errs, state := apiAccessTestCreate(t, c, apiAccessTestPlan(true, nil, false, nil))
	require.Empty(t, errs)
	require.NotNil(t, state)
	assert.Equal(t, []string{"GET api", "PUT grant"}, f.routes())
	assert.True(t, state.UserDelegatedAccess.ValueBool())
}

// Requests Pocket ID would silently narrow are refused before the write.
func TestAPIClientAccessCreate_RefusedBeforeWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*apiAccessTestPocketID)
		plan  apiClientAccessModel
		want  string
	}{
		"unknown key":          {nil, apiAccessTestPlan(true, []string{"read", "delete"}, false, nil), `API ` + apiAccessTestAPI + ` has no permission "delete"`},
		"public client access": {func(f *apiAccessTestPocketID) { f.public = true }, apiAccessTestPlan(false, nil, true, nil), "is public"},
		"public client perms":  {func(f *apiAccessTestPocketID) { f.public = true }, apiAccessTestPlan(true, []string{"read"}, true, []string{"write"}), "is public"},
		"api gone":             {func(f *apiAccessTestPocketID) { f.failures["GET api"] = apiTestFailure{status: 404} }, apiAccessTestPlan(true, nil, false, nil), "Could not read API"},
		"client unreadable":    {func(f *apiAccessTestPocketID) { f.failures["GET client"] = apiTestFailure{status: 403} }, apiAccessTestPlan(false, nil, true, nil), "Could not read OIDC client"},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPIAccessTestPocketID(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			errs, state := apiAccessTestCreate(t, c, tc.plan)
			assert.Contains(t, errs, tc.want)
			assert.Contains(t, errs, "no mutation was attempted")
			assert.Nil(t, state)
			assert.NotContains(t, f.routes(), "PUT grant")
		})
	}
}

// When the server stores less than was asked for (here a permission removed
// between the check and the write), the apply fails naming what was dropped
// and state records only what the server confirmed.
func TestAPIClientAccessCreate_ServerDroppedPermission(t *testing.T) {
	f, c := newAPIAccessTestPocketID(t)
	f.tamper = func(g *client.APIClientGrant) {
		g.UserDelegatedPermissionIDs = slices.DeleteFunc(g.UserDelegatedPermissionIDs, func(id string) bool { return id == apiAccessTestWrite })
	}
	errs, state := apiAccessTestCreate(t, c, apiAccessTestPlan(true, []string{"read", "write"}, false, nil))
	assert.Contains(t, errs, `user-delegated permission "write" was not granted`)
	require.NotNil(t, state)
	assert.Equal(t, []string{"read"}, apiAccessTestKeys(t, state.UserDelegatedPermissions))
}

// A write the server stored as nothing records nothing on create.
func TestAPIClientAccessCreate_ServerStoredNothing(t *testing.T) {
	f, c := newAPIAccessTestPocketID(t)
	f.tamper = func(g *client.APIClientGrant) {
		*g = client.APIClientGrant{UserDelegatedPermissionIDs: []string{}, ClientPermissionIDs: []string{}}
	}
	errs, state := apiAccessTestCreate(t, c, apiAccessTestPlan(true, nil, false, nil))
	assert.Contains(t, errs, "stored no grant")
	assert.Nil(t, state)
}

// A write that failed without a definite answer is settled by a read: what
// the server holds is recorded, nothing when it holds nothing, and only the
// pair, with no access, when the read fails too. A refused write records
// nothing and reads nothing.
func TestAPIClientAccessCreate_UncertainWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		failures map[string]apiTestFailure
		state    string // "", "granted" or "empty"
		summary  string
	}{
		"applied then 503": {map[string]apiTestFailure{"PUT grant": {status: 503, afterApply: true}}, "granted", "API access result uncertain"},
		"503 not applied":  {map[string]apiTestFailure{"PUT grant": {status: 503}}, "", "Error granting API access"},
		"read back failed": {map[string]apiTestFailure{"PUT grant": {status: 503, afterApply: true}, "GET grants": {status: 403}}, "empty", "API access result uncertain"},
		"refused":          {map[string]apiTestFailure{"PUT grant": {status: 400}}, "", "Error granting API access"},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPIAccessTestPocketID(t)
			f.failures = tc.failures
			errs, state := apiAccessTestCreate(t, c, apiAccessTestPlan(true, []string{"read"}, false, nil))
			assert.Contains(t, errs, tc.summary)
			switch tc.state {
			case "":
				assert.Nil(t, state)
			case "granted":
				require.NotNil(t, state)
				assert.Equal(t, []string{"read"}, apiAccessTestKeys(t, state.UserDelegatedPermissions))
			case "empty":
				require.NotNil(t, state)
				assert.False(t, state.UserDelegatedAccess.ValueBool())
				assert.Empty(t, apiAccessTestKeys(t, state.UserDelegatedPermissions))
			}
			if name == "refused" {
				assert.NotContains(t, f.routes(), "GET grants")
			}
		})
	}
}

// After an update whose result is not confirmed, state claims no access
// rather than the grant that was there before.
func TestAPIClientAccessUpdate_UnconfirmedRecordsNoAccess(t *testing.T) {
	ctx := context.Background()
	f, c := newAPIAccessTestPocketID(t)
	_, prior := apiAccessTestCreate(t, c, apiAccessTestPlan(true, []string{"read"}, true, []string{"write"}))
	require.NotNil(t, prior)
	f.failures["PUT grant"] = apiTestFailure{status: 503}
	f.failures["GET grants"] = apiTestFailure{status: 403}

	sr := apiAccessTestSchema(t)
	plan := apiAccessTestPlan(true, []string{"read", "write"}, false, nil)
	plan.ID = prior.ID
	p := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, p.Set(ctx, &plan).HasError())
	s := tfsdk.State{Schema: sr.Schema}
	require.False(t, s.Set(ctx, prior).HasError())
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: s.Raw.Copy()}}
	(&apiClientAccessResource{client: c}).Update(ctx, resource.UpdateRequest{Plan: p, State: s}, &resp)
	require.True(t, resp.Diagnostics.HasError())
	var state apiClientAccessModel
	require.False(t, resp.State.Get(ctx, &state).HasError())
	assert.False(t, state.UserDelegatedAccess.ValueBool())
	assert.False(t, state.ClientAccess.ValueBool())
	assert.Empty(t, apiAccessTestKeys(t, state.ClientPermissions))

	// A refused update keeps the prior state, which is still true.
	f.failures = map[string]apiTestFailure{"PUT grant": {status: 400}}
	resp = resource.UpdateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: s.Raw.Copy()}}
	(&apiClientAccessResource{client: c}).Update(ctx, resource.UpdateRequest{Plan: p, State: s}, &resp)
	require.True(t, resp.Diagnostics.HasError())
	require.False(t, resp.State.Get(ctx, &state).HasError())
	assert.Equal(t, []string{"write"}, apiAccessTestKeys(t, state.ClientPermissions))
}

// Read names permission IDs by key and drops the pair only when the
// server's list confirms it holds no grant, or the client is gone.
func TestAPIClientAccessRead(t *testing.T) {
	for name, tc := range map[string]struct {
		grant   *client.APIClientGrant
		failure *apiTestFailure
		removed bool
		failed  bool
	}{
		"granted":     {grant: &client.APIClientGrant{UserDelegatedAccess: true, UserDelegatedPermissionIDs: []string{apiAccessTestWrite, apiAccessTestRead}, ClientPermissionIDs: []string{}}},
		"no grant":    {removed: true},
		"client gone": {failure: &apiTestFailure{status: 404}, removed: true},
		"forbidden":   {failure: &apiTestFailure{status: 403}, failed: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, c := newAPIAccessTestPocketID(t)
			f.grant = tc.grant
			if tc.failure != nil {
				f.failures["GET grants"] = *tc.failure
			}
			sr := apiAccessTestSchema(t)
			prior := apiAccessModel(apiAccessTestAPI, apiAccessTestClient, apiAccessGrant{UserAccess: true})
			s := tfsdk.State{Schema: sr.Schema}
			require.False(t, s.Set(ctx, &prior).HasError())
			resp := resource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: s.Raw.Copy()}}
			(&apiClientAccessResource{client: c}).Read(ctx, resource.ReadRequest{State: s}, &resp)
			assert.Equal(t, tc.failed, resp.Diagnostics.HasError())
			assert.Equal(t, tc.removed, resp.State.Raw.IsNull())
			if tc.grant != nil {
				var state apiClientAccessModel
				require.False(t, resp.State.Get(ctx, &state).HasError())
				assert.Equal(t, []string{"read", "write"}, apiAccessTestKeys(t, state.UserDelegatedPermissions))
			}
		})
	}
}

// A bare 404 from the grant list is not proof the client is gone.
func TestAPIClientAccessRead_BareNotFoundIsAnError(t *testing.T) {
	ctx := context.Background()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer s.Close()
	c, _ := client.NewClient(s.URL, "synthetic-token", false, 1)
	sr := apiAccessTestSchema(t)
	prior := apiAccessModel(apiAccessTestAPI, apiAccessTestClient, apiAccessGrant{UserAccess: true})
	st := tfsdk.State{Schema: sr.Schema}
	require.False(t, st.Set(ctx, &prior).HasError())
	resp := resource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: st.Raw.Copy()}}
	(&apiClientAccessResource{client: c}).Read(ctx, resource.ReadRequest{State: st}, &resp)
	assert.True(t, resp.Diagnostics.HasError())
	assert.False(t, resp.State.Raw.IsNull())
}

// Delete succeeds when Pocket ID confirms the API or the client is gone, and
// fails on any other 404.
func TestAPIClientAccessDelete(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		ok     bool
	}{
		"deleted":     {204, ``, true},
		"api gone":    {404, apiTestNotFound, true},
		"client gone": {404, apiAccessTestClientGone, true},
		"bare 404":    {404, ``, false},
		"route 404":   {404, `{"error":"API endpoint not found"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "DELETE /api/apis/"+apiAccessTestAPI+"/clients/"+apiAccessTestClient, r.Method+" "+r.URL.Path)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			c, _ := client.NewClient(s.URL, "synthetic-token", false, 1)
			sr := apiAccessTestSchema(t)
			prior := apiAccessModel(apiAccessTestAPI, apiAccessTestClient, apiAccessGrant{UserAccess: true})
			st := tfsdk.State{Schema: sr.Schema}
			require.False(t, st.Set(ctx, &prior).HasError())
			resp := resource.DeleteResponse{State: st}
			(&apiClientAccessResource{client: c}).Delete(ctx, resource.DeleteRequest{State: st}, &resp)
			assert.Equal(t, !tc.ok, resp.Diagnostics.HasError())
		})
	}
}

func TestAPIClientAccessValidateConfig(t *testing.T) {
	ctx := context.Background()
	sr := apiAccessTestSchema(t)
	null := types.SetNull(types.StringType)
	for name, tc := range map[string]struct {
		userAccess, clientAccess types.Bool
		userKeys, clientKeys     types.Set
		want                     string
	}{
		"user access only":        {types.BoolValue(true), types.BoolNull(), null, null, ""},
		"permissions only":        {types.BoolNull(), types.BoolNull(), null, apiAccessTestSet("write"), ""},
		"false with permissions":  {types.BoolValue(false), types.BoolNull(), apiAccessTestSet("read"), null, "Access cannot be off with permissions"},
		"client false with perms": {types.BoolNull(), types.BoolValue(false), null, apiAccessTestSet("write"), "Access cannot be off with permissions"},
		"nothing":                 {types.BoolNull(), types.BoolNull(), null, null, "Grant gives nothing"},
		"explicit nothing":        {types.BoolValue(false), types.BoolValue(false), apiAccessTestSet(), apiAccessTestSet(), "Grant gives nothing"},
		"unknown":                 {types.BoolUnknown(), types.BoolNull(), null, null, ""},
	} {
		t.Run(name, func(t *testing.T) {
			m := apiClientAccessModel{
				ID: types.StringNull(), APIID: types.StringValue(apiAccessTestAPI), ClientID: types.StringValue(apiAccessTestClient),
				UserDelegatedAccess: tc.userAccess, UserDelegatedPermissions: tc.userKeys, ClientAccess: tc.clientAccess, ClientPermissions: tc.clientKeys,
			}
			cfg := tfsdk.Config{Schema: sr.Schema}
			st := tfsdk.State{Schema: sr.Schema}
			require.False(t, st.Set(ctx, &m).HasError())
			cfg.Raw = st.Raw
			resp := resource.ValidateConfigResponse{}
			(&apiClientAccessResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, &resp)
			if tc.want == "" {
				assert.False(t, resp.Diagnostics.HasError(), apiAccessTestText(resp.Diagnostics.Errors()))
			} else {
				assert.Contains(t, apiAccessTestText(resp.Diagnostics.Errors()), tc.want)
			}
		})
	}
}

// An unset access flag is planned as what Pocket ID will store.
func TestAPIClientAccessPlan_AccessFromPermissions(t *testing.T) {
	ctx := context.Background()
	sr := apiAccessTestSchema(t)
	for name, tc := range map[string]struct {
		config types.Bool
		keys   types.Set
		want   types.Bool
	}{
		"unset with permissions": {types.BoolNull(), apiAccessTestSet("read"), types.BoolValue(true)},
		"unset without":          {types.BoolNull(), apiAccessTestSet(), types.BoolValue(false)},
		"unset, keys unknown":    {types.BoolNull(), types.SetUnknown(types.StringType), types.BoolUnknown()},
		"explicit true":          {types.BoolValue(true), apiAccessTestSet(), types.BoolValue(true)},
	} {
		t.Run(name, func(t *testing.T) {
			m := apiAccessTestPlan(false, nil, false, nil)
			m.UserDelegatedAccess = types.BoolUnknown()
			m.UserDelegatedPermissions = tc.keys
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &m).HasError())
			req := planmodifier.BoolRequest{Path: path.Root("user_delegated_access"), ConfigValue: tc.config, PlanValue: types.BoolUnknown(), Plan: plan}
			resp := planmodifier.BoolResponse{PlanValue: tc.config}
			if tc.config.IsNull() {
				resp.PlanValue = types.BoolUnknown()
			}
			apiAccessFromPermissions{permissions: path.Root("user_delegated_permissions")}.PlanModifyBool(ctx, req, &resp)
			require.False(t, resp.Diagnostics.HasError())
			assert.Equal(t, tc.want, resp.PlanValue)
		})
	}
}

func TestAPIClientAccessImport(t *testing.T) {
	ctx := context.Background()
	sr := apiAccessTestSchema(t)
	for id, ok := range map[string]bool{
		apiAccessTestAPI + "/app":                 true,
		apiAccessTestAPI + "/my.client-id_2":      true,
		apiAccessTestAPI:                          false,
		"not-a-uuid/app":                          false,
		apiAccessTestAPI + "/":                    false,
		apiAccessTestAPI + "/https://x.example/c": false,
	} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
		(&apiClientAccessResource{}).ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		assert.Equal(t, !ok, resp.Diagnostics.HasError(), id)
		if ok {
			var clientID types.String
			require.False(t, resp.State.GetAttribute(ctx, path.Root("client_id"), &clientID).HasError())
			assert.Equal(t, strings.SplitN(id, "/", 2)[1], clientID.ValueString())
		}
	}
}
