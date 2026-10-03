package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	groupA = "aaaaaaaa-0000-4000-8000-00000000000a"
	groupB = "bbbbbbbb-0000-4000-8000-00000000000b"
	// groupMissing names no group; Pocket ID drops it silently.
	groupMissing = "dddddddd-0000-4000-8000-00000000000d"
)

func TestPlanGroupRestriction(t *testing.T) {
	restricted, open, null, unknown := types.BoolValue(true), types.BoolValue(false), types.BoolNull(), types.BoolUnknown()
	someGroups := stringSet(groupA)
	unknownElement := types.SetValueMust(types.StringType, []attr.Value{types.StringUnknown()})
	for name, tc := range map[string]struct {
		create     bool
		state      types.Bool // prior is_group_restricted
		configured types.Bool
		groups     types.Set
		want       types.Bool
	}{
		"create with groups":                 {true, null, null, someGroups, restricted},
		"create without groups":              {true, null, null, types.SetNull(types.StringType), open},
		"create with [] ":                    {true, null, null, stringSet(), open},
		"create, groups unknown":             {true, null, null, types.SetUnknown(types.StringType), unknown},
		"create, a group not known yet":      {true, null, null, unknownElement, restricted},
		"create, restricted to nobody":       {true, null, restricted, types.SetNull(types.StringType), restricted},
		"open client gets groups":            {false, open, null, someGroups, restricted},
		"restricted client loses its groups": {false, restricted, null, types.SetNull(types.StringType), restricted},
		"restricted client, groups unknown":  {false, restricted, null, types.SetUnknown(types.StringType), restricted},
		"open client stays open":             {false, open, null, types.SetNull(types.StringType), open},
		"open client, groups unknown":        {false, open, null, types.SetUnknown(types.StringType), unknown},
		"explicitly opened":                  {false, restricted, open, types.SetNull(types.StringType), open},
		"state before the attribute, groups": {false, null, null, someGroups, restricted},
		"state before the attribute, none":   {false, null, null, types.SetNull(types.StringType), unknown},
		"explicitly restricted, no groups":   {false, open, restricted, types.SetNull(types.StringType), restricted},
	} {
		t.Run(name, func(t *testing.T) {
			proposed := managedModel()
			proposed.Name = types.StringValue("renamed") // the resource changes
			proposed.AllowedUserGroups = tc.groups
			proposed.IsGroupRestricted = tc.configured
			if tc.configured.IsNull() {
				proposed.IsGroupRestricted = types.BoolUnknown() // computed, marked by the framework
			}
			config := configOf(proposed)
			config.IsGroupRestricted = tc.configured
			var prior *clientResourceModel
			if !tc.create {
				p := managedModel()
				p.IsGroupRestricted = tc.state
				prior = &p
			} else {
				proposed.ID, proposed.HasLogo = types.StringUnknown(), types.BoolUnknown()
				proposed.ClientSecret, proposed.ClientSecretID = types.StringUnknown(), types.StringUnknown()
			}
			resp, planned := runModifyPlan(t, prior, proposed, config)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, tc.want, planned.IsGroupRestricted)
			opened := !tc.create && tc.state.ValueBool() && tc.want == open
			assert.Equal(t, opened, resp.Diagnostics.WarningsCount() == 1, "a plan that opens a restricted client warns")
		})
	}
}

// With no other change, a plan keeps whatever state holds.
func TestPlanGroupRestrictionWithoutChange(t *testing.T) {
	prior := managedModel()
	prior.IsGroupRestricted = types.BoolNull() // state written before the attribute, unrefreshed
	prior.AllowedUserGroups = stringSet(groupA)
	resp, planned := runModifyPlan(t, &prior, prior, configOf(prior))
	require.False(t, resp.Diagnostics.HasError())
	assert.True(t, planned.IsGroupRestricted.IsNull())
}

func TestClientValidateConfigGroupRestriction(t *testing.T) {
	for name, tc := range map[string]struct {
		restricted types.Bool
		groups     types.Set
		errors     int
	}{
		"false with groups":         {types.BoolValue(false), stringSet(groupA), 1},
		"false with unknown groups": {types.BoolValue(false), types.SetValueMust(types.StringType, []attr.Value{types.StringUnknown()}), 1},
		"false with []":             {types.BoolValue(false), stringSet(), 0},
		"false without groups":      {types.BoolValue(false), types.SetNull(types.StringType), 0},
		"true without groups":       {types.BoolValue(true), types.SetNull(types.StringType), 0},
		"omitted with groups":       {types.BoolNull(), stringSet(groupA), 0},
	} {
		t.Run(name, func(t *testing.T) {
			config := configOf(lifecycleModel())
			config.ClientID, config.GenerateSecret = types.StringNull(), types.BoolNull()
			config.IsGroupRestricted, config.AllowedUserGroups = tc.restricted, tc.groups
			resp := validateClientConfig(t, config)
			assert.Equal(t, tc.errors, resp.Diagnostics.ErrorsCount(), "%v", resp.Diagnostics)
		})
	}
}

