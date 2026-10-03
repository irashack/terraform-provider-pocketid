package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	unresolvedMembershipUser  = "12121212-1212-4212-8212-121212121212"
	unresolvedMembershipGroup = "34343434-3434-4434-8434-343434343434"
	unresolvedMembershipOther = "56565656-5656-4656-8656-565656565656"
)

// pendingMembershipServer is one user whose group list a PUT replaces. While
// hold is set, a PUT is received but not applied and is answered 502, as when a
// proxy gives up on a request the server goes on to commit; release then
// applies it, the way the late commit lands.
type pendingMembershipServer struct {
	mu      sync.Mutex
	current []string
	hold    bool
	pending []string
	gone    bool
	// getStatus, when set, answers every GET with it; getBody, when set, is
	// the raw body of every GET's 200 answer.
	getStatus int
	getBody   string
	puts      int
}

func (s *pendingMembershipServer) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current, s.hold = s.pending, false
}

func (s *pendingMembershipServer) start(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if s.gone {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"User not found","code":"user_not_found"}`))
			return
		}
		user := func() {
			groups := []map[string]string{}
			for _, id := range s.current {
				groups = append(groups, map[string]string{"id": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": unresolvedMembershipUser, "username": "fixture", "userGroups": groups})
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/users/" + unresolvedMembershipUser:
			switch {
			case s.getStatus != 0:
				w.WriteHeader(s.getStatus)
			case s.getBody != "":
				_, _ = w.Write([]byte(s.getBody))
			default:
				user()
			}
		case "PUT /api/users/" + unresolvedMembershipUser + "/user-groups":
			s.puts++
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			if s.hold {
				s.pending = slices.Clone(req.UserGroupIDs)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			s.current = slices.Clone(req.UserGroupIDs)
			user()
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

// membershipHarness drives pocketid_group_membership through the framework's
// protocol server.
type membershipHarness struct {
	t      *testing.T
	server tfprotov6.ProviderServer
	typ    tftypes.Type
	schema resource.SchemaResponse
}

func newMembershipHarness(t *testing.T, api *client.Client) *membershipHarness {
	t.Helper()
	ctx := context.Background()
	server := providerserver.NewProtocol6(&usersGroupsHarnessProvider{api: api, resources: []func() resource.Resource{NewGroupMembershipResource}})()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)
	providerType := schemas.Provider.ValueType()
	config, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, map[string]tftypes.Value{}))
	require.NoError(t, err)
	_, err = server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &config})
	require.NoError(t, err)
	h := &membershipHarness{t: t, server: server, typ: schemas.ResourceSchemas["pocketid_group_membership"].ValueType()}
	(&groupMembershipResource{}).Schema(ctx, resource.SchemaRequest{}, &h.schema)
	return h
}

func (h *membershipHarness) dynamic(m *groupMembershipResourceModel) *tfprotov6.DynamicValue {
	h.t.Helper()
	value := tftypes.NewValue(h.typ, nil)
	if m != nil {
		plan := tfsdk.Plan{Schema: h.schema.Schema}
		require.False(h.t, plan.Set(context.Background(), m).HasError())
		value = plan.Raw
	}
	dv, err := tfprotov6.NewDynamicValue(h.typ, value)
	require.NoError(h.t, err)
	return &dv
}

func (h *membershipHarness) decode(dv *tfprotov6.DynamicValue) *groupMembershipResourceModel {
	h.t.Helper()
	if dv == nil {
		return nil
	}
	v, err := dv.Unmarshal(h.typ)
	require.NoError(h.t, err)
	if v.IsNull() {
		return nil
	}
	var m groupMembershipResourceModel
	require.False(h.t, (&tfsdk.State{Schema: h.schema.Schema, Raw: v}).Get(context.Background(), &m).HasError())
	return &m
}

// membershipConfig is the configuration of the pair.
func membershipConfig() *groupMembershipResourceModel {
	return &groupMembershipResourceModel{
		ID: types.StringNull(), GroupID: types.StringValue(unresolvedMembershipGroup), UserID: types.StringValue(unresolvedMembershipUser),
		UnresolvedCreation: types.BoolNull(),
	}
}

// planFromNull plans a resource that has no prior state, as Terraform does for
// a new resource and for the replacement of a tainted one.
func (h *membershipHarness) planFromNull() *groupMembershipResourceModel {
	h.t.Helper()
	resp, err := h.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: "pocketid_group_membership", PriorState: h.dynamic(nil), ProposedNewState: h.dynamic(membershipConfig()), Config: h.dynamic(membershipConfig()),
	})
	require.NoError(h.t, err)
	require.Empty(h.t, usersGroupsErrors(resp.Diagnostics))
	return h.decode(resp.PlannedState)
}

// apply sends ApplyResourceChange; a nil planned model is a delete. The private
// state is empty, as it is for a replacement of a tainted resource.
func (h *membershipHarness) apply(prior, planned *groupMembershipResourceModel) (*groupMembershipResourceModel, string) {
	h.t.Helper()
	config := planned
	if planned != nil {
		config = membershipConfig()
	}
	resp, err := h.server.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName: "pocketid_group_membership", PriorState: h.dynamic(prior), PlannedState: h.dynamic(planned), Config: h.dynamic(config),
	})
	require.NoError(h.t, err)
	return h.decode(resp.NewState), usersGroupsErrors(resp.Diagnostics)
}

func (h *membershipHarness) read(current *groupMembershipResourceModel) (*groupMembershipResourceModel, string, string) {
	h.t.Helper()
	resp, err := h.server.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{TypeName: "pocketid_group_membership", CurrentState: h.dynamic(current)})
	require.NoError(h.t, err)
	return h.decode(resp.NewState), usersGroupsErrors(resp.Diagnostics), usersGroupsWarnings(resp.Diagnostics)
}

// An addition whose PUT may still take effect is not forgotten by a refresh
// that sees the old group list: the pair stays in state with a warning, a
// removal that cannot see it is refused rather than recorded as done, and once
// the PUT has landed the next refresh sees the membership, clears the
// condition, and the membership can be removed.
func TestGroupMembershipPendingAdditionSurvivesRefreshBeforeItLands(t *testing.T) {
	s := &pendingMembershipServer{hold: true}
	h := newMembershipHarness(t, s.start(t))

	// The create: the PUT is answered 502 and not applied yet.
	state, errs := h.apply(nil, h.planFromNull())
	require.Contains(t, errs, "Group membership result uncertain")
	require.NotNil(t, state, "the pair is kept in state")
	require.True(t, state.UnresolvedCreation.ValueBool())
	require.Equal(t, 1, s.puts)

	// A refresh before the request lands sees the old group list.
	state, errs, warnings := h.read(state)
	require.Empty(t, errs)
	require.NotNil(t, state, "the unresolved membership is not dropped")
	require.True(t, state.UnresolvedCreation.ValueBool())
	require.Contains(t, warnings, "Group membership creation still unresolved")

	// Terraform's replacement of the tainted resource, or a destroy, cannot be
	// recorded as done while the request may still land.
	_, errs = h.apply(state, nil)
	require.Contains(t, errs, "Group membership creation unresolved")
	require.Equal(t, 1, s.puts, "no removal is written")

	// The pending PUT lands after the refresh.
	s.release()
	state, errs, warnings = h.read(state)
	require.Empty(t, errs)
	require.Empty(t, warnings)
	require.NotNil(t, state, "the membership that landed is tracked")
	require.True(t, state.UnresolvedCreation.IsNull(), "seeing the user in the group clears the condition")
	require.Equal(t, unresolvedMembershipGroup+"/"+unresolvedMembershipUser, state.ID.ValueString())

	// Removing it from the configuration now revokes it.
	_, errs = h.apply(state, nil)
	require.Empty(t, errs)
	require.Empty(t, s.current)
}

// Terraform plans the replacement of a tainted resource from nothing and
// applies its destroy half with the tainted state alone: that state is enough
// to refuse removing a membership that is not visible yet, and to remove one
// that is.
func TestGroupMembershipTaintedReplacementDestroy(t *testing.T) {
	s := &pendingMembershipServer{hold: true}
	h := newMembershipHarness(t, s.start(t))
	tainted, errs := h.apply(nil, h.planFromNull())
	require.Contains(t, errs, "Group membership result uncertain")
	require.NotNil(t, tainted)

	h.planFromNull() // the replacement plan has no prior state
	_, errs = h.apply(tainted, nil)
	require.Contains(t, errs, "Group membership creation unresolved")
	require.Equal(t, 1, s.puts)

	s.release()
	_, errs = h.apply(tainted, nil)
	require.Empty(t, errs, "a membership that landed is removed")
	require.Empty(t, s.current)
	require.Equal(t, 2, s.puts)
}

// While the creation is unresolved, a user Pocket ID no longer knows does not
// remove the pair either; an ordinary membership is removed when the user or
// the membership is confirmed gone.
func TestGroupMembershipReadAbsence(t *testing.T) {
	for name, tc := range map[string]struct {
		unresolved bool
		gone       bool
		current    []string
		wantKept   bool
	}{
		"unresolved_user_missing":    {unresolved: true, gone: true, wantKept: true},
		"unresolved_not_member":      {unresolved: true, current: []string{unresolvedMembershipOther}, wantKept: true},
		"ordinary_user_missing":      {gone: true},
		"ordinary_not_member":        {current: []string{unresolvedMembershipOther}},
		"ordinary_member":            {current: []string{unresolvedMembershipGroup}, wantKept: true},
		"unresolved_member_resolves": {unresolved: true, current: []string{unresolvedMembershipGroup}, wantKept: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := &pendingMembershipServer{gone: tc.gone, current: tc.current}
			h := newMembershipHarness(t, s.start(t))
			state := membershipConfig()
			state.ID = types.StringValue(unresolvedMembershipGroup + "/" + unresolvedMembershipUser)
			if tc.unresolved {
				state.UnresolvedCreation = types.BoolValue(true)
			}
			got, errs, warnings := h.read(state)
			require.Empty(t, errs)
			require.Equal(t, tc.wantKept, got != nil)
			if tc.unresolved && tc.wantKept && !slices.Contains(tc.current, unresolvedMembershipGroup) {
				require.Contains(t, warnings, "Group membership creation still unresolved")
				require.True(t, got.UnresolvedCreation.ValueBool())
			} else {
				require.Empty(t, warnings)
			}
			if got != nil && slices.Contains(tc.current, unresolvedMembershipGroup) {
				require.True(t, got.UnresolvedCreation.IsNull())
			}
		})
	}
	t.Run("unresolved_user_missing_delete_refused", func(t *testing.T) {
		s := &pendingMembershipServer{gone: true}
		h := newMembershipHarness(t, s.start(t))
		state := membershipConfig()
		state.ID, state.UnresolvedCreation = types.StringValue(unresolvedMembershipGroup+"/"+unresolvedMembershipUser), types.BoolValue(true)
		_, errs := h.apply(state, nil)
		require.Contains(t, errs, "Group membership creation unresolved")
	})
}

// A membership that was created normally has no unresolved condition, and an
// ordinary delete does not look for one.
func TestGroupMembershipOrdinaryCreateAndDelete(t *testing.T) {
	s := &pendingMembershipServer{}
	h := newMembershipHarness(t, s.start(t))
	state, errs := h.apply(nil, h.planFromNull())
	require.Empty(t, errs)
	require.NotNil(t, state)
	require.True(t, state.UnresolvedCreation.IsNull(), "a known null, never unknown, after the apply")
	require.Equal(t, []string{unresolvedMembershipGroup}, s.current)
	_, errs = h.apply(state, nil)
	require.Empty(t, errs)
	require.Empty(t, s.current)
}

// A failed read of the user's groups before the write (a refused request, a
// malformed answer) means no write was sent and nothing is pending: the error
// is an ordinary one, no pair is kept in state, and once the API answers
// again the next apply creates the membership with no manual reconciliation.
func TestGroupMembershipFailedPreflightLeavesNothingToReconcile(t *testing.T) {
	for name, fail := range map[string]func(*pendingMembershipServer){
		"forbidden":         func(s *pendingMembershipServer) { s.getStatus = http.StatusForbidden },
		"malformed_success": func(s *pendingMembershipServer) { s.getBody = `{}` },
		"other_users_answer": func(s *pendingMembershipServer) {
			s.getBody = `{"id":"` + unresolvedMembershipOther + `","userGroups":[]}`
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := &pendingMembershipServer{}
			h := newMembershipHarness(t, s.start(t))

			fail(s)
			state, errs := h.apply(nil, h.planFromNull())
			require.Contains(t, errs, "Error adding user to group")
			require.NotContains(t, errs, "uncertain")
			require.Nil(t, state, "no pair is kept: nothing was sent")
			require.Zero(t, s.puts, "no write was sent")

			// The API recovers; the same configuration applies normally.
			s.getStatus, s.getBody = 0, ""
			state, errs = h.apply(nil, h.planFromNull())
			require.Empty(t, errs)
			require.NotNil(t, state)
			require.True(t, state.UnresolvedCreation.IsNull(), "nothing is unresolved")
			require.Equal(t, 1, s.puts)
			require.Equal(t, []string{unresolvedMembershipGroup}, s.current)

			// And it is an ordinary resource: it refreshes and is removed.
			state, errs, warnings := h.read(state)
			require.Empty(t, errs)
			require.Empty(t, warnings)
			require.NotNil(t, state)
			_, errs = h.apply(state, nil)
			require.Empty(t, errs)
			require.Empty(t, s.current)
		})
	}
}
