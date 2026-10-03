package resources

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	providerschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// usersGroupsHarnessProvider serves only pocketid_user, configured with a
// given API client, so tests drive the resource through the framework's
// protocol server, private state included.
type usersGroupsHarnessProvider struct{ api *client.Client }

func (p *usersGroupsHarnessProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pocketid"
}

func (p *usersGroupsHarnessProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = providerschema.Schema{}
}

func (p *usersGroupsHarnessProvider) Configure(_ context.Context, _ provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	resp.ResourceData = p.api
}

func (p *usersGroupsHarnessProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewUserResource}
}

func (p *usersGroupsHarnessProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}

type usersGroupsUserHarness struct {
	t      *testing.T
	server tfprotov6.ProviderServer
	typ    tftypes.Type
}

func newUsersGroupsUserHarness(t *testing.T, api *client.Client) *usersGroupsUserHarness {
	t.Helper()
	ctx := context.Background()
	server := providerserver.NewProtocol6(&usersGroupsHarnessProvider{api: api})()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)
	providerType := schemas.Provider.ValueType()
	config, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, map[string]tftypes.Value{}))
	require.NoError(t, err)
	_, err = server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &config})
	require.NoError(t, err)
	return &usersGroupsUserHarness{t: t, server: server, typ: schemas.ResourceSchemas["pocketid_user"].ValueType()}
}

func (h *usersGroupsUserHarness) dynamic(m *userResourceModel) *tfprotov6.DynamicValue {
	h.t.Helper()
	value := tftypes.NewValue(h.typ, nil)
	if m != nil {
		sr := resource.SchemaResponse{}
		(&userResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
		plan := tfsdk.Plan{Schema: sr.Schema}
		require.False(h.t, plan.Set(context.Background(), m).HasError())
		value = plan.Raw
	}
	dv, err := tfprotov6.NewDynamicValue(h.typ, value)
	require.NoError(h.t, err)
	return &dv
}

func (h *usersGroupsUserHarness) decode(dv *tfprotov6.DynamicValue) *userResourceModel {
	h.t.Helper()
	if dv == nil {
		return nil
	}
	v, err := dv.Unmarshal(h.typ)
	require.NoError(h.t, err)
	if v.IsNull() {
		return nil
	}
	sr := resource.SchemaResponse{}
	(&userResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
	var m userResourceModel
	require.False(h.t, (&tfsdk.State{Schema: sr.Schema, Raw: v}).Get(context.Background(), &m).HasError())
	return &m
}

// usersGroupsErrors joins the error diagnostics.
func usersGroupsErrors(diags []*tfprotov6.Diagnostic) string {
	var out []string
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			out = append(out, d.Summary+": "+d.Detail)
		}
	}
	return strings.Join(out, "\n")
}

// configOf is the configuration behind a planned model: Computed attributes
// the configuration does not set are null in it.
func configOf(planned *userResourceModel) *userResourceModel {
	if planned == nil {
		return nil
	}
	c := *planned
	c.DisplayName = types.StringNull()
	c.UnresolvedCreation = types.BoolNull()
	return &c
}

// apply sends ApplyResourceChange; a nil planned model is a delete.
func (h *usersGroupsUserHarness) apply(prior, planned *userResourceModel, private []byte) (*userResourceModel, []byte, string) {
	h.t.Helper()
	config := configOf(planned)
	resp, err := h.server.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName: "pocketid_user", PriorState: h.dynamic(prior), PlannedState: h.dynamic(planned),
		Config: h.dynamic(config), PlannedPrivate: private,
	})
	require.NoError(h.t, err)
	return h.decode(resp.NewState), resp.Private, usersGroupsErrors(resp.Diagnostics)
}

// plan sends PlanResourceChange with prior state and private state; a nil
// proposed model plans a destroy.
func (h *usersGroupsUserHarness) plan(prior, proposed *userResourceModel, private []byte) string {
	h.t.Helper()
	resp, err := h.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: "pocketid_user", PriorState: h.dynamic(prior), ProposedNewState: h.dynamic(proposed),
		Config: h.dynamic(proposed), PriorPrivate: private,
	})
	require.NoError(h.t, err)
	return usersGroupsErrors(resp.Diagnostics)
}

func (h *usersGroupsUserHarness) read(current *userResourceModel, private []byte) (*userResourceModel, []byte, string) {
	h.t.Helper()
	resp, err := h.server.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName: "pocketid_user", CurrentState: h.dynamic(current), Private: private,
	})
	require.NoError(h.t, err)
	return h.decode(resp.NewState), resp.Private, usersGroupsErrors(resp.Diagnostics)
}

// planFromNull sends PlanResourceChange for a resource that has no prior
// state, which is how Terraform plans a tainted resource's replacement (and a
// new resource): null prior state, no prior private state. It returns the
// planned state and the planned private state the engine would carry into the
// apply.
func (h *usersGroupsUserHarness) planFromNull(config *userResourceModel) (*userResourceModel, []byte, string) {
	h.t.Helper()
	resp, err := h.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: "pocketid_user", PriorState: h.dynamic(nil), ProposedNewState: h.dynamic(configOf(config)), Config: h.dynamic(configOf(config)),
	})
	require.NoError(h.t, err)
	return h.decode(resp.PlannedState), resp.PlannedPrivate, usersGroupsErrors(resp.Diagnostics)
}

