package resources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/resources"
)

func TestNewScimServiceProviderResource(t *testing.T) {
	r := resources.NewScimServiceProviderResource()
	assert.NotNil(t, r)
	assert.Implements(t, (*resource.Resource)(nil), r)
}

func TestScimServiceProviderResource_Metadata(t *testing.T) {
	r := resources.NewScimServiceProviderResource()

	req := resource.MetadataRequest{
		ProviderTypeName: "pocketid",
	}
	resp := &resource.MetadataResponse{}

	r.Metadata(context.TODO(), req, resp)

	assert.Equal(t, "pocketid_scim_service_provider", resp.TypeName)
}

func TestScimServiceProviderResource_Schema(t *testing.T) {
	r := resources.NewScimServiceProviderResource()

	req := resource.SchemaRequest{}
	resp := &resource.SchemaResponse{}

	r.Schema(context.TODO(), req, resp)

	schema := resp.Schema
	assert.NotNil(t, schema)

	assert.Contains(t, schema.Attributes, "id")
	assert.Contains(t, schema.Attributes, "client_id")
	assert.Contains(t, schema.Attributes, "endpoint")
	assert.Contains(t, schema.Attributes, "token")
	assert.Contains(t, schema.Attributes, "last_synced_at")
	assert.Contains(t, schema.Attributes, "created_at")

	idAttr := schema.Attributes["id"]
	assert.True(t, idAttr.IsComputed())
	assert.False(t, idAttr.IsRequired())

	clientIDAttr := schema.Attributes["client_id"]
	assert.True(t, clientIDAttr.IsRequired())
	assert.False(t, clientIDAttr.IsComputed())

	endpointAttr := schema.Attributes["endpoint"]
	assert.True(t, endpointAttr.IsRequired())

	tokenAttr := schema.Attributes["token"]
	assert.True(t, tokenAttr.IsOptional())
	assert.True(t, tokenAttr.IsSensitive())

	lastSyncedAtAttr := schema.Attributes["last_synced_at"]
	assert.True(t, lastSyncedAtAttr.IsComputed())

	createdAtAttr := schema.Attributes["created_at"]
	assert.True(t, createdAtAttr.IsComputed())
}

func TestScimServiceProviderResource_Configure(t *testing.T) {
	tests := []struct {
		name         string
		providerData interface{}
		expectError  bool
	}{
		{
			name:         "nil provider data",
			providerData: nil,
			expectError:  false,
		},
		{
			name:         "invalid provider data type",
			providerData: "invalid",
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := resources.NewScimServiceProviderResource()

			cfgResource, ok := r.(resource.ResourceWithConfigure)
			assert.True(t, ok, "Resource should implement ResourceWithConfigure")

			req := resource.ConfigureRequest{
				ProviderData: tt.providerData,
			}
			resp := &resource.ConfigureResponse{}

			cfgResource.Configure(context.TODO(), req, resp)

			if tt.expectError {
				assert.True(t, resp.Diagnostics.HasError())
			} else {
				assert.False(t, resp.Diagnostics.HasError())
			}
		})
	}
}

const (
	scimTestProviderID = "33333333-3333-4333-8333-333333333333"
	scimTestClientID   = "scim-client"
)

// scimFake is a Pocket ID stand-in that answers the SCIM service provider
// routes from a mutable record and remembers each request it received.
type scimFake struct {
	mu       sync.Mutex
	exists   bool
	endpoint string
	token    string
	synced   *string
	requests []scimRequest
	// failWith, when set, answers every request with this status and body.
	failWith *scimFailure
	// getBody, when set, is the 200 answer to the read by client.
	getBody *string
}

type scimRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

type scimFailure struct {
	Status int
	Body   string
}

const (
	scimNotFoundBody       = `{"error":"SCIM service provider not found","code":"not_found","details":{"resource":"SCIM service provider"}}`
	scimClientNotFoundBody = `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`
	scimMissingRouteBody   = `{"error":"API endpoint not found"}`
)

func newScimFake(t *testing.T) (*scimFake, *client.Client) {
	t.Helper()
	fake := &scimFake{exists: true, endpoint: "https://scim.example.com/v2"}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return fake, c
}

