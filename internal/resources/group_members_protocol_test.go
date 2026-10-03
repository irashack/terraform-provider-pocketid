package resources_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	providerschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/resources"
)

// gmHarnessProvider serves pocketid_group_members, configured with a client, so
// that tests drive it through the framework's protocol server the way Terraform
// does: planning (computed values marked unknown, ModifyPlan), private state
// and the tainted-replacement flow behave as under Terraform.
type gmHarnessProvider struct{ api *client.Client }

func (p *gmHarnessProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pocketid"
}

func (p *gmHarnessProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = providerschema.Schema{}
}

func (p *gmHarnessProvider) Configure(_ context.Context, _ provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	resp.ResourceData = p.api
}

func (p *gmHarnessProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{resources.NewGroupMembersResource}
}

func (p *gmHarnessProvider) DataSources(context.Context) []func() datasource.DataSource { return nil }

const gmTypeName = "pocketid_group_members"

type gmHarness struct {
	t      *testing.T
	server tfprotov6.ProviderServer
	typ    tftypes.Type
}

func newGMHarness(t *testing.T, api *client.Client) *gmHarness {
	t.Helper()
	ctx := context.Background()
	server := providerserver.NewProtocol6(&gmHarnessProvider{api: api})()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)
	providerType := schemas.Provider.ValueType()
	config, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, map[string]tftypes.Value{}))
	require.NoError(t, err)
	_, err = server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &config})
	require.NoError(t, err)
	return &gmHarness{t: t, server: server, typ: schemas.ResourceSchemas[gmTypeName].ValueType()}
}

func (h *gmHarness) set(ids []string) tftypes.Value {
	elements := make([]tftypes.Value, 0, len(ids))
	for _, id := range ids {
		elements = append(elements, tftypes.NewValue(tftypes.String, id))
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elements)
}

// object builds the resource's value. An empty id and a nil unresolved list are
// null.
func (h *gmHarness) object(id, groupID string, userIDs, unresolved []string) tftypes.Value {
	idValue := tftypes.NewValue(tftypes.String, nil)
	if id != "" {
		idValue = tftypes.NewValue(tftypes.String, id)
	}
	unresolvedValue := tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, nil)
	if unresolved != nil {
		unresolvedValue = h.set(unresolved)
	}
	return tftypes.NewValue(h.typ, map[string]tftypes.Value{
		"id": idValue, "group_id": tftypes.NewValue(tftypes.String, groupID),
		"user_ids": h.set(userIDs), "unresolved_user_ids": unresolvedValue,
	})
}

func (h *gmHarness) null() tftypes.Value { return tftypes.NewValue(h.typ, nil) }

func (h *gmHarness) dynamic(v tftypes.Value) *tfprotov6.DynamicValue {
	h.t.Helper()
	dv, err := tfprotov6.NewDynamicValue(h.typ, v)
	require.NoError(h.t, err)
	return &dv
}

func (h *gmHarness) decode(dv *tfprotov6.DynamicValue) tftypes.Value {
	h.t.Helper()
	v, err := dv.Unmarshal(h.typ)
	require.NoError(h.t, err)
	return v
}

// attr reads one attribute of an object value; it returns the sorted strings of
// a set and whether the attribute is null or unknown.
func (h *gmHarness) attr(v tftypes.Value, name string) (ids []string, null, unknown bool) {
	h.t.Helper()
	fields := map[string]tftypes.Value{}
	require.NoError(h.t, v.As(&fields))
	field := fields[name]
	if !field.IsKnown() {
		return nil, false, true
	}
	if field.IsNull() {
		return nil, true, false
	}
	var elements []tftypes.Value
	require.NoError(h.t, field.As(&elements))
	for _, e := range elements {
		var id string
		require.NoError(h.t, e.As(&id))
		ids = append(ids, id)
	}
	return ids, false, false
}

func gmDiagErrors(diags []*tfprotov6.Diagnostic) string {
	out := ""
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			out += d.Summary + ": " + d.Detail + "\n"
		}
	}
	return out
}

// plan runs PlanResourceChange. For a create the prior state is null and the
// proposed new state is the configuration.
func (h *gmHarness) plan(prior, config, proposed tftypes.Value, priorPrivate []byte) *tfprotov6.PlanResourceChangeResponse {
	h.t.Helper()
	resp, err := h.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: gmTypeName, PriorState: h.dynamic(prior), ProposedNewState: h.dynamic(proposed),
		Config: h.dynamic(config), PriorPrivate: priorPrivate,
	})
	require.NoError(h.t, err)
	return resp
}

func (h *gmHarness) apply(prior, config tftypes.Value, planned *tfprotov6.DynamicValue, plannedPrivate []byte) *tfprotov6.ApplyResourceChangeResponse {
	h.t.Helper()
	resp, err := h.server.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName: gmTypeName, PriorState: h.dynamic(prior), PlannedState: planned,
		Config: h.dynamic(config), PlannedPrivate: plannedPrivate,
	})
	require.NoError(h.t, err)
	return resp
}