// importID imports the user and refreshes it, as terraform import does, and
// returns the resulting state and private state.
func (h *usersGroupsUserHarness) importID(id string) (*userResourceModel, []byte) {
	h.t.Helper()
	resp, err := h.server.ImportResourceState(context.Background(), &tfprotov6.ImportResourceStateRequest{TypeName: "pocketid_user", ID: id})
	require.NoError(h.t, err)
	require.Empty(h.t, usersGroupsErrors(resp.Diagnostics))
	require.Len(h.t, resp.ImportedResources, 1)
	read, err := h.server.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName: "pocketid_user", CurrentState: resp.ImportedResources[0].State, Private: resp.ImportedResources[0].Private,
	})
	require.NoError(h.t, err)
	require.Empty(h.t, usersGroupsErrors(read.Diagnostics))
	return h.decode(read.NewState), read.Private
}

func fixedIDPlanModel() *userResourceModel {
	m := defaultsPlanModel(types.SetNull(types.StringType), types.MapNull(types.StringType))
	m.ID = types.StringValue(fixedUserID)
	m.UnresolvedCreation = types.BoolUnknown()
	return &m
}

// A create with a chosen ID that fails without a definite answer, while a
// user with that ID turns out to exist (possibly someone else's, created
// between the check and the create), keeps the ID only as an unresolved
// creation: the provider then refuses to plan a destroy, to delete (also as
// part of a replacement) and to update it, and sends no request that would.
// Importing clears the condition.
func TestUserUnresolvedCreationIsNeverDeleted(t *testing.T) {
	s := &fixedIDServer{version: "2.17.0", createStatus: http.StatusBadGateway, afterPostExisting: true}
	h := newUsersGroupsUserHarness(t, s.start(t))

	state, private, errs := h.apply(nil, fixedIDPlanModel(), nil)
	require.Contains(t, errs, "User creation result uncertain")
	require.NotNil(t, state, "the ID is kept in state")
	require.Equal(t, fixedUserID, state.ID.ValueString())
	require.True(t, state.UnresolvedCreation.ValueBool(), "state records the condition")
	require.Contains(t, string(private), userUnresolvedCreationKey)
	require.Equal(t, 1, s.posts, "the create is not repeated")

	// A refresh keeps both records while the user exists.
	state, private, errs = h.read(state, private)
	require.Empty(t, errs)
	require.True(t, state.UnresolvedCreation.ValueBool())
	require.Contains(t, string(private), userUnresolvedCreationKey)

	require.Contains(t, h.plan(state, nil, private), "User creation unresolved", "a destroy is refused at plan time")
	changed := *state
	changed.IsAdmin = types.BoolValue(true)
	require.Contains(t, h.plan(state, &changed, private), "User creation unresolved", "a change is refused at plan time")
	require.Empty(t, h.plan(state, state, private), "a plan that changes nothing is not refused")

	_, _, errs = h.apply(state, nil, private)
	require.Contains(t, errs, "User creation unresolved")
	_, _, errs = h.apply(state, &changed, private)
	require.Contains(t, errs, "User creation unresolved")
	require.Zero(t, s.deletes, "no DELETE is ever sent")
	require.Zero(t, s.puts, "no update is ever sent")

	// Import starts with fresh state; the imported user is then managed
	// normally.
	imported, importedPrivate := h.importID(fixedUserID)
	require.NotNil(t, imported)
	require.True(t, imported.UnresolvedCreation.IsNull(), "importing clears the condition")
	require.NotContains(t, string(importedPrivate), userUnresolvedCreationKey)
	_, _, errs = h.apply(imported, nil, importedPrivate)
	require.Empty(t, errs)
	require.Equal(t, 1, s.deletes)
}

// Terraform taints a resource whose create returned an error, and plans the
// tainted resource's replacement from a null prior state with no prior private
// state: the plan cannot see the condition, and the destroy half of the
// replacement is applied with the replacement plan's private state, which has
// no marker, but with the tainted object's own state. The condition must
// still stop the delete, or the provider could delete a user it never owned.
func TestUserUnresolvedCreationTaintedReplacementIsRefused(t *testing.T) {
	s := &fixedIDServer{version: "2.17.0", createStatus: http.StatusBadGateway, afterPostExisting: true}
	h := newUsersGroupsUserHarness(t, s.start(t))
	config := fixedIDPlanModel()

	// The apply that creates the resource: the create is uncertain, the state
	// is returned with the error, and Terraform taints it.
	planned, plannedPrivate, errs := h.planFromNull(config)
	require.Empty(t, errs)
	tainted, _, errs := h.apply(nil, planned, plannedPrivate)
	require.Contains(t, errs, "User creation result uncertain")
	require.NotNil(t, tainted)

	// The next run plans the replacement from nothing. Nothing in the plan
	// carries the marker.
	replacement, replacementPrivate, errs := h.planFromNull(config)
	require.Empty(t, errs, "the plan cannot see the tainted state")
	require.NotContains(t, string(replacementPrivate), userUnresolvedCreationKey)

	// The destroy half runs first, with the tainted state and the
	// replacement's private state.
	_, _, errs = h.apply(tainted, nil, replacementPrivate)
	require.Contains(t, errs, "User creation unresolved", "the tainted state is what stops the delete")
	require.Zero(t, s.deletes, "no DELETE is sent for a user this apply may not own")
	require.NotNil(t, replacement)
	require.Equal(t, 1, s.posts, "the replacement's create never runs")
}