func (f *scimFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := scimRequest{Method: r.Method, Path: r.URL.Path}
	if r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			record.Body = body
		}
	}
	f.requests = append(f.requests, record)
	w.Header().Set("Content-Type", "application/json")
	if f.failWith != nil {
		w.WriteHeader(f.failWith.Status)
		_, _ = w.Write([]byte(f.failWith.Body))
		return
	}
	provider := func() {
		out := map[string]any{
			"id": scimTestProviderID, "endpoint": f.endpoint, "token": f.token,
			"lastSyncedAt": f.synced, "createdAt": "2026-01-01T00:00:00Z",
			"oidcClient": map[string]any{"id": scimTestClientID, "name": "SCIM client"},
		}
		_ = json.NewEncoder(w).Encode(out)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/oidc/clients/"+scimTestClientID+"/scim-service-provider":
		if !f.exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(scimNotFoundBody))
			return
		}
		if f.getBody != nil {
			_, _ = w.Write([]byte(*f.getBody))
			return
		}
		provider()
	case r.Method == http.MethodPost && r.URL.Path == "/api/scim/service-provider":
		f.exists = true
		f.endpoint, _ = record.Body["endpoint"].(string)
		f.token, _ = record.Body["token"].(string)
		w.WriteHeader(http.StatusCreated)
		provider()
	case r.Method == http.MethodPut && r.URL.Path == "/api/scim/service-provider/"+scimTestProviderID:
		if !f.exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(scimNotFoundBody))
			return
		}
		f.endpoint, _ = record.Body["endpoint"].(string)
		f.token, _ = record.Body["token"].(string)
		provider()
	case r.Method == http.MethodPost && r.URL.Path == "/api/scim/service-provider/"+scimTestProviderID+"/sync":
		if !f.exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(scimNotFoundBody))
			return
		}
		now := "2026-02-01T00:00:00Z"
		f.synced = &now
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete && r.URL.Path == "/api/scim/service-provider/"+scimTestProviderID:
		if !f.exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(scimNotFoundBody))
			return
		}
		f.exists = false
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(scimMissingRouteBody))
	}
}

func (f *scimFake) recorded() []scimRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scimRequest(nil), f.requests...)
}

// mutations returns the requests that changed something.
func (f *scimFake) mutations() []scimRequest {
	var out []scimRequest
	for _, r := range f.recorded() {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

func scimResource(t *testing.T, c *client.Client) resource.Resource {
	t.Helper()
	r := resources.NewScimServiceProviderResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	return r
}

func scimSchema(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

// scimObject builds a raw object value for the schema from the attributes
// given; every other attribute is null.
func scimObject(t *testing.T, sch schema.Schema, values map[string]any) tftypes.Value {
	t.Helper()
	objectType, ok := sch.Type().TerraformType(context.Background()).(tftypes.Object)
	require.True(t, ok)
	attributes := map[string]tftypes.Value{}
	for name, attrType := range objectType.AttributeTypes {
		if v, set := values[name]; set {
			attributes[name] = tftypes.NewValue(attrType, v)
		} else {
			attributes[name] = tftypes.NewValue(attrType, nil)
		}
	}
	return tftypes.NewValue(objectType, attributes)
}

func scimState(t *testing.T, sch schema.Schema, values map[string]any) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: sch, Raw: scimObject(t, sch, values)}
}

func scimStoredState(token any) map[string]any {
	return map[string]any{
		"id": scimTestProviderID, "client_id": scimTestClientID,
		"endpoint": "https://scim.example.com/v2", "token": token,
		"created_at": "2026-01-01T00:00:00Z",
	}
}

// scimRead runs Read over a prior state and returns the response.
func scimRead(t *testing.T, r resource.Resource, state tfsdk.State) *resource.ReadResponse {
	t.Helper()
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	return resp
}

func scimStateString(t *testing.T, state tfsdk.State, name string) types.String {
	t.Helper()
	var out types.String
	require.False(t, state.GetAttribute(context.Background(), path.Root(name), &out).HasError())
	return out
}

func TestScimServiceProviderResource_ReadDropsOnlyAConfirmedAbsence(t *testing.T) {
	cases := []struct {
		name    string
		failure *scimFailure
		dropped bool
		errored bool
	}{
		{"Pocket ID's not-found for the SCIM service provider", &scimFailure{404, scimNotFoundBody}, true, false},
		{"not-found naming another kind", &scimFailure{404, scimClientNotFoundBody}, false, true},
		{"the router's unknown-route 404", &scimFailure{404, scimMissingRouteBody}, false, true},
		{"a bare 404 from a proxy", &scimFailure{404, `<html>not found</html>`}, false, true},
		{"a server error", &scimFailure{500, `{"error":"boom"}`}, false, true},
		{"a rejected key", &scimFailure{401, `{"error":"nope","code":"not_signed_in"}`}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, c := newScimFake(t)
			fake.failWith = tc.failure
			r := scimResource(t, c)
			sch := scimSchema(t, r)

			resp := scimRead(t, r, scimState(t, sch, scimStoredState("abc")))
			assert.Equal(t, tc.errored, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, tc.dropped, resp.State.Raw.IsNull(), "dropped from state")
			for _, d := range resp.Diagnostics {
				assert.NotContains(t, d.Detail(), "abc", "a diagnostic must not carry the token")
			}
		})
	}
}

