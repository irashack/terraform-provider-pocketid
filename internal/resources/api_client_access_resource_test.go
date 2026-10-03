package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
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
	version  string
	grant    *client.APIClientGrant
	calls    []string
	failures map[string]apiTestFailure
	// tamper may change what a grant write stores.
	tamper func(g *client.APIClientGrant)
	// putBody and listBody, when set, replace the body of a successful grant
	// write (after it was applied) and of the client's grant list.
	putBody, listBody *string
	// clientBody, when set, replaces the body of the OIDC client read.
	clientBody *string
}

func newAPIAccessTestPocketID(t *testing.T) (*apiAccessTestPocketID, *client.Client) {
	return newAPIAccessTestPocketIDWithKey(t, "synthetic-token")
}

// newAPIAccessTestPocketIDWithKey serves the fake to a client that
// authenticates with key.
func newAPIAccessTestPocketIDWithKey(t *testing.T, key string) (*apiAccessTestPocketID, *client.Client) {
	f := &apiAccessTestPocketID{t: t, version: "2.16.0", failures: map[string]apiTestFailure{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, key, false, 5)
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
		if failure, failing := f.failures["GET version"]; failing {
			w.WriteHeader(failure.status)
			return
		}
		_, _ = fmt.Fprintf(w, `{"currentVersion":%q}`, f.version)
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
		f.fail(w, failure, route)
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
		f.fail(w, failure, route)
		return
	}
	if override := map[string]*string{"PUT grant": f.putBody, "GET grants": f.listBody, "GET client": f.clientBody}[route]; override != nil {
		_, _ = w.Write([]byte(*override))
		return
	}
	body, _ := json.Marshal(out)
	_, _ = w.Write(body)
}

