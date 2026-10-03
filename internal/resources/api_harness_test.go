package resources

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	providerschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// apiHarnessProvider serves the API resources, configured with a given
// client, so tests drive them through the framework's protocol server:
// planning (defaults, computed-value marking, plan modifiers, ModifyPlan)
// and private state behave as under Terraform.
type apiHarnessProvider struct{ api *client.Client }

func (p *apiHarnessProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pocketid"
}

func (p *apiHarnessProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = providerschema.Schema{}
}

func (p *apiHarnessProvider) Configure(_ context.Context, _ provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	resp.ResourceData = p.api
}

func (p *apiHarnessProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewAPIResource, NewAPIClientAccessResource}
}

func (p *apiHarnessProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}

type apiHarness struct {
	t      *testing.T
	server tfprotov6.ProviderServer
	types  map[string]tftypes.Type
}

func newAPIHarness(t *testing.T, api *client.Client) *apiHarness {
	t.Helper()
	ctx := context.Background()
	server := providerserver.NewProtocol6(&apiHarnessProvider{api: api})()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)
	providerType := schemas.Provider.ValueType()
	config, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, map[string]tftypes.Value{}))
	require.NoError(t, err)
	_, err = server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &config})
	require.NoError(t, err)
	types := map[string]tftypes.Type{}
	for name, s := range schemas.ResourceSchemas {
		types[name] = s.ValueType()
	}
	return &apiHarness{t: t, server: server, types: types}
}

// value converts a resource model to the object Terraform would send; nil
// gives the null object.
func apiHarnessValue[M any](t *testing.T, r resource.Resource, model *M) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	if model == nil {
		return tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)
	}
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, model).HasError())
	return plan.Raw
}

// decode reads an object the provider returned into a model.
func apiHarnessDecode[M any](t *testing.T, r resource.Resource, typ tftypes.Type, value *tfprotov6.DynamicValue) (M, bool) {
	t.Helper()
	ctx := context.Background()
	var out M
	raw, err := value.Unmarshal(typ)
	require.NoError(t, err)
	if raw.IsNull() {
		return out, false
	}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	plan := tfsdk.Plan{Schema: sr.Schema, Raw: raw}
	require.False(t, plan.Get(ctx, &out).HasError())
	return out, true
}

func (h *apiHarness) dynamic(typeName string, value tftypes.Value) *tfprotov6.DynamicValue {
	h.t.Helper()
	dv, err := tfprotov6.NewDynamicValue(h.types[typeName], value)
	require.NoError(h.t, err)
	return &dv
}

// plan runs PlanResourceChange. proposed is Terraform's proposed new state:
// the configuration, with the prior state's values for what the
// configuration leaves to the provider.
func (h *apiHarness) plan(typeName string, prior, config, proposed tftypes.Value, priorPrivate []byte) *tfprotov6.PlanResourceChangeResponse {
	h.t.Helper()
	resp, err := h.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         typeName,
		PriorState:       h.dynamic(typeName, prior),
		ProposedNewState: h.dynamic(typeName, proposed),
		Config:           h.dynamic(typeName, config),
		PriorPrivate:     priorPrivate,
	})
	require.NoError(h.t, err)
	return resp
}

// apply runs ApplyResourceChange.
func (h *apiHarness) apply(typeName string, prior, config tftypes.Value, planned *tfprotov6.DynamicValue, plannedPrivate []byte) *tfprotov6.ApplyResourceChangeResponse {
	h.t.Helper()
	resp, err := h.server.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       typeName,
		PriorState:     h.dynamic(typeName, prior),
		PlannedState:   planned,
		Config:         h.dynamic(typeName, config),
		PlannedPrivate: plannedPrivate,
	})
	require.NoError(h.t, err)
	return resp
}

// read runs ReadResource (a refresh) on a state with its private state.
func (h *apiHarness) read(typeName string, current tftypes.Value, private []byte) *tfprotov6.ReadResourceResponse {
	h.t.Helper()
	resp, err := h.server.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName:     typeName,
		CurrentState: h.dynamic(typeName, current),
		Private:      private,
	})
	require.NoError(h.t, err)
	return resp
}

// apiHarnessAssertApplied checks what Terraform enforces after an apply
// without errors: every value the plan knew is what the apply returned.
func apiHarnessAssertApplied(t *testing.T, typ tftypes.Type, planned, applied *tfprotov6.DynamicValue) {
	t.Helper()
	plannedValue, err := planned.Unmarshal(typ)
	require.NoError(t, err)
	appliedValue, err := applied.Unmarshal(typ)
	require.NoError(t, err)
	require.NoError(t, tftypes.Walk(plannedValue, func(p *tftypes.AttributePath, v tftypes.Value) (bool, error) {
		if !v.IsKnown() {
			return false, nil
		}
		if v.Type().Is(tftypes.Object{}) || v.Type().Is(tftypes.Map{}) {
			return true, nil
		}
		got, _, err := tftypes.WalkAttributePath(appliedValue, p)
		if err != nil {
			return false, fmt.Errorf("%s: planned but missing after apply", p)
		}
		if !v.Equal(got.(tftypes.Value)) {
			return false, fmt.Errorf("%s: planned %s, applied %s", p, v, got)
		}
		return false, nil
	}))
}

// apiHarnessErrors joins the error diagnostics of a protocol response.
func apiHarnessErrors(diags []*tfprotov6.Diagnostic) string {
	out := ""
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			out += d.Summary + ": " + d.Detail + "\n"
		}
	}
	return out
}