func (h *gmHarness) read(current tftypes.Value, private []byte) *tfprotov6.ReadResourceResponse {
	h.t.Helper()
	resp, err := h.server.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName: gmTypeName, CurrentState: h.dynamic(current), Private: private,
	})
	require.NoError(h.t, err)
	return resp
}

// A create whose outcome is unknown, then the replacement Terraform plans for
// the tainted resource, driven through the protocol the way Terraform drives it:
// the replacement is planned from a null prior state with no prior private
// data, and its destroy half runs Delete with the replacement plan's private
// data (none) and the tainted resource's prior state. The user the unknown
// request granted late is removed, because the computed unresolved_user_ids is
// in that prior state.
func TestGroupMembersProtocol_FailedCreateThenReplacementRemovesTheLateGrant(t *testing.T) {
	s, c := newGMServer(t)
	s.putHeld = true // the request is answered 502 and committed later
	h := newGMHarness(t, c)
	group, user := s.groupID, gmUUID(101)
	config := h.object("", group, []string{user}, nil)

	// 1. Plan and apply the create. The apply fails and the group still shows
	// its old members.
	create := h.plan(h.null(), config, config, nil)
	require.Empty(t, gmDiagErrors(create.Diagnostics))
	_, null, unknown := h.attr(h.decode(create.PlannedState), "unresolved_user_ids")
	assert.True(t, null && !unknown, "the plan says unresolved_user_ids stays null, not (known after apply)")
	applied := h.apply(h.null(), config, create.PlannedState, create.PlannedPrivate)
	require.NotEmpty(t, gmDiagErrors(applied.Diagnostics), "the create reports the unknown outcome")
	tainted := h.decode(applied.NewState)
	require.False(t, tainted.IsNull(), "the resource keeps its identity")
	candidates, null, _ := h.attr(tainted, "unresolved_user_ids")
	require.False(t, null)
	assert.Equal(t, []string{user}, candidates)
	members, _, _ := h.attr(tainted, "user_ids")
	assert.Empty(t, members, "the observed membership is recorded separately from the candidates")

	// 2. The request commits late.
	s.releasePut()
	require.Equal(t, []string{user}, s.memberSet())

	// 3. Terraform plans the tainted resource's replacement: null prior state,
	// no private data.
	replacement := h.plan(h.null(), config, config, nil)
	require.Empty(t, gmDiagErrors(replacement.Diagnostics), "a replacement is not refused")

	// 4. The destroy half: the tainted resource's prior state, a null planned
	// state and the replacement plan's private data. No refresh in between.
	destroyed := h.apply(tainted, h.null(), h.dynamic(h.null()), replacement.PlannedPrivate)
	require.Empty(t, gmDiagErrors(destroyed.Diagnostics))
	assert.Empty(t, s.memberSet(), "the user granted by the unknown request is removed")

	// 5. The create half then makes the configuration true.
	recreated := h.apply(h.null(), config, replacement.PlannedState, replacement.PlannedPrivate)
	require.Empty(t, gmDiagErrors(recreated.Diagnostics))
	assert.Equal(t, []string{user}, s.memberSet())
	_, null, _ = h.attr(h.decode(recreated.NewState), "unresolved_user_ids")
	assert.True(t, null, "a resource that was created normally has none")
}

// A destroy with the group unreadable stops with an error and the resource is
// kept: nothing is forgotten on the strength of the snapshot from before the
// write.
func TestGroupMembersProtocol_DestroyWithTheGroupUnreadableKeepsTheResource(t *testing.T) {
	s, c := newGMServer(t)
	s.putAppliesThenFails = 500
	s.failGetsAfterPut = true
	h := newGMHarness(t, c)
	config := h.object("", s.groupID, []string{gmUUID(101)}, nil)
	create := h.plan(h.null(), config, config, nil)
	applied := h.apply(h.null(), config, create.PlannedState, create.PlannedPrivate)
	require.NotEmpty(t, gmDiagErrors(applied.Diagnostics))
	tainted := h.decode(applied.NewState)

	destroyed := h.apply(tainted, h.null(), h.dynamic(h.null()), nil)
	require.NotEmpty(t, gmDiagErrors(destroyed.Diagnostics), "destroy cannot confirm the group and stops")
	require.NotNil(t, destroyed.NewState)
	kept := h.decode(destroyed.NewState)
	assert.False(t, kept.IsNull(), "the resource stays in state")
	assert.Equal(t, []string{gmUUID(101)}, s.memberSet())

	s.stopFailingGets()
	destroyed = h.apply(tainted, h.null(), h.dynamic(h.null()), nil)
	require.Empty(t, gmDiagErrors(destroyed.Diagnostics))
	assert.Empty(t, s.memberSet())
}