func TestScimServiceProviderResource_ReadRemovesAProviderDeletedOutsideTerraform(t *testing.T) {
	fake, c := newScimFake(t)
	fake.exists = false
	r := scimResource(t, c)
	sch := scimSchema(t, r)

	resp := scimRead(t, r, scimState(t, sch, scimStoredState("abc")))
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.True(t, resp.State.Raw.IsNull())
}

func TestScimServiceProviderResource_DeleteAcceptsOnlyAConfirmedAbsence(t *testing.T) {
	cases := []struct {
		name    string
		exists  bool
		failure *scimFailure
		errored bool
	}{
		{"deletes an existing provider", true, nil, false},
		{"Pocket ID's not-found for the SCIM service provider", false, nil, false},
		{"not-found naming another kind", true, &scimFailure{404, scimClientNotFoundBody}, true},
		{"the router's unknown-route 404", true, &scimFailure{404, scimMissingRouteBody}, true},
		{"a server error", true, &scimFailure{500, `{"error":"boom"}`}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, c := newScimFake(t)
			fake.exists = tc.exists
			fake.failWith = tc.failure
			r := scimResource(t, c)
			sch := scimSchema(t, r)

			resp := &resource.DeleteResponse{}
			r.Delete(context.Background(), resource.DeleteRequest{State: scimState(t, sch, scimStoredState("abc"))}, resp)
			assert.Equal(t, tc.errored, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		})
	}
}

// Pocket ID always returns the token field, and "" when none is configured.
// The state must follow it, without churn for an unconfigured or an empty
// token.
func TestScimServiceProviderResource_ReadTracksTheServersToken(t *testing.T) {
	cases := []struct {
		name   string
		prior  any // the token the state held
		server string
		want   types.String
	}{
		{"a token cleared outside Terraform shows as drift", "abc", "", types.StringValue("")},
		{"a token changed outside Terraform shows as drift", "abc", "other", types.StringValue("other")},
		{"an unchanged token stays", "abc", "abc", types.StringValue("abc")},
		{"an unconfigured token with no server token stays null", nil, "", types.StringNull()},
		{"an explicitly empty token with no server token stays empty", "", "", types.StringValue("")},
		{"a token set outside Terraform over an unconfigured one shows as drift", nil, "outside", types.StringValue("outside")},
		{"a token set outside Terraform over an empty one shows as drift", "", "outside", types.StringValue("outside")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, c := newScimFake(t)
			fake.token = tc.server
			r := scimResource(t, c)
			sch := scimSchema(t, r)

			resp := scimRead(t, r, scimState(t, sch, scimStoredState(tc.prior)))
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, tc.want, scimStateString(t, resp.State, "token"))
		})
	}
}