// fail answers with the injected failure: a status, or a closed connection.
func (f *apiAccessTestPocketID) fail(w http.ResponseWriter, failure apiTestFailure, route string) {
	if failure.hangUp {
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(f.t, err)
		_ = conn.Close()
		return
	}
	w.WriteHeader(failure.status)
	if failure.status == 404 && route == "GET grants" {
		_, _ = fmt.Fprint(w, apiAccessTestClientGone)
	}
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
// the server holds is recorded, and nothing when it holds nothing. A refused
// write records nothing and reads nothing. (When the read fails too, see
// TestAPIClientAccessCreate_UnresolvedKeepsIdentity.)
func TestAPIClientAccessCreate_UncertainWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		failures map[string]apiTestFailure
		state    string // "" or "granted"
		summary  string
	}{
		"applied then 503": {map[string]apiTestFailure{"PUT grant": {status: 503, afterApply: true}}, "granted", "API access result uncertain"},
		"503 not applied":  {map[string]apiTestFailure{"PUT grant": {status: 503}}, "", "Error granting API access"},
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

// apiAccessConfig is a configuration with the access flags left unset, so
// they follow the permissions.
func apiAccessConfig(userKeys, clientKeys []string) apiClientAccessModel {
	return apiClientAccessModel{
		ID: types.StringNull(), APIID: types.StringValue(apiAccessTestAPI), ClientID: types.StringValue(apiAccessTestClient),
		UserDelegatedAccess: types.BoolNull(), UserDelegatedPermissions: apiAccessTestSet(userKeys...),
		ClientAccess: types.BoolNull(), ClientPermissions: apiAccessTestSet(clientKeys...),
	}
}

// apiAccessProposed is Terraform's proposed new state: the configuration with
// the prior state's values for what the configuration leaves unset.
func apiAccessProposed(config apiClientAccessModel, prior *apiClientAccessModel) apiClientAccessModel {
	proposed := config
	if prior != nil {
		proposed.ID = prior.ID
		if config.UserDelegatedAccess.IsNull() {
			proposed.UserDelegatedAccess = prior.UserDelegatedAccess
		}
		if config.ClientAccess.IsNull() {
			proposed.ClientAccess = prior.ClientAccess
		}
	}
	return proposed
}

// apiAccessSummary describes a recorded grant; unknown (null) values show as
// "unknown", so they cannot be mistaken for false or empty.
func apiAccessSummary(t *testing.T, m *apiClientAccessModel) string {
	t.Helper()
	if m == nil {
		return "no state"
	}
	flag := func(b types.Bool) string {
		if b.IsNull() {
			return "unknown"
		}
		return fmt.Sprint(b.ValueBool())
	}
	keys := func(s types.Set) string {
		if s.IsNull() {
			return "unknown"
		}
		return "[" + strings.Join(apiAccessTestKeys(t, s), " ") + "]"
	}
	return fmt.Sprintf("user=%s%s client=%s%s", flag(m.UserDelegatedAccess), keys(m.UserDelegatedPermissions), flag(m.ClientAccess), keys(m.ClientPermissions))
}

func apiAccessHasMarker(private []byte) bool {
	return strings.Contains(string(private), apiAccessUnresolvedKey)
}

// apiAccessStep is one plan and apply through the protocol server.
type apiAccessStep struct {
	plan    *tfprotov6.PlanResourceChangeResponse
	apply   *tfprotov6.ApplyResourceChangeResponse // nil when planning failed
	state   *apiClientAccessModel                  // nil when the new state is null
	private []byte
}

// apiAccessRun plans the configuration against the prior state and private
// state as Terraform would, and applies the plan when planning succeeded.
func apiAccessRun(t *testing.T, h *apiHarness, prior *apiClientAccessModel, priorPrivate []byte, config apiClientAccessModel) apiAccessStep {
	t.Helper()
	const name = "pocketid_api_client_access"
	r := &apiClientAccessResource{}
	proposed := apiAccessProposed(config, prior)
	priorValue := apiHarnessValue(t, r, prior)
	configValue := apiHarnessValue(t, r, &config)
	step := apiAccessStep{plan: h.plan(name, priorValue, configValue, apiHarnessValue(t, r, &proposed), priorPrivate)}
	if apiHarnessErrors(step.plan.Diagnostics) != "" {
		return step
	}
	step.apply = h.apply(name, priorValue, configValue, step.plan.PlannedState, step.plan.PlannedPrivate)
	step.private = step.apply.Private
	if state, ok := apiHarnessDecode[apiClientAccessModel](t, r, h.types[name], step.apply.NewState); ok {
		step.state = &state
	}
	if apiHarnessErrors(step.apply.Diagnostics) == "" {
		apiHarnessAssertApplied(t, h.types[name], step.plan.PlannedState, step.apply.NewState)
	}
	return step
}

func apiAccessMustApply(t *testing.T, h *apiHarness, prior *apiClientAccessModel, priorPrivate []byte, config apiClientAccessModel) apiAccessStep {
	t.Helper()
	step := apiAccessRun(t, h, prior, priorPrivate, config)
	require.Empty(t, apiHarnessErrors(step.plan.Diagnostics))
	require.NotNil(t, step.apply)
	require.Empty(t, apiHarnessErrors(step.apply.Diagnostics))
	require.NotNil(t, step.state)
	return step
}

// apiAccessRefresh reads the resource as a refresh does.
func apiAccessRefresh(t *testing.T, h *apiHarness, state *apiClientAccessModel, private []byte) (errs string, refreshed *apiClientAccessModel, newPrivate []byte) {
	t.Helper()
	resp := h.read("pocketid_api_client_access", apiHarnessValue(t, &apiClientAccessResource{}, state), private)
	if m, ok := apiHarnessDecode[apiClientAccessModel](t, &apiClientAccessResource{}, h.types["pocketid_api_client_access"], resp.NewState); ok {
		refreshed = &m
	}
	return apiHarnessErrors(resp.Diagnostics), refreshed, resp.Private
}

// A write whose outcome is uncertain and cannot be read back leaves state at
// the last grant the provider confirmed, never at "no access", and marks the
// resource: no plan or write is accepted until a read succeeds, and the
// diagnostic names the way out. This drives the framework's protocol server,
// so the plans are the ones Terraform would ask for after the failed apply.
func TestAPIClientAccessUpdate_UnresolvedKeepsConfirmedState(t *testing.T) {
	const prior, written = "user=true[read] client=true[write]", "user=true[read write] client=false[]"
	for name, tc := range map[string]struct {
		failure apiTestFailure
		server  string // what the server holds afterwards
	}{
		"503 before the change is made":     {apiTestFailure{status: 503}, prior},
		"503 after the change is made":      {apiTestFailure{status: 503, afterApply: true}, written},
		"connection closed before the PUT":  {apiTestFailure{hangUp: true}, prior},
		"connection closed after the PUT":   {apiTestFailure{hangUp: true, afterApply: true}, written},
		"change made, response unreadable ": {apiTestFailure{status: 200, afterApply: true}, written},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPIAccessTestPocketID(t)
			h := newAPIHarness(t, c)
			created := apiAccessMustApply(t, h, nil, nil, apiAccessConfig([]string{"read"}, []string{"write"}))
			require.Equal(t, prior, apiAccessSummary(t, created.state))
			require.False(t, apiAccessHasMarker(created.private))

			// The update removes client access and adds a user permission; the
			// PUT's outcome is unknown and the read-back fails as well.
			f.failures["PUT grant"] = tc.failure
			f.failures["GET grants"] = apiTestFailure{status: 403}
			update := apiAccessConfig([]string{"read", "write"}, nil)
			failed := apiAccessRun(t, h, created.state, created.private, update)
			require.Empty(t, apiHarnessErrors(failed.plan.Diagnostics))
			require.NotNil(t, failed.apply)
			assert.Contains(t, apiHarnessErrors(failed.apply.Diagnostics), "API access result uncertain")
			assert.Contains(t, apiHarnessErrors(failed.apply.Diagnostics), "state rm")
			require.NotNil(t, failed.state)
			assert.Equal(t, prior, apiAccessSummary(t, failed.state), "state keeps the last confirmed grant")
			assert.Equal(t, created.state.ID, failed.state.ID)
			assert.True(t, apiAccessHasMarker(failed.private), "the unresolved outcome is saved with the resource")

			// A plan that does not refresh, for the same or for unchanged
			// configuration, is refused: it would be made from state that may be
			// stale.
			for label, config := range map[string]apiClientAccessModel{"same change": update, "unchanged": apiAccessConfig([]string{"read"}, []string{"write"})} {
				again := apiAccessRun(t, h, failed.state, failed.private, config)
				errs := apiHarnessErrors(again.plan.Diagnostics)
				assert.Contains(t, errs, "API access outcome unresolved", label)
				assert.Contains(t, errs, "without -refresh=false", label)
				assert.Contains(t, errs, "state rm", label)
				assert.Contains(t, errs, apiAccessTestAPI+"/"+apiAccessTestClient, label)
				assert.Nil(t, again.apply, label)
			}

			// Nor does a write from a plan made earlier go through.
			before := len(f.routes())
			stale := h.apply("pocketid_api_client_access",
				apiHarnessValue(t, &apiClientAccessResource{}, failed.state), apiHarnessValue(t, &apiClientAccessResource{}, &update),
				failed.plan.PlannedState, failed.private)
			assert.Contains(t, apiHarnessErrors(stale.Diagnostics), "API access outcome unresolved")
			assert.Equal(t, before, len(f.routes()), "no request was sent")

			// A refresh that fails leaves the marker; one that succeeds shows
			// what the server holds and clears it.
			errs, _, private := apiAccessRefresh(t, h, failed.state, failed.private)
			assert.NotEmpty(t, errs)
			assert.True(t, apiAccessHasMarker(private))
			delete(f.failures, "GET grants")
			errs, refreshed, private := apiAccessRefresh(t, h, failed.state, failed.private)
			require.Empty(t, errs)
			require.NotNil(t, refreshed)
			assert.Equal(t, tc.server, apiAccessSummary(t, refreshed))
			assert.False(t, apiAccessHasMarker(private))

			// Planning works again, and applying the configuration converges.
			delete(f.failures, "PUT grant")
			done := apiAccessMustApply(t, h, refreshed, private, update)
			assert.Equal(t, written, apiAccessSummary(t, done.state))
			assert.False(t, apiAccessHasMarker(done.private))
		})
	}
}