// groupFake is a 2.17 server whose client c1 three users have authorized:
// "member-a" in group A, "member-b" in group B and "outsider" in neither.
func groupFake(t *testing.T, restricted bool, allowed ...string) *fakePocketID {
	f := managedFake(t, "2.17.0")
	f.client.Restricted, f.client.Allowed = restricted, allowed
	f.groups = map[string]bool{groupA: true, groupB: true}
	f.members = map[string][]string{"member-a": {groupA}, "member-b": {groupB}, "outsider": nil}
	return f
}

// Restricting a client on Pocket ID 2.17 signs out exactly the users who are
// in none of the new allowed groups: the groups are written before the
// restriction is turned on. Turned on first, Pocket ID would sign out every
// user, including the members who are about to be allowed.
func TestClientUpdateRestrictsWithoutSigningOutAllowedUsers(t *testing.T) {
	fake := groupFake(t, false)
	r := &clientResource{client: fake.start()}
	prior := managedModel()
	planned := prior
	planned.AllowedUserGroups = stringSet(groupA)
	planned.IsGroupRestricted = types.BoolValue(true)

	resp, after := runUpdate(t, r, prior, planned, configOf(planned))
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"member-b", "outsider"}, fake.signedOut, "member-a keeps access and is not signed out")
	assert.Equal(t, []string{"PUT /api/oidc/clients/c1/allowed-user-groups", "PUT /api/oidc/clients/c1"}, fake.mutations())
	assert.True(t, fake.client.Restricted)
	assert.Equal(t, []string{groupA}, fake.client.Allowed)
	assert.True(t, after.IsGroupRestricted.ValueBool())
	assert.True(t, stringSet(groupA).Equal(after.AllowedUserGroups))
}

