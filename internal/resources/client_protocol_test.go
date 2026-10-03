package resources

import (
	"context"
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

// harnessProvider serves only pocketid_client, configured with a given API
// client, so tests can drive the resource through the real framework server:
// defaults, unknown marking, attribute plan modifiers, ModifyPlan,
// replacement and private state, as Terraform and OpenTofu would.
type harnessProvider struct{ api *client.Client }

func (p *harnessProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pocketid"
}

func (p *harnessProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = providerschema.Schema{}
}

func (p *harnessProvider) Configure(_ context.Context, _ provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	resp.ResourceData = p.api
}

func (p *harnessProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewClientResource}
}

func (p *harnessProvider) DataSources(context.Context) []func() datasource.DataSource { return nil }

// protoHarness drives pocketid_client over the provider protocol.
type protoHarness struct {
	t      *testing.T
	server tfprotov6.ProviderServer
	typ    tftypes.Type
}

func newProtoHarness(t *testing.T, api *client.Client) *protoHarness {
	t.Helper()
	ctx := context.Background()
	server := providerserver.NewProtocol6(&harnessProvider{api: api})()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)
	requireNoProtocolErrors(t, schemas.Diagnostics)
	typ := schemas.ResourceSchemas["pocketid_client"].ValueType()
	providerType := schemas.Provider.ValueType()
	config, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, map[string]tftypes.Value{}))
	require.NoError(t, err)
	configured, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &config})
	require.NoError(t, err)
	requireNoProtocolErrors(t, configured.Diagnostics)
	return &protoHarness{t: t, server: server, typ: typ}
}

func requireNoProtocolErrors(t *testing.T, diags []*tfprotov6.Diagnostic) {
	t.Helper()
	for _, d := range diags {
		require.NotEqual(t, tfprotov6.DiagnosticSeverityError, d.Severity, "%s: %s", d.Summary, d.Detail)
	}
}

// protocolErrors joins the error diagnostics' summaries and details.
func protocolErrors(diags []*tfprotov6.Diagnostic) string {
	var out []string
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			out = append(out, d.Summary+": "+d.Detail)
		}
	}
	return strings.Join(out, "\n")
}

// raw converts a model into the resource's object value.
func (h *protoHarness) raw(m *clientResourceModel) tftypes.Value {
	h.t.Helper()
	if m == nil {
		return tftypes.NewValue(h.typ, nil)
	}
	plan := tfsdk.Plan{Schema: clientSchema(h.t).Schema}
	require.False(h.t, plan.Set(context.Background(), m).HasError())
	return plan.Raw
}

func (h *protoHarness) dynamic(v tftypes.Value) *tfprotov6.DynamicValue {
	h.t.Helper()
	dv, err := tfprotov6.NewDynamicValue(h.typ, v)
	require.NoError(h.t, err)
	return &dv
}

// model decodes an object value; nil for a null object.
func (h *protoHarness) model(dv *tfprotov6.DynamicValue) *clientResourceModel {
	h.t.Helper()
	v, err := dv.Unmarshal(h.typ)
	require.NoError(h.t, err)
	if v.IsNull() {
		return nil
	}
	var m clientResourceModel
	state := tfsdk.State{Schema: clientSchema(h.t).Schema, Raw: v}
	require.False(h.t, state.Get(context.Background(), &m).HasError())
	return &m
}

// proposed builds the proposed new state as Terraform core does for these
// top-level attributes: the configured value, or for a computed attribute
// omitted from configuration, the prior value.
func (h *protoHarness) proposed(prior, config tftypes.Value) tftypes.Value {
	h.t.Helper()
	ctx := context.Background()
	s := clientSchema(h.t).Schema
	var configured, priorAttrs map[string]tftypes.Value
	require.NoError(h.t, config.As(&configured))
	if !prior.IsNull() {
		require.NoError(h.t, prior.As(&priorAttrs))
	}
	out := map[string]tftypes.Value{}
	for name, value := range configured {
		out[name] = value
		if value.IsNull() && priorAttrs != nil && s.Attributes[name].IsComputed() {
			out[name] = priorAttrs[name]
		}
	}
	_ = ctx
	return tftypes.NewValue(h.typ, out)
}

// planned is the result of a plan.
type planned struct {
	model           *clientResourceModel
	raw             tftypes.Value
	private         []byte
	requiresReplace []*tftypes.AttributePath
	errors          string
	warnings        int
}