// Either record alone is enough: the state attribute (what a tainted
// replacement carries) and the private marker (what an untainted state may
// carry).
func TestUserUnresolvedCreationEitherRecordRefuses(t *testing.T) {
	for name, keep := range map[string]func(state *userResourceModel, private []byte) (*userResourceModel, []byte){
		"state_attribute_only": func(state *userResourceModel, _ []byte) (*userResourceModel, []byte) { return state, nil },
		"private_marker_only": func(state *userResourceModel, private []byte) (*userResourceModel, []byte) {
			c := *state
			c.UnresolvedCreation = types.BoolNull()
			return &c, private
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := &fixedIDServer{version: "2.17.0", createStatus: http.StatusBadGateway, afterPostExisting: true}
			h := newUsersGroupsUserHarness(t, s.start(t))
			created, createdPrivate, errs := h.apply(nil, fixedIDPlanModel(), nil)
			require.Contains(t, errs, "User creation result uncertain")
			state, private := keep(created, createdPrivate)

			_, _, errs = h.apply(state, nil, private)
			require.Contains(t, errs, "User creation unresolved", "delete")
			changed := *state
			changed.IsAdmin = types.BoolValue(true)
			_, _, errs = h.apply(state, &changed, private)
			require.Contains(t, errs, "User creation unresolved", "update")
			require.Zero(t, s.deletes)
			require.Zero(t, s.puts)
		})
	}
	t.Run("neither_record_deletes", func(t *testing.T) {
		s := &fixedIDServer{version: "2.17.0", createStatus: http.StatusBadGateway, afterPostExisting: true}
		h := newUsersGroupsUserHarness(t, s.start(t))
		created, _, _ := h.apply(nil, fixedIDPlanModel(), nil)
		state := *created
		state.UnresolvedCreation = types.BoolNull()
		_, _, errs := h.apply(&state, nil, nil)
		require.Empty(t, errs)
		require.Equal(t, 1, s.deletes)
	})
}

// When whether a user with the chosen ID exists cannot be confirmed after an
// uncertain create, the ID is kept as an unresolved creation too.
func TestUserUnresolvedCreationUnconfirmedRead(t *testing.T) {
	s := &fixedIDServer{version: "2.17.0", createStatus: http.StatusBadGateway, afterPostReadStatus: http.StatusForbidden}
	h := newUsersGroupsUserHarness(t, s.start(t))
	state, private, errs := h.apply(nil, fixedIDPlanModel(), nil)
	require.Contains(t, errs, "could not be confirmed")
	require.NotNil(t, state)
	require.Equal(t, fixedUserID, state.ID.ValueString())
	require.True(t, state.UnresolvedCreation.ValueBool())
	require.Contains(t, string(private), userUnresolvedCreationKey)
	_, _, errs = h.apply(state, nil, private)
	require.Contains(t, errs, "User creation unresolved")
	require.Zero(t, s.deletes)
}

// A recovery read that finds no user does not settle an uncertain create: a
// proxy can give up before the server commits. Here the read comes before
// the commit; the ID is still kept as an unresolved creation, and once the
// create has committed the provider still refuses to delete the user.
func TestUserUnresolvedCreationReadBeforeCommit(t *testing.T) {
	s := &fixedIDServer{version: "2.17.0", createStatus: http.StatusGatewayTimeout}
	h := newUsersGroupsUserHarness(t, s.start(t))
	state, private, errs := h.apply(nil, fixedIDPlanModel(), nil)
	require.Contains(t, errs, "found no user with this ID yet")
	require.NotNil(t, state, "the ID is kept in state")
	require.True(t, state.UnresolvedCreation.ValueBool())
	require.Contains(t, string(private), userUnresolvedCreationKey)
	require.Equal(t, 2, s.gets, "the preflight and the recovery read both found no user")

	// The server's transaction commits after the recovery read.
	s.mu.Lock()
	s.existing = true
	s.mu.Unlock()
	state, private, errs = h.read(state, private)
	require.Empty(t, errs)
	require.NotNil(t, state)
	require.True(t, state.UnresolvedCreation.ValueBool())
	require.Contains(t, string(private), userUnresolvedCreationKey)
	_, _, errs = h.apply(state, nil, private)
	require.Contains(t, errs, "User creation unresolved")
	require.Zero(t, s.deletes)
}