func TestScimServiceProviderResource_WriteOnlySchema(t *testing.T) {
	r := resources.NewScimServiceProviderResource()
	sch := scimSchema(t, r)

	tokenWO, ok := sch.Attributes["token_wo"].(schema.StringAttribute)
	require.True(t, ok)
	assert.True(t, tokenWO.WriteOnly, "token_wo must be write-only")
	assert.True(t, tokenWO.Sensitive)
	assert.True(t, tokenWO.Optional)
	assert.False(t, tokenWO.Computed)

	version, ok := sch.Attributes["token_wo_version"].(schema.StringAttribute)
	require.True(t, ok)
	assert.True(t, version.Optional)
	assert.False(t, version.WriteOnly, "the version must reach the plan and the state")

	token, ok := sch.Attributes["token"].(schema.StringAttribute)
	require.True(t, ok)
	assert.False(t, token.WriteOnly)
	assert.True(t, token.Sensitive)
}

func scimConfig(t *testing.T, sch schema.Schema, values map[string]any) tfsdk.Config {
	t.Helper()
	return tfsdk.Config{Schema: sch, Raw: scimObject(t, sch, values)}
}

func scimPlan(t *testing.T, sch schema.Schema, values map[string]any) tfsdk.Plan {
	t.Helper()
	return tfsdk.Plan{Schema: sch, Raw: scimObject(t, sch, values)}
}