// plan plans config against prior (nil: create) and prior private state.
func (h *protoHarness) plan(prior *clientResourceModel, config clientResourceModel, priorPrivate []byte) planned {
	h.t.Helper()
	priorRaw, configRaw := h.raw(prior), h.raw(&config)
	resp, err := h.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "pocketid_client",
		PriorState:       h.dynamic(priorRaw),
		ProposedNewState: h.dynamic(h.proposed(priorRaw, configRaw)),
		Config:           h.dynamic(configRaw),
		PriorPrivate:     priorPrivate,
	})
	require.NoError(h.t, err)
	out := planned{private: resp.PlannedPrivate, requiresReplace: resp.RequiresReplace, errors: protocolErrors(resp.Diagnostics)}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityWarning {
			out.warnings++
		}
	}
	if out.errors == "" {
		out.raw, _ = resp.PlannedState.Unmarshal(h.typ)
		out.model = h.model(resp.PlannedState)
	}
	return out
}

// applied is the result of an apply or a read.
type applied struct {
	model   *clientResourceModel
	private []byte
	errors  string
}

// apply applies a plan made by plan.
func (h *protoHarness) apply(prior *clientResourceModel, config clientResourceModel, p planned) applied {
	h.t.Helper()
	require.Empty(h.t, p.errors, "applying a plan that failed")
	resp, err := h.server.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       "pocketid_client",
		PriorState:     h.dynamic(h.raw(prior)),
		PlannedState:   h.dynamic(p.raw),
		Config:         h.dynamic(h.raw(&config)),
		PlannedPrivate: p.private,
	})
	require.NoError(h.t, err)
	out := applied{private: resp.Private, errors: protocolErrors(resp.Diagnostics)}
	if resp.NewState != nil {
		out.model = h.model(resp.NewState)
	}
	return out
}

// read refreshes state.
func (h *protoHarness) read(current clientResourceModel, private []byte) applied {
	h.t.Helper()
	resp, err := h.server.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName:     "pocketid_client",
		CurrentState: h.dynamic(h.raw(&current)),
		Private:      private,
	})
	require.NoError(h.t, err)
	out := applied{private: resp.Private, errors: protocolErrors(resp.Diagnostics)}
	if resp.NewState != nil {
		out.model = h.model(resp.NewState)
	}
	return out
}

// emptyPlan reports whether a plan changes nothing.
func (h *protoHarness) emptyPlan(prior clientResourceModel, p planned) bool {
	return p.errors == "" && p.raw.Equal(h.raw(&prior))
}

// homelabConfig is the configuration of managedModel: computed attributes
// and the "unmanaged unless set" ones omitted.
func homelabConfig() clientResourceModel {
	c := managedModel()
	c.ID, c.ClientID, c.HasLogo, c.LaunchURL = clientNullString(), clientNullString(), clientNullBool(), clientNullString()
	c.ClientSecret, c.ClientSecretID = clientNullString(), clientNullString()
	c.HasDarkLogo, c.ClientType, c.PkceSupported, c.IsGroupRestricted = clientNullBool(), clientNullString(), clientNullBool(), clientNullBool()
	c.Description, c.SkipConsent = clientNullString(), clientNullBool()
	c.AccessTokenDurationMinutes, c.RefreshTokenDurationMinutes = clientNullInt64(), clientNullInt64()
	c.GenerateSecret = clientNullBool()
	c.RequiresReauthentication, c.RequiresPushedAuthorizationRequests = clientNullBool(), clientNullBool()
	return c
}

// TestProtoHarnessUnchangedPlanIsEmpty checks the harness itself: a state
// that matches the server and its configuration plans no change.
func TestProtoHarnessUnchangedPlanIsEmpty(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	h := newProtoHarness(t, fake.start())
	prior := managedModel()
	refreshed := h.read(prior, nil)
	require.Empty(t, refreshed.errors)
	p := h.plan(refreshed.model, homelabConfig(), refreshed.private)
	require.Empty(t, p.errors)
	require.True(t, h.emptyPlan(*refreshed.model, p), "plan: %+v", p.model)
}

func clientNullString() types.String { return types.StringNull() }
func clientNullBool() types.Bool     { return types.BoolNull() }
func clientNullInt64() types.Int64   { return types.Int64Null() }