// Only a definite refusal is final; an unread result is never one, even when
// the error also carries a client-error status.
func TestAPIWriteRefused(t *testing.T) {
	refusal := &client.HTTPError{StatusCode: 400}
	assert.True(t, apiWriteRefused(refusal))
	assert.True(t, apiWriteRefused(fmt.Errorf("wrapped: %w", refusal)))
	assert.False(t, apiWriteRefused(&client.HTTPError{StatusCode: 503}))
	assert.False(t, apiWriteRefused(&client.HTTPError{StatusCode: 408}))
	assert.False(t, apiWriteRefused(fmt.Errorf("%w: %w", client.ErrResultUnread, refusal)))
	assert.False(t, apiWriteRefused(fmt.Errorf("%w: %w", refusal, client.ErrResultUnread)))
	assert.False(t, apiWriteRefused(client.ErrResultUnread))
	assert.False(t, apiWriteRefused(errors.New("connection reset")))
}

// A successful answer to the grant PUT that does not describe a grant (null,
// {}, a partial object) is an unread result, not "no grant": the write may
// have been applied, so the resource reads the grant back, keeps the pair's
// identity, and never records access that was not confirmed as false.
func TestAPIClientAccess_IncompletePutResponse(t *testing.T) {
	const prior = "user=true[read] client=true[write]"
	for name, body := range map[string]string{
		"null":           `null`,
		"empty object":   `{}`,
		"partial object": `{"userDelegatedAccess":true}`,
		"no list fields": `{"userDelegatedAccess":true,"clientAccess":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Run("create, read back", func(t *testing.T) {
				f, c := newAPIAccessTestPocketID(t)
				h := newAPIHarness(t, c)
				f.putBody = &body
				step := apiAccessRun(t, h, nil, nil, apiAccessConfig([]string{"read"}, nil))
				require.NotNil(t, step.apply)
				errs := apiHarnessErrors(step.apply.Diagnostics)
				assert.Contains(t, errs, "API access result uncertain")
				assert.NotContains(t, errs, "stored no grant")
				require.NotNil(t, step.state, "the identity of a grant the server holds is kept")
				assert.Equal(t, "user=true[read] client=false[]", apiAccessSummary(t, step.state))
				assert.False(t, apiAccessHasMarker(step.private), "a confirmed read-back needs no marker")
			})
			t.Run("create, read back fails", func(t *testing.T) {
				f, c := newAPIAccessTestPocketID(t)
				h := newAPIHarness(t, c)
				f.putBody = &body
				f.failures["GET grants"] = apiTestFailure{status: 403}
				step := apiAccessRun(t, h, nil, nil, apiAccessConfig([]string{"read"}, nil))
				require.NotNil(t, step.apply)
				assert.Contains(t, apiHarnessErrors(step.apply.Diagnostics), "API access result uncertain")
				require.NotNil(t, step.state)
				assert.Equal(t, apiAccessTestAPI+"/"+apiAccessTestClient, step.state.ID.ValueString())
				assert.True(t, step.state.UserDelegatedAccess.IsNull() && step.state.ClientAccess.IsNull(), "access is unknown, not false")
				assert.True(t, step.state.UserDelegatedPermissions.IsNull() && step.state.ClientPermissions.IsNull(), "permissions are unknown, not empty")
				assert.True(t, apiAccessHasMarker(step.private))
			})
			t.Run("update, read back", func(t *testing.T) {
				f, c := newAPIAccessTestPocketID(t)
				h := newAPIHarness(t, c)
				created := apiAccessMustApply(t, h, nil, nil, apiAccessConfig([]string{"read"}, []string{"write"}))
				require.Equal(t, prior, apiAccessSummary(t, created.state))
				f.putBody = &body
				step := apiAccessRun(t, h, created.state, created.private, apiAccessConfig([]string{"read", "write"}, nil))
				require.NotNil(t, step.apply)
				assert.Contains(t, apiHarnessErrors(step.apply.Diagnostics), "API access result uncertain")
				require.NotNil(t, step.state)
				assert.Equal(t, "user=true[read write] client=false[]", apiAccessSummary(t, step.state), "state shows what the server holds")
				assert.False(t, apiAccessHasMarker(step.private))
			})
			t.Run("update, read back fails", func(t *testing.T) {
				f, c := newAPIAccessTestPocketID(t)
				h := newAPIHarness(t, c)
				created := apiAccessMustApply(t, h, nil, nil, apiAccessConfig([]string{"read"}, []string{"write"}))
				f.putBody = &body
				f.failures["GET grants"] = apiTestFailure{status: 403}
				step := apiAccessRun(t, h, created.state, created.private, apiAccessConfig([]string{"read", "write"}, nil))
				require.NotNil(t, step.apply)
				assert.Contains(t, apiHarnessErrors(step.apply.Diagnostics), "API access result uncertain")
				require.NotNil(t, step.state)
				assert.Equal(t, prior, apiAccessSummary(t, step.state), "no revocation is recorded that no read confirmed")
				assert.True(t, apiAccessHasMarker(step.private))
			})
		})
	}
}

// A grant list that is not a list, or whose entries do not say what the client
// may do, fails the refresh and leaves the resource in state: it is not
// evidence that the grant is gone.
func TestAPIClientAccessRead_IncompleteList(t *testing.T) {
	entry := func(fields string) string {
		return fmt.Sprintf(`[{"api":{"id":%q,"permissions":[]}%s}]`, apiAccessTestAPI, fields)
	}
	for name, body := range map[string]string{
		"null":                `null`,
		"empty object":        `{}`,
		"empty entry":         `[{}]`,
		"entry without grant": entry(``),
		"entry missing lists": entry(`,"userDelegatedAccess":true,"clientAccess":false`),
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPIAccessTestPocketID(t)
			h := newAPIHarness(t, c)
			created := apiAccessMustApply(t, h, nil, nil, apiAccessConfig([]string{"read"}, nil))
			f.listBody = &body
			errs, refreshed, _ := apiAccessRefresh(t, h, created.state, created.private)
			assert.Contains(t, errs, "Error reading API access")
			require.NotNil(t, refreshed, "the resource stays in state")
			assert.Equal(t, apiAccessSummary(t, created.state), apiAccessSummary(t, refreshed))
		})
	}
}

// Identifiers that carry the API key never enter a request, a log line or a
// diagnostic, wherever they come from: the import ID (both halves of the
// pair, and the whole), configuration, and state. Each is refused with fixed
// text before anything is stored or sent.
func TestAPIClientAccess_KeyBearingIdentities(t *testing.T) {
	const uuidKey = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	const clientKey = "synthetic-client-key-0123"
	ctx := context.Background()
	text := func(diags interface {
		Errors() diag.Diagnostics
	}) string {
		var parts []string
		for _, d := range diags.Errors() {
			parts = append(parts, d.Summary()+": "+d.Detail())
		}
		return strings.Join(parts, "\n")
	}

	t.Run("import", func(t *testing.T) {
		sr := apiAccessTestSchema(t)
		for name, tc := range map[string]struct {
			key, id string
		}{
			"api half is the key":      {uuidKey, uuidKey + "/app"},
			"client half is the key":   {clientKey, apiAccessTestAPI + "/" + clientKey},
			"client half contains key": {clientKey, apiAccessTestAPI + "/x-" + clientKey},
			"whole ID contains key":    {"abc/def-0123456789", apiAccessTestAPI + "/abc/def-0123456789"},
		} {
			t.Run(name, func(t *testing.T) {
				_, c := newAPIAccessTestPocketIDWithKey(t, tc.key)
				resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
				(&apiClientAccessResource{client: c}).ImportState(ctx, resource.ImportStateRequest{ID: tc.id}, &resp)
				require.True(t, resp.Diagnostics.HasError())
				assert.NotContains(t, text(resp.Diagnostics), tc.key)
				assert.True(t, resp.State.Raw.IsNull(), "nothing is stored")
			})
		}
		// An identity without the key still imports.
		_, c := newAPIAccessTestPocketIDWithKey(t, uuidKey)
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
		(&apiClientAccessResource{client: c}).ImportState(ctx, resource.ImportStateRequest{ID: apiAccessTestAPI + "/app"}, &resp)
		assert.False(t, resp.Diagnostics.HasError())
	})

	// Operations on an identity from state or configuration that carries the
	// key refuse before any request and show nothing of it.
	t.Run("operations", func(t *testing.T) {
		f, c := newAPIAccessTestPocketIDWithKey(t, uuidKey)
		h := newAPIHarness(t, c)
		r := &apiClientAccessResource{}
		state := apiAccessModel(uuidKey, "app", apiAccessGrant{UserAccess: true})
		config := apiAccessConfig([]string{"read"}, nil)
		config.APIID = types.StringValue(uuidKey)

		errs, refreshed, _ := apiAccessRefresh(t, h, &state, nil)
		assert.Contains(t, errs, "Unusable identifier")
		assert.NotContains(t, errs, uuidKey)
		require.NotNil(t, refreshed, "state is left as it was")

		step := apiAccessRun(t, h, nil, nil, config)
		require.NotNil(t, step.apply)
		assert.Contains(t, apiHarnessErrors(step.apply.Diagnostics), "Unusable identifier")
		assert.NotContains(t, apiHarnessErrors(step.apply.Diagnostics), uuidKey)
		assert.Nil(t, step.state)

		priorValue := apiHarnessValue(t, r, &state)
		null := apiHarnessValue(t, r, (*apiClientAccessModel)(nil))
		destroy := h.apply("pocketid_api_client_access", priorValue, null, h.dynamic("pocketid_api_client_access", null), nil)
		assert.Contains(t, apiHarnessErrors(destroy.Diagnostics), "Unusable identifier")
		assert.NotContains(t, apiHarnessErrors(destroy.Diagnostics), uuidKey)

		update := apiAccessRun(t, h, &state, nil, config)
		require.NotNil(t, update.apply)
		assert.Contains(t, apiHarnessErrors(update.apply.Diagnostics), "Unusable identifier")
		assert.NotContains(t, apiHarnessErrors(update.apply.Diagnostics), uuidKey)

		assert.Empty(t, f.routes(), "no request carried the identity")
	})

	// The plan refusal of an unresolved outcome prints the identity from state
	// only when it passes the check.
	t.Run("unresolved plan diagnostic", func(t *testing.T) {
		_, c := newAPIAccessTestPocketIDWithKey(t, uuidKey)
		h := newAPIHarness(t, c)
		marker, err := json.Marshal(map[string][]byte{apiAccessUnresolvedKey: []byte(`{"unresolved":true}`)})
		require.NoError(t, err)
		for name, tc := range map[string]struct {
			state     apiClientAccessModel
			wantShown string
		}{
			"api ID is the key": {apiAccessModel(uuidKey, "app", apiAccessGrant{UserAccess: true}), ""},
			"clean identity":    {apiAccessModel(apiAccessTestAPI, "app", apiAccessGrant{UserAccess: true}), apiAccessTestAPI + "/app"},
		} {
			t.Run(name, func(t *testing.T) {
				config := apiAccessConfig([]string{"read"}, nil)
				config.APIID = tc.state.APIID
				step := apiAccessRun(t, h, &tc.state, marker, config)
				errs := apiHarnessErrors(step.plan.Diagnostics)
				assert.Contains(t, errs, "API access outcome unresolved")
				assert.Contains(t, errs, "state rm")
				assert.NotContains(t, errs, uuidKey)
				if tc.wantShown != "" {
					assert.Contains(t, errs, tc.wantShown)
				}
			})
		}
	})
}

// The public-client check reads only the isPublic flag, so a numeric field
// that overflows is not decoded at all, and an answer without a boolean flag
// is refused with fixed text (never taken as "not public") before any write.
func TestAPIClientAccess_ClientCheckDecodesOnlyTheFlag(t *testing.T) {
	const numericKey = "1234567890123456"
	overflow := "99" + numericKey + "999999"
	for name, tc := range map[string]struct {
		body    string
		refused bool
	}{
		"overflowing other field": {`{"id":"app","isPublic":false,"accessTokenDurationMinutes":` + overflow + `}`, false},
		"flag is a number":        {`{"id":"app","isPublic":` + numericKey + `}`, true},
		"flag missing":            {`{"id":"app"}`, true},
		"not an object":           {`[` + overflow + `]`, true},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPIAccessTestPocketIDWithKey(t, numericKey)
			f.clientBody = &tc.body
			errs, state := apiAccessTestCreate(t, c, apiAccessTestPlan(false, nil, true, []string{"write"}))
			if !tc.refused {
				require.Empty(t, errs)
				require.NotNil(t, state)
				return
			}
			assert.Contains(t, errs, "Could not read OIDC client")
			assert.Contains(t, errs, "could not be decoded")
			assert.Contains(t, errs, "no mutation was attempted")
			assert.NotContains(t, errs, numericKey)
			assert.NotContains(t, errs, overflow)
			assert.Nil(t, state)
			assert.NotContains(t, f.routes(), "PUT grant")
		})
	}
}

// A destroy is never held back by an unresolved outcome: removing the grant
// cannot widen access.
func TestAPIClientAccessDelete_UnresolvedOutcome(t *testing.T) {
	f, c := newAPIAccessTestPocketID(t)
	h := newAPIHarness(t, c)
	created := apiAccessMustApply(t, h, nil, nil, apiAccessConfig([]string{"read"}, nil))
	f.failures["PUT grant"] = apiTestFailure{status: 503}
	f.failures["GET grants"] = apiTestFailure{status: 403}
	failed := apiAccessRun(t, h, created.state, created.private, apiAccessConfig([]string{"read", "write"}, nil))
	require.True(t, apiAccessHasMarker(failed.private))
	f.failures = map[string]apiTestFailure{}

	r := &apiClientAccessResource{}
	priorValue := apiHarnessValue(t, r, failed.state)
	null := apiHarnessValue(t, r, (*apiClientAccessModel)(nil))
	plan := h.plan("pocketid_api_client_access", priorValue, null, null, failed.private)
	require.Empty(t, apiHarnessErrors(plan.Diagnostics))
	destroyed := h.apply("pocketid_api_client_access", priorValue, null, plan.PlannedState, plan.PlannedPrivate)
	require.Empty(t, apiHarnessErrors(destroyed.Diagnostics))
	assert.Contains(t, f.routes(), "DELETE grant")
	assert.Nil(t, f.grant)
}

// A create whose outcome is uncertain and cannot be read back keeps the
// pair's identity, so it can be refreshed, replaced or removed, without
// recording what it grants: access flags and permissions are unknown (null),
// not false or empty.
func TestAPIClientAccessCreate_UnresolvedKeepsIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		failure   apiTestFailure
		committed bool
	}{
		"503 before the change is made":    {apiTestFailure{status: 503}, false},
		"503 after the change is made":     {apiTestFailure{status: 503, afterApply: true}, true},
		"connection closed before the PUT": {apiTestFailure{hangUp: true}, false},
		"connection closed after the PUT":  {apiTestFailure{hangUp: true, afterApply: true}, true},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newAPIAccessTestPocketID(t)
			h := newAPIHarness(t, c)
			f.failures["PUT grant"] = tc.failure
			f.failures["GET grants"] = apiTestFailure{status: 403}
			config := apiAccessConfig([]string{"read"}, nil)
			failed := apiAccessRun(t, h, nil, nil, config)
			require.Empty(t, apiHarnessErrors(failed.plan.Diagnostics))
			assert.Contains(t, apiHarnessErrors(failed.apply.Diagnostics), "API access result uncertain")
			assert.Contains(t, apiHarnessErrors(failed.apply.Diagnostics), "state rm")
			require.NotNil(t, failed.state, "the pair's identity is kept")
			assert.Equal(t, apiAccessTestAPI+"/"+apiAccessTestClient, failed.state.ID.ValueString())
			assert.True(t, failed.state.UserDelegatedAccess.IsNull() && failed.state.ClientAccess.IsNull(), "access is not recorded as false")
			assert.True(t, failed.state.UserDelegatedPermissions.IsNull() && failed.state.ClientPermissions.IsNull(), "permissions are not recorded as empty")
			assert.True(t, apiAccessHasMarker(failed.private))

			// Terraform keeps the object tainted and plans its replacement from
			// nothing, which is not held back.
			replace := apiAccessRun(t, h, nil, nil, config)
			assert.Empty(t, apiHarnessErrors(replace.plan.Diagnostics))

			errs, _, private := apiAccessRefresh(t, h, failed.state, failed.private)
			assert.NotEmpty(t, errs)
			assert.True(t, apiAccessHasMarker(private))
			delete(f.failures, "GET grants")
			errs, refreshed, private := apiAccessRefresh(t, h, failed.state, failed.private)
			require.Empty(t, errs)
			if tc.committed {
				require.NotNil(t, refreshed)
				assert.Equal(t, "user=true[read] client=false[]", apiAccessSummary(t, refreshed))
				assert.False(t, apiAccessHasMarker(private))
			} else {
				assert.Nil(t, refreshed, "the server holds no grant, so the pair leaves state")
			}
		})
	}
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
				if r.URL.Path == "/api/version/current" {
					_, _ = fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
					return
				}
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

// Delete is gated like Create and Update: no DELETE is sent to a server
// older than 2.14.0 or one whose version cannot be read.
func TestAPIClientAccessDelete_VersionGate(t *testing.T) {
	for name, setup := range map[string]func(f *apiAccessTestPocketID){
		"old server":         func(f *apiAccessTestPocketID) { f.version = "2.13.0" },
		"malformed version":  func(f *apiAccessTestPocketID) { f.version = "not-a-version" },
		"version unreadable": func(f *apiAccessTestPocketID) { f.failures["GET version"] = apiTestFailure{status: 403} },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, c := newAPIAccessTestPocketID(t)
			f.grant = &client.APIClientGrant{UserDelegatedAccess: true}
			setup(f)
			sr := apiAccessTestSchema(t)
			prior := apiAccessModel(apiAccessTestAPI, apiAccessTestClient, apiAccessGrant{UserAccess: true})
			st := tfsdk.State{Schema: sr.Schema}
			require.False(t, st.Set(ctx, &prior).HasError())
			resp := resource.DeleteResponse{State: st}
			(&apiClientAccessResource{client: c}).Delete(ctx, resource.DeleteRequest{State: st}, &resp)
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, resp.Diagnostics[0].Detail(), "no mutation was attempted")
			assert.Empty(t, f.routes())
			assert.NotNil(t, f.grant)
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