// A write-only token reaches Pocket ID on create, and neither the state nor
// the planned value keeps it, although the server returns it on every read.
func TestScimServiceProviderResource_CreateSendsTheWriteOnlyToken(t *testing.T) {
	fake, c := newScimFake(t)
	fake.exists = false
	r := scimResource(t, c)
	sch := scimSchema(t, r)

	plan := map[string]any{
		"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v2", "token_wo_version": "1",
		"id": tftypes.UnknownValue, "last_synced_at": tftypes.UnknownValue, "created_at": tftypes.UnknownValue,
	}
	config := map[string]any{
		"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v2",
		"token_wo": "secret-from-config", "token_wo_version": "1",
	}
	resp := &resource.CreateResponse{State: scimState(t, sch, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: scimPlan(t, sch, plan), Config: scimConfig(t, sch, config)}, resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	mutations := fake.mutations()
	require.Len(t, mutations, 1)
	assert.Equal(t, "secret-from-config", mutations[0].Body["token"])
	assert.Equal(t, types.StringNull(), scimStateString(t, resp.State, "token"), "the plain token stays null")
	assert.Equal(t, types.StringNull(), scimStateString(t, resp.State, "token_wo"), "a write-only value is never stored")
	assert.Equal(t, types.StringValue("1"), scimStateString(t, resp.State, "token_wo_version"))
}

func scimWriteOnlyState(version string) map[string]any {
	return map[string]any{
		"id": scimTestProviderID, "client_id": scimTestClientID,
		"endpoint": "https://scim.example.com/v2", "token_wo_version": version,
		"created_at": "2026-01-01T00:00:00Z",
	}
}

func scimUpdate(t *testing.T, r resource.Resource, sch schema.Schema, state, plan, config map[string]any) *resource.UpdateResponse {
	t.Helper()
	resp := &resource.UpdateResponse{State: scimState(t, sch, state)}
	r.Update(context.Background(), resource.UpdateRequest{
		State:  scimState(t, sch, state),
		Plan:   scimPlan(t, sch, plan),
		Config: scimConfig(t, sch, config),
	}, resp)
	return resp
}

// The PUT replaces the token, so an update that does not send the write-only
// value again must carry the token Pocket ID already holds.
func TestScimServiceProviderResource_UpdateWithoutAVersionBumpKeepsTheServersToken(t *testing.T) {
	fake, c := newScimFake(t)
	fake.token = "token-held-by-the-server"
	r := scimResource(t, c)
	sch := scimSchema(t, r)

	plan := scimWriteOnlyState("1")
	plan["endpoint"] = "https://scim.example.com/v3"
	config := map[string]any{
		"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v3",
		"token_wo": "token-in-the-configuration", "token_wo_version": "1",
	}
	resp := scimUpdate(t, r, sch, scimWriteOnlyState("1"), plan, config)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	mutations := fake.mutations()
	require.Len(t, mutations, 1)
	assert.Equal(t, http.MethodPut, mutations[0].Method)
	assert.Equal(t, "token-held-by-the-server", mutations[0].Body["token"], "the update must not erase or replace the token")
	assert.Equal(t, "https://scim.example.com/v3", mutations[0].Body["endpoint"])
	assert.Equal(t, "token-held-by-the-server", fake.token)
	assert.Equal(t, types.StringNull(), scimStateString(t, resp.State, "token"), "the state never holds the server's token")
	assert.Equal(t, types.StringNull(), scimStateString(t, resp.State, "token_wo"))
}

func TestScimServiceProviderResource_UpdateWithAVersionBumpSendsTheNewToken(t *testing.T) {
	fake, c := newScimFake(t)
	fake.token = "old-token"
	r := scimResource(t, c)
	sch := scimSchema(t, r)

	plan := scimWriteOnlyState("2")
	config := map[string]any{
		"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v2",
		"token_wo": "new-token", "token_wo_version": "2",
	}
	resp := scimUpdate(t, r, sch, scimWriteOnlyState("1"), plan, config)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	mutations := fake.mutations()
	require.Len(t, mutations, 1)
	assert.Equal(t, "new-token", mutations[0].Body["token"])
	assert.Equal(t, "new-token", fake.token)
	assert.Equal(t, types.StringValue("2"), scimStateString(t, resp.State, "token_wo_version"))
	assert.Equal(t, types.StringNull(), scimStateString(t, resp.State, "token"))
}

// Switching from the plain attribute to the write-only one sends the new
// value even though the state has no earlier version to compare.
func TestScimServiceProviderResource_UpdateFromPlainToWriteOnlySendsTheToken(t *testing.T) {
	fake, c := newScimFake(t)
	fake.token = "plain-token"
	r := scimResource(t, c)
	sch := scimSchema(t, r)

	plan := scimWriteOnlyState("1")
	config := map[string]any{
		"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v2",
		"token_wo": "write-only-token", "token_wo_version": "1",
	}
	resp := scimUpdate(t, r, sch, scimStoredState("plain-token"), plan, config)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	mutations := fake.mutations()
	require.Len(t, mutations, 1)
	assert.Equal(t, "write-only-token", mutations[0].Body["token"])
	assert.Equal(t, types.StringNull(), scimStateString(t, resp.State, "token"), "the plain token leaves the state")
}

// An update that cannot read the server's token must not send anything: a
// PUT without it would clear the token.
func TestScimServiceProviderResource_UpdateWithoutAVersionBumpFailsBeforeAnyPutWhenTheReadFails(t *testing.T) {
	fake, c := newScimFake(t)
	fake.token = "token-held-by-the-server"
	fake.failWith = &scimFailure{Status: http.StatusForbidden, Body: `{"error":"nope","code":"missing_permission"}`}
	r := scimResource(t, c)
	sch := scimSchema(t, r)

	config := map[string]any{
		"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v2",
		"token_wo": "token-in-the-configuration", "token_wo_version": "1",
	}
	plan := scimWriteOnlyState("1")
	plan["endpoint"] = "https://scim.example.com/v3"
	resp := scimUpdate(t, r, sch, scimWriteOnlyState("1"), plan, config)
	require.True(t, resp.Diagnostics.HasError())
	assert.Empty(t, fake.mutations(), "nothing may be sent")
	for _, d := range resp.Diagnostics {
		assert.NotContains(t, d.Summary()+d.Detail(), "token-held-by-the-server")
		assert.NotContains(t, d.Summary()+d.Detail(), "token-in-the-configuration")
	}
}

// With the plain attribute, an omitted token still clears the server's, as
// it always did: the plan shows that change as the removal of `token`.
func TestScimServiceProviderResource_UpdateWithoutAnyTokenClearsIt(t *testing.T) {
	fake, c := newScimFake(t)
	fake.token = "plain-token"
	r := scimResource(t, c)
	sch := scimSchema(t, r)

	plan := scimStoredState(nil)
	resp := scimUpdate(t, r, sch, scimStoredState("plain-token"), plan, map[string]any{
		"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v2",
	})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	mutations := fake.mutations()
	require.Len(t, mutations, 1)
	assert.NotContains(t, mutations[0].Body, "token")
	assert.Equal(t, "", fake.token)
	assert.Equal(t, types.StringNull(), scimStateString(t, resp.State, "token"))
}

// Read never stores the token the server returns once the write-only
// variant is in use, so the state cannot leak it and a plan cannot churn.
func TestScimServiceProviderResource_ReadDoesNotStoreTheTokenInWriteOnlyMode(t *testing.T) {
	fake, c := newScimFake(t)
	fake.token = "token-held-by-the-server"
	r := scimResource(t, c)
	sch := scimSchema(t, r)

	resp := scimRead(t, r, scimState(t, sch, scimWriteOnlyState("1")))
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, types.StringNull(), scimStateString(t, resp.State, "token"))
	assert.Equal(t, types.StringValue("1"), scimStateString(t, resp.State, "token_wo_version"))
}