// While candidates are recorded, an ordinary plan is refused and says how to
// recover; a destroy plan is not, and a refresh clears the candidates.
func TestGroupMembersProtocol_PlansAreRefusedWhileUnresolvedAndARefreshClearsIt(t *testing.T) {
	s, c := newGMServer(t)
	h := newGMHarness(t, c)
	group, user := s.groupID, gmUUID(101)
	config := h.object("", group, []string{user}, nil)
	unresolved := h.object(group, group, nil, []string{user})
	proposed := h.object(group, group, []string{user}, []string{user}) // config merged with the prior state

	refused := h.plan(unresolved, config, proposed, nil)
	errs := gmDiagErrors(refused.Diagnostics)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs, "unresolved change")
	assert.Contains(t, errs, "-refresh=false", "names the refresh as the way out")
	assert.Contains(t, errs, "terraform state rm", "and state rm as the other")

	destroy := h.plan(unresolved, h.null(), h.null(), nil)
	assert.Empty(t, gmDiagErrors(destroy.Diagnostics), "a destroy plan is not refused: Delete reconciles")

	// The refresh reads the group (the request never committed) and clears the
	// candidates.
	refreshed := h.read(unresolved, nil)
	require.Empty(t, gmDiagErrors(refreshed.Diagnostics))
	state := h.decode(refreshed.NewState)
	_, null, _ := h.attr(state, "unresolved_user_ids")
	assert.True(t, null)
	members, _, _ := h.attr(state, "user_ids")
	assert.Empty(t, members)

	// And the same configuration now plans: the user is to be added.
	next := h.plan(state, config, h.object(group, group, []string{user}, nil), nil)
	require.Empty(t, gmDiagErrors(next.Diagnostics))
	added, _, _ := h.attr(h.decode(next.PlannedState), "user_ids")
	assert.Equal(t, []string{user}, added)
}

// State written before unresolved_user_ids existed has no such attribute. It
// reads as null, and an unchanged configuration plans empty.
func TestGroupMembersProtocol_StateFromTheEarlierSchemaPlansEmpty(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101))
	h := newGMHarness(t, c)
	group := s.groupID

	upgraded, err := h.server.UpgradeResourceState(context.Background(), &tfprotov6.UpgradeResourceStateRequest{
		TypeName: gmTypeName, Version: 0,
		RawState: &tfprotov6.RawState{JSON: []byte(`{"id":"` + group + `","group_id":"` + group + `","user_ids":["` + gmUUID(101) + `"]}`)},
	})
	require.NoError(t, err)
	require.Empty(t, gmDiagErrors(upgraded.Diagnostics))
	prior := h.decode(upgraded.UpgradedState)
	_, null, _ := h.attr(prior, "unresolved_user_ids")
	assert.True(t, null, "the new attribute is null in the old state")

	refreshed := h.read(prior, nil)
	require.Empty(t, gmDiagErrors(refreshed.Diagnostics))
	prior = h.decode(refreshed.NewState)

	config := h.object("", group, []string{gmUUID(101)}, nil)
	plan := h.plan(prior, config, h.object(group, group, []string{gmUUID(101)}, nil), nil)
	require.Empty(t, gmDiagErrors(plan.Diagnostics))
	assert.True(t, h.decode(plan.PlannedState).Equal(prior), "an unchanged configuration plans no change")

	// A change plans normally and keeps unresolved_user_ids known and null.
	changed := h.plan(prior, h.object("", group, []string{gmUUID(102)}, nil), h.object(group, group, []string{gmUUID(102)}, nil), nil)
	require.Empty(t, gmDiagErrors(changed.Diagnostics))
	_, null, unknown := h.attr(h.decode(changed.PlannedState), "unresolved_user_ids")
	assert.True(t, null && !unknown)
}

// The private-state marker covers a state that has lost the attribute: with the
// marker alone, a plan is refused and a refresh clears it.
func TestGroupMembersProtocol_PrivateMarkerAloneIsHonored(t *testing.T) {
	s, c := newGMServer(t)
	s.putHeld = true
	h := newGMHarness(t, c)
	group, user := s.groupID, gmUUID(101)
	config := h.object("", group, []string{user}, nil)
	create := h.plan(h.null(), config, config, nil)
	applied := h.apply(h.null(), config, create.PlannedState, create.PlannedPrivate)
	require.NotEmpty(t, gmDiagErrors(applied.Diagnostics))
	require.NotEmpty(t, applied.Private, "the unknown outcome is also kept in private state")
	assert.Contains(t, string(applied.Private), "unresolved_members")

	withoutAttribute := h.object(group, group, nil, nil)
	refused := h.plan(withoutAttribute, config, h.object(group, group, []string{user}, nil), applied.Private)
	assert.Contains(t, gmDiagErrors(refused.Diagnostics), "unresolved change")

	refreshed := h.read(withoutAttribute, applied.Private)
	require.Empty(t, gmDiagErrors(refreshed.Diagnostics))
	assert.NotContains(t, string(refreshed.Private), "unresolved_members", "a refresh that read the group clears the marker")
	allowed := h.plan(h.decode(refreshed.NewState), config, h.object(group, group, []string{user}, nil), refreshed.Private)
	assert.Empty(t, gmDiagErrors(allowed.Diagnostics))
}
