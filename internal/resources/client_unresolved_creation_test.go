package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const unresolvedClientID = "chosen-app"

// unresolvedClientServer answers the absence check before a create with a
// chosen ID with "not found", answers the POST with createStatus (without
// creating anything), and from then on holds a client under that ID when
// existsAfter is set: another actor's, created in between.
type unresolvedClientServer struct {
	mu           sync.Mutex
	createStatus int
	existsAfter  bool
	posted       bool
	posts        int
	deletes      int
	puts         int
}

func (s *unresolvedClientServer) start(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		notFound := func() {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`)
		}
		switch {
		case r.URL.Path == "/api/version/current":
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/oidc/clients/"+unresolvedClientID:
			if !s.posted || !s.existsAfter {
				notFound()
				return
			}
			_, _ = fmt.Fprint(w, `{"id":"`+unresolvedClientID+`","name":"someone else's","callbackURLs":["https://other.invalid/cb"],"pkceEnabled":true,"isPublic":false,"isGroupRestricted":false,"allowedUserGroups":[],"credentials":{"secrets":[]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/oidc/clients/"+unresolvedClientID+"/secrets":
			_, _ = fmt.Fprint(w, `[]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/oidc/clients":
			s.posts++
			s.posted = true
			w.WriteHeader(s.createStatus)
		case r.Method == http.MethodDelete:
			s.deletes++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut:
			s.puts++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c
}

type clientProtocolHarness struct {
	t      *testing.T
	server tfprotov6.ProviderServer
	typ    tftypes.Type
}

func newClientProtocolHarness(t *testing.T, api *client.Client) *clientProtocolHarness {
	t.Helper()
	ctx := context.Background()
	server := providerserver.NewProtocol6(&usersGroupsHarnessProvider{api: api, resources: []func() resource.Resource{NewClientResource}})()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)
	providerType := schemas.Provider.ValueType()
	config, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, map[string]tftypes.Value{}))
	require.NoError(t, err)
	_, err = server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &config})
	require.NoError(t, err)
	return &clientProtocolHarness{t: t, server: server, typ: schemas.ResourceSchemas["pocketid_client"].ValueType()}
}

func (h *clientProtocolHarness) dynamic(m *clientResourceModel) *tfprotov6.DynamicValue {
	h.t.Helper()
	value := tftypes.NewValue(h.typ, nil)
	if m != nil {
		plan := tfsdk.Plan{Schema: clientSchema(h.t).Schema}
		require.False(h.t, plan.Set(context.Background(), m).HasError())
		value = plan.Raw
	}
	dv, err := tfprotov6.NewDynamicValue(h.typ, value)
	require.NoError(h.t, err)
	return &dv
}

func (h *clientProtocolHarness) decode(dv *tfprotov6.DynamicValue) *clientResourceModel {
	h.t.Helper()
	if dv == nil {
		return nil
	}
	v, err := dv.Unmarshal(h.typ)
	require.NoError(h.t, err)
	if v.IsNull() {
		return nil
	}
	var m clientResourceModel
	require.False(h.t, (&tfsdk.State{Schema: clientSchema(h.t).Schema, Raw: v}).Get(context.Background(), &m).HasError())
	return &m
}

func (h *clientProtocolHarness) apply(prior, planned, config *clientResourceModel, private []byte) (*clientResourceModel, []byte, string) {
	h.t.Helper()
	resp, err := h.server.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName: "pocketid_client", PriorState: h.dynamic(prior), PlannedState: h.dynamic(planned),
		Config: h.dynamic(config), PlannedPrivate: private,
	})
	require.NoError(h.t, err)
	return h.decode(resp.NewState), resp.Private, usersGroupsErrors(resp.Diagnostics)
}

func (h *clientProtocolHarness) plan(prior, proposed, config *clientResourceModel, private []byte) (*clientResourceModel, []byte, string) {
	h.t.Helper()
	resp, err := h.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: "pocketid_client", PriorState: h.dynamic(prior), ProposedNewState: h.dynamic(proposed),
		Config: h.dynamic(config), PriorPrivate: private,
	})
	require.NoError(h.t, err)
	return h.decode(resp.PlannedState), resp.PlannedPrivate, usersGroupsErrors(resp.Diagnostics)
}

func (h *clientProtocolHarness) read(current *clientResourceModel, private []byte) (*clientResourceModel, []byte, string, string) {
	h.t.Helper()
	resp, err := h.server.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName: "pocketid_client", CurrentState: h.dynamic(current), Private: private,
	})
	require.NoError(h.t, err)
	return h.decode(resp.NewState), resp.Private, usersGroupsErrors(resp.Diagnostics), usersGroupsWarnings(resp.Diagnostics)
}

// unresolvedClientConfig is a configuration with a chosen client_id; the
// computed attributes are null in it.
func unresolvedClientConfig() *clientResourceModel {
	m := lifecycleModel()
	m.ClientID = types.StringValue(unresolvedClientID)
	m = configOf(m)
	m.UnresolvedCreation = types.BoolNull()
	return &m
}

// A create with a chosen client_id that fails without a definite answer,
// while a client with that ID turns out to exist (possibly someone else's,
// created between the absence check and the create), keeps the ID only as an
// unresolved creation. A destroy or a change is refused at plan time; the
// replacement Terraform plans for the tainted resource from a null prior
// state cannot see that, so its destroy half is refused at apply time from
// the tainted prior state, with no DELETE sent. Importing clears it.
func TestClientUnresolvedCreationIsNeverDeleted(t *testing.T) {
	s := &unresolvedClientServer{createStatus: http.StatusBadGateway, existsAfter: true}
	h := newClientProtocolHarness(t, s.start(t))
	config := unresolvedClientConfig()

	planned, plannedPrivate, errs := h.plan(nil, config, config, nil)
	require.Empty(t, errs)
	state, private, errs := h.apply(nil, planned, config, plannedPrivate)
	require.Contains(t, errs, "OIDC client creation result uncertain")
	require.NotNil(t, state, "the chosen ID is kept in state")
	assert.Equal(t, unresolvedClientID, state.ID.ValueString())
	assert.True(t, state.UnresolvedCreation.ValueBool(), "state records the condition")
	assert.Contains(t, string(private), clientUnresolvedCreationKey)
	assert.NotEqual(t, "someone else's", state.Name.ValueString(), "the client found under the ID is not adopted")
	assert.Equal(t, 1, s.posts, "the create is not repeated")

	// A refresh keeps both records.
	state, private, errs, _ = h.read(state, private)
	require.Empty(t, errs)
	require.True(t, state.UnresolvedCreation.ValueBool())
	require.Contains(t, string(private), clientUnresolvedCreationKey)

	_, _, errs = h.plan(state, nil, nil, private)
	assert.Contains(t, errs, "OIDC client creation unresolved", "a destroy is refused at plan time")
	renamed := *config
	renamed.Name = types.StringValue("renamed")
	_, _, errs = h.plan(state, &renamed, &renamed, private)
	assert.Contains(t, errs, "OIDC client creation unresolved", "a change is refused at plan time")

	// The failed create left the resource tainted: Terraform plans its
	// replacement from a null prior state, which raises nothing, and then
	// destroys the tainted object with its state and no private state.
	_, _, errs = h.plan(nil, config, config, nil)
	require.Empty(t, errs)
	_, _, errs = h.apply(state, nil, nil, nil)
	assert.Contains(t, errs, "OIDC client creation unresolved", "the destroy half of the replacement is refused")
	assert.Zero(t, s.deletes, "no DELETE is sent")

	// An update applied from state (without the private marker) is refused.
	_, _, errs = h.apply(state, &renamed, &renamed, nil)
	assert.Contains(t, errs, "OIDC client creation unresolved")
	assert.Zero(t, s.puts, "no PUT is sent")

	// Importing the client starts from fresh state without the condition.
	imported, err := h.server.ImportResourceState(context.Background(), &tfprotov6.ImportResourceStateRequest{TypeName: "pocketid_client", ID: unresolvedClientID})
	require.NoError(t, err)
	require.Empty(t, usersGroupsErrors(imported.Diagnostics))
	refreshed, _, errs, _ := h.read(h.decode(imported.ImportedResources[0].State), imported.ImportedResources[0].Private)
	require.Empty(t, errs)
	assert.True(t, refreshed.UnresolvedCreation.IsNull())
}

// A refresh that finds no client while the creation is unresolved keeps the
// resource with a warning: the create may still commit.
func TestClientUnresolvedCreationRefreshBeforeCommit(t *testing.T) {
	s := &unresolvedClientServer{createStatus: http.StatusBadGateway}
	h := newClientProtocolHarness(t, s.start(t))
	config := unresolvedClientConfig()
	planned, plannedPrivate, _ := h.plan(nil, config, config, nil)
	state, private, errs := h.apply(nil, planned, config, plannedPrivate)
	require.Contains(t, errs, "OIDC client creation result uncertain")
	require.NotNil(t, state)

	refreshed, _, errs, warnings := h.read(state, private)
	require.Empty(t, errs)
	require.NotNil(t, refreshed, "the resource stays in state")
	assert.Contains(t, warnings, "OIDC client creation still unresolved")
	assert.True(t, refreshed.UnresolvedCreation.ValueBool())
}

// A create that was never sent (the absence check could not settle it), or
// that Pocket ID refused, records nothing to reconcile.
func TestClientChosenIDDefiniteFailuresRecordNothing(t *testing.T) {
	s := &unresolvedClientServer{createStatus: http.StatusBadRequest}
	h := newClientProtocolHarness(t, s.start(t))
	config := unresolvedClientConfig()
	planned, plannedPrivate, _ := h.plan(nil, config, config, nil)
	state, private, errs := h.apply(nil, planned, config, plannedPrivate)
	require.NotEmpty(t, errs)
	assert.Nil(t, state, "a refused create records nothing")
	assert.NotContains(t, string(private), clientUnresolvedCreationKey)
}

// clientState2_4_104 is a pocketid_client as provider 2.4.104 wrote it to
// state (schema version 0): the attributes of that release only, with
// allowed_user_groups as a list and no client_secret_id, generate_secret,
// computed client settings or unresolved_creation.
const clientState2_4_104 = `{
  "id": "c1",
  "name": "fixture",
  "client_id": null,
  "callback_urls": ["https://example.invalid/callback"],
  "logout_callback_urls": null,
  "backchannel_logout_url": null,
  "is_public": false,
  "pkce_enabled": true,
  "allowed_user_groups": [],
  "has_logo": false,
  "requires_reauthentication": false,
  "requires_pushed_authorization_requests": false,
  "launch_url": null,
  "federated_identities": null,
  "client_secret": "gen0synthetic-held-secret"
}`

// State written by 2.4.104 has no unresolved_creation: it decodes as null,
// stays null through a refresh, and a plan keeps it null.
func TestClientUnresolvedCreationNullForEarlierState(t *testing.T) {
	h := newClientProtocolHarness(t, managedFake(t, "2.17.0").start())
	upgraded, err := h.server.UpgradeResourceState(context.Background(), &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "pocketid_client", Version: 0, RawState: &tfprotov6.RawState{JSON: []byte(clientState2_4_104)},
	})
	require.NoError(t, err)
	require.Empty(t, usersGroupsErrors(upgraded.Diagnostics))
	prior := h.decode(upgraded.UpgradedState)
	require.NotNil(t, prior)
	assert.Equal(t, "c1", prior.ID.ValueString())
	assert.True(t, prior.UnresolvedCreation.IsNull(), "the attribute 2.4.104 did not have is null")

	refreshed, private, errs, warnings := h.read(prior, nil)
	require.Empty(t, errs)
	assert.Empty(t, warnings)
	require.NotNil(t, refreshed)
	assert.True(t, refreshed.UnresolvedCreation.IsNull(), "a refresh keeps it null")

	planned, _, errs := h.plan(refreshed, refreshed, refreshed, private)
	require.Empty(t, errs)
	require.NotNil(t, planned)
	assert.True(t, planned.UnresolvedCreation.IsNull(), "null stays null in the plan")
}