func TestClientUpdateGroupRestriction(t *testing.T) {
	for name, tc := range map[string]struct {
		restricted      bool
		allowed         []string
		priorGroups     types.Set
		priorRestrict   types.Bool
		plannedGroups   types.Set
		plannedRestrict types.Bool
		configRestrict  types.Bool
		wantMutations   []string
		wantRestricted  bool
		wantAllowed     []string
		wantSignedOut   []string
		wantErr         string
	}{
		"change groups of a restricted client": {
			restricted: true, allowed: []string{groupA}, priorGroups: stringSet(groupA), priorRestrict: types.BoolValue(true),
			plannedGroups: stringSet(groupB), plannedRestrict: types.BoolValue(true), configRestrict: types.BoolNull(),
			wantMutations:  []string{"PUT /api/oidc/clients/c1/allowed-user-groups", "PUT /api/oidc/clients/c1"},
			wantRestricted: true, wantAllowed: []string{groupB}, wantSignedOut: []string{"member-a", "outsider"},
		},
		"remove the groups: admits nobody": {
			restricted: true, allowed: []string{groupA}, priorGroups: stringSet(groupA), priorRestrict: types.BoolValue(true),
			plannedGroups: types.SetNull(types.StringType), plannedRestrict: types.BoolValue(true), configRestrict: types.BoolNull(),
			wantMutations:  []string{"PUT /api/oidc/clients/c1/allowed-user-groups", "PUT /api/oidc/clients/c1"},
			wantRestricted: true, wantSignedOut: []string{"member-a", "member-b", "outsider"},
		},
		"open explicitly": {
			restricted: true, allowed: []string{groupA}, priorGroups: stringSet(groupA), priorRestrict: types.BoolValue(true),
			plannedGroups: types.SetNull(types.StringType), plannedRestrict: types.BoolValue(false), configRestrict: types.BoolValue(false),
			wantMutations:  []string{"PUT /api/oidc/clients/c1"},
			wantRestricted: false,
		},
		"unrelated change keeps groups": {
			restricted: true, allowed: []string{groupA}, priorGroups: stringSet(groupA), priorRestrict: types.BoolValue(true),
			plannedGroups: stringSet(groupA), plannedRestrict: types.BoolValue(true), configRestrict: types.BoolNull(),
			wantMutations:  []string{"PUT /api/oidc/clients/c1"},
			wantRestricted: true, wantAllowed: []string{groupA},
		},
		// State written before is_group_restricted, applied without a
		// refresh: planning leaves it unknown (see TestPlanGroupRestriction)
		// and the server's current restriction decides, never opened. A
		// stale known false is covered through the framework by
		// TestClientUnrefreshedUpdateNeverOpensARestrictedClient.
		"unrefreshed old state, groups removed": {
			restricted: true, allowed: []string{groupA}, priorGroups: stringSet(groupA), priorRestrict: types.BoolNull(),
			plannedGroups: types.SetNull(types.StringType), plannedRestrict: types.BoolUnknown(), configRestrict: types.BoolNull(),
			wantMutations:  []string{"PUT /api/oidc/clients/c1/allowed-user-groups", "PUT /api/oidc/clients/c1"},
			wantRestricted: true, wantSignedOut: []string{"member-a", "member-b", "outsider"},
		},
		"an ID that names no group": {
			restricted: true, allowed: []string{groupA}, priorGroups: stringSet(groupA), priorRestrict: types.BoolValue(true),
			plannedGroups: stringSet(groupA, groupMissing), plannedRestrict: types.BoolValue(true), configRestrict: types.BoolNull(),
			wantMutations:  []string{"PUT /api/oidc/clients/c1/allowed-user-groups"},
			wantRestricted: true, wantAllowed: []string{groupA}, wantSignedOut: []string{"member-b", "outsider"},
			wantErr: groupMissing,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := groupFake(t, tc.restricted, tc.allowed...)
			r := &clientResource{client: fake.start()}
			prior := managedModel()
			prior.AllowedUserGroups, prior.IsGroupRestricted = tc.priorGroups, tc.priorRestrict
			planned := prior
			planned.Name = types.StringValue("renamed")
			planned.AllowedUserGroups, planned.IsGroupRestricted = tc.plannedGroups, tc.plannedRestrict
			config := configOf(planned)
			config.IsGroupRestricted = tc.configRestrict

			resp, after := runUpdate(t, r, prior, planned, config)
			assert.Equal(t, tc.wantMutations, fake.mutations())
			assert.Equal(t, tc.wantRestricted, fake.client.Restricted)
			assert.ElementsMatch(t, tc.wantAllowed, fake.client.Allowed)
			assert.Equal(t, tc.wantSignedOut, fake.signedOut)
			if tc.wantErr != "" {
				require.True(t, resp.Diagnostics.HasError())
				assert.Contains(t, resp.Diagnostics[0].Detail(), tc.wantErr)
				assert.True(t, groupSetFromServer(groupsFromIDs(fake.client.Allowed), types.SetNull(types.StringType)).Equal(after.AllowedUserGroups), "state records what the server kept")
				assert.Equal(t, "fixture", after.Name.ValueString(), "the client itself was not updated")
				return
			}
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, tc.wantRestricted, after.IsGroupRestricted.ValueBool())
			assert.Equal(t, tc.plannedGroups, after.AllowedUserGroups)
		})
	}
}

// A refused combination at apply time (groups only known then) changes nothing.
func TestClientUpdateOpenWithGroupsRefused(t *testing.T) {
	fake := groupFake(t, true, groupA)
	r := &clientResource{client: fake.start()}
	prior := managedModel()
	prior.AllowedUserGroups, prior.IsGroupRestricted = stringSet(groupA), types.BoolValue(true)
	planned := prior
	planned.IsGroupRestricted = types.BoolValue(false)
	resp, _ := runUpdate(t, r, prior, planned, configOf(planned))
	require.True(t, resp.Diagnostics.HasError())
	assert.Empty(t, fake.mutations())
}