// The keep-token path reads the token before an unrelated update. A read that
// succeeds but is not this provider with a present token must not become a PUT
// that clears the credential.
func TestScimServiceProviderResource_UpdateWithoutAVersionBumpRefusesAnUnusableRead(t *testing.T) {
	otherID := "99999999-9999-4999-8999-999999999999"
	cases := []struct {
		name string
		body string
	}{
		{"an empty object", `{}`},
		{"JSON null", `null`},
		{"a missing token", fmt.Sprintf(`{"id":%q,"endpoint":"https://scim.example.com/v2"}`, scimTestProviderID)},
		{"a null token", fmt.Sprintf(`{"id":%q,"token":null}`, scimTestProviderID)},
		{"a token of the wrong type", fmt.Sprintf(`{"id":%q,"token":5}`, scimTestProviderID)},
		{"a missing ID", `{"token":"other-token-value"}`},
		{"a null ID", `{"id":null,"token":"other-token-value"}`},
		{"another provider's ID", fmt.Sprintf(`{"id":%q,"token":"other-token-value"}`, otherID)},
		{"a string", `"other-token-value"`},
		{"a list", `[]`},
		{"not JSON", `<html>other-token-value</html>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, c := newScimFake(t)
			fake.token = "token-held-by-the-server"
			body := tc.body
			fake.getBody = &body
			r := scimResource(t, c)
			sch := scimSchema(t, r)

			plan := scimWriteOnlyState("1")
			plan["endpoint"] = "https://scim.example.com/v3"
			config := map[string]any{
				"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v3",
				"token_wo": "token-in-the-configuration", "token_wo_version": "1",
			}
			resp := scimUpdate(t, r, sch, scimWriteOnlyState("1"), plan, config)
			require.True(t, resp.Diagnostics.HasError())
			assert.Empty(t, fake.mutations(), "nothing may be sent: a PUT would clear the token")
			assert.Equal(t, "token-held-by-the-server", fake.token)
			for _, d := range resp.Diagnostics {
				text := d.Summary() + d.Detail()
				assert.NotContains(t, text, "other-token-value")
				assert.NotContains(t, text, "token-in-the-configuration")
				assert.NotContains(t, text, "token-held-by-the-server")
			}
			assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "was not this SCIM service provider with its token")
		})
	}
}

// An explicit empty token is a valid answer (the provider has none), and the
// provider ID may be written in either case.
func TestScimServiceProviderResource_UpdateWithoutAVersionBumpAcceptsAPresentToken(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantToken any
	}{
		{"an explicit empty token", fmt.Sprintf(`{"id":%q,"token":""}`, scimTestProviderID), nil},
		{"a token", fmt.Sprintf(`{"id":%q,"token":"kept-token"}`, scimTestProviderID), "kept-token"},
		{"an upper-case ID", fmt.Sprintf(`{"id":%q,"token":"kept-token"}`, strings.ToUpper(scimTestProviderID)), "kept-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, c := newScimFake(t)
			body := tc.body
			fake.getBody = &body
			r := scimResource(t, c)
			sch := scimSchema(t, r)

			plan := scimWriteOnlyState("1")
			plan["endpoint"] = "https://scim.example.com/v3"
			config := map[string]any{
				"client_id": scimTestClientID, "endpoint": "https://scim.example.com/v3",
				"token_wo": "token-in-the-configuration", "token_wo_version": "1",
			}
			resp := scimUpdate(t, r, sch, scimWriteOnlyState("1"), plan, config)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			mutations := fake.mutations()
			require.Len(t, mutations, 1)
			assert.Equal(t, tc.wantToken, mutations[0].Body["token"])
		})
	}
}