// A new client is created with its restriction in place, then given its
// groups; an ID that names no group rolls the new client back.
func TestClientCreateGroupRestriction(t *testing.T) {
	for name, tc := range map[string]struct {
		groups         types.Set
		restricted     types.Bool
		wantRestricted bool
		wantErr        bool
	}{
		"groups":                    {stringSet(groupA), types.BoolValue(true), true, false},
		"nobody":                    {types.SetNull(types.StringType), types.BoolValue(true), true, false},
		"open":                      {types.SetNull(types.StringType), types.BoolValue(false), false, false},
		"missing group rolled back": {stringSet(groupA, groupMissing), types.BoolValue(true), true, true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakePocketID(t, "2.17.0", &fakeClient{ID: "c1"})
			fake.groups = map[string]bool{groupA: true}
			r := &clientResource{client: fake.start()}
			ctx := context.Background()
			s := clientSchema(t).Schema
			model := lifecycleModel()
			model.AllowedUserGroups, model.IsGroupRestricted = tc.groups, tc.restricted
			plan := tfsdk.Plan{Schema: s}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			require.NotEmpty(t, fake.puts)
			assert.Equal(t, tc.wantRestricted, fake.puts[0]["isGroupRestricted"], "the restriction is part of the create")
			if tc.wantErr {
				require.True(t, resp.Diagnostics.HasError())
				assert.Contains(t, resp.Diagnostics[len(resp.Diagnostics)-1].Detail(), groupMissing)
				assert.Equal(t, 1, fake.called("DELETE /api/oidc/clients/c1"), "rolled back")
				assert.True(t, resp.State.Raw.IsNull())
				return
			}
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			var after clientResourceModel
			require.False(t, resp.State.Get(ctx, &after).HasError())
			assert.Equal(t, tc.wantRestricted, after.IsGroupRestricted.ValueBool())
			assert.Equal(t, setStrings(tc.groups), append([]string{}, fake.client.Allowed...))
		})
	}
}

func TestClientReadGroupRestriction(t *testing.T) {
	fake := groupFake(t, true)
	prior := managedModel()
	prior.IsGroupRestricted = types.BoolNull()
	resp, after := runRead(t, &clientResource{client: fake.start()}, prior)
	require.False(t, resp.Diagnostics.HasError())
	assert.Equal(t, types.BoolValue(true), after.IsGroupRestricted)
	assert.True(t, after.AllowedUserGroups.IsNull(), "restricted to nobody")
}

// When the server reports a different restriction after the update, the
// apply stops: state records what the server reports, and a planned secret
// generation is left for the next plan.
func TestClientUpdateRestrictionNotApplied(t *testing.T) {
	fake := groupFake(t, false)
	fake.client.IsPublic, fake.secrets = true, nil
	r := &clientResource{client: fake.start()}
	// A server that ignores the restriction.
	fake.ignoreRestriction = true
	prior := managedModel()
	prior.IsPublic = types.BoolValue(true)
	prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
	planned := prior
	planned.IsPublic = types.BoolValue(false)
	planned.IsGroupRestricted = types.BoolValue(true)
	planned.ClientSecret, planned.ClientSecretID = types.StringUnknown(), types.StringUnknown()
	resp, after := runUpdate(t, r, prior, planned, configOf(planned))
	require.True(t, resp.Diagnostics.HasError())
	assert.Zero(t, fake.called("POST /api/oidc/clients/c1/secrets"), "no further change")
	assert.False(t, after.IsGroupRestricted.ValueBool(), "state records what the server reports")
	assert.False(t, after.GenerateSecret.ValueBool(), "the next plan generates the secret")
	next := after
	next.GenerateSecret = types.BoolValue(true)
	action, _ := planSecretAction(after, next, false)
	assert.Equal(t, secretGenerate, action)
}

// A client restricted outside Terraform after the last refresh is never
// opened by an unrefreshed update that leaves is_group_restricted unset:
// planned from stale state, the restriction would read as false. The apply
// refuses before any change and asks for a refreshed plan; with one, the
// client stays restricted. Driven through the framework's planning.
func TestClientUnrefreshedUpdateNeverOpensARestrictedClient(t *testing.T) {
	fake := groupFake(t, false)
	h := newProtoHarness(t, fake.start())
	prior := managedModel()       // last refresh: open, no groups
	fake.client.Restricted = true // restricted in the admin UI since then
	config := homelabConfig()
	config.Name = types.StringValue("renamed")

	stale := h.plan(&prior, config, nil) // -refresh=false
	require.Empty(t, stale.errors)
	result := h.apply(&prior, config, stale)
	require.Contains(t, result.errors, "refresh")
	assert.Empty(t, fake.mutations(), "nothing changed")
	assert.True(t, fake.client.Restricted)
	assert.Equal(t, "fixture", fake.client.Name)

	refreshed := h.read(prior, nil)
	require.Empty(t, refreshed.errors)
	p := h.plan(refreshed.model, config, refreshed.private)
	require.Empty(t, p.errors)
	assert.True(t, p.model.IsGroupRestricted.ValueBool())
	result = h.apply(refreshed.model, config, p)
	require.Empty(t, result.errors)
	assert.True(t, fake.client.Restricted, "still restricted")
	assert.Equal(t, "renamed", fake.client.Name)
	assert.Empty(t, fake.signedOut)
}
