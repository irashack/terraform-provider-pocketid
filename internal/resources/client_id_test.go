package resources

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientIDValidator(t *testing.T) {
	for value, ok := range map[string]bool{
		"jellyfin": true, "a.b_c-D9": true, "ab": true, strings.Repeat("x", 128): true,
		"a": false, strings.Repeat("x", 129): false, "has space": false, "https://rp.example/cimd": false,
		"..": false, "a/b": false, "": false,
	} {
		resp := &validator.StringResponse{}
		clientIDValidator{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("client_id"), ConfigValue: types.StringValue(value)}, resp)
		assert.Equal(t, !ok, resp.Diagnostics.HasError(), "%q", value)
	}
}

func TestClientIDRequiresReplace(t *testing.T) {
	ctx := context.Background()
	s := clientSchema(t).Schema
	for name, tc := range map[string]struct {
		create          bool
		stateClientID   types.String
		config          types.String
		requiresReplace bool
	}{
		"omitted":                          {false, types.StringValue("c1"), types.StringNull(), false},
		"omitted, state before the fix":    {false, types.StringNull(), types.StringNull(), false},
		"same as the ID":                   {false, types.StringValue("c1"), types.StringValue("c1"), false},
		"same, state before the fix":       {false, types.StringNull(), types.StringValue("c1"), false},
		"different":                        {false, types.StringValue("c1"), types.StringValue("c2"), true},
		"state recorded an ignored rename": {false, types.StringValue("c2"), types.StringValue("c2"), true},
		"unknown until apply":              {false, types.StringValue("c1"), types.StringUnknown(), false},
		"create with a fixed ID":           {true, types.StringNull(), types.StringValue("c1"), false},
	} {
		t.Run(name, func(t *testing.T) {
			state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
			if !tc.create {
				prior := managedModel()
				prior.ClientID = tc.stateClientID
				require.False(t, state.Set(ctx, &prior).HasError())
			}
			plan := tfsdk.Plan{Schema: s}
			proposed := managedModel()
			require.False(t, plan.Set(ctx, &proposed).HasError())
			req := planmodifier.StringRequest{Path: path.Root("client_id"), ConfigValue: tc.config, PlanValue: tc.config, StateValue: tc.stateClientID, State: state, Plan: plan}
			resp := &planmodifier.StringResponse{PlanValue: tc.config}
			clientIDReplace{}.PlanModifyString(ctx, req, resp)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, tc.requiresReplace, resp.RequiresReplace)
		})
	}
}

// Read records the server's ID as client_id, also for state that had none or
// a value the server never applied; an update never sends an ID.
func TestClientIDFollowsTheServer(t *testing.T) {
	for name, stored := range map[string]types.String{"null": types.StringNull(), "stale": types.StringValue("renamed"), "same": types.StringValue("c1")} {
		t.Run(name, func(t *testing.T) {
			fake := managedFake(t, "2.16.0")
			r := &clientResource{client: fake.start()}
			prior := managedModel()
			prior.ClientID = stored
			resp, after := runRead(t, r, prior)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, "c1", after.ClientID.ValueString())
		})
	}
	fake := managedFake(t, "2.16.0")
	r := &clientResource{client: fake.start()}
	prior := managedModel()
	prior.ClientID = types.StringValue("c1")
	planned := prior
	planned.Name = types.StringValue("renamed")
	resp, after := runUpdate(t, r, prior, planned, configOf(planned))
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	require.Len(t, fake.puts, 1)
	_, sent := fake.puts[0]["id"]
	assert.False(t, sent, "an update never sends an ID")
	assert.Equal(t, "c1", after.ClientID.ValueString())
}

// State in which a 2.4.x provider recorded a client_id rename that Pocket ID
// ignored: without a refresh the replacement cannot be planned (the stored
// and configured values are equal), so the plan fails and asks for a
// refresh; refreshed, client_id reads as the real ID and the plan replaces
// the client.
func TestClientIDIgnoredRenameNeedsRefresh(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	h := newProtoHarness(t, fake.start())
	legacy := managedModel()
	legacy.ClientID = types.StringValue("renamed") // recorded; the server kept c1
	config := homelabConfig()
	config.ClientID = types.StringValue("renamed")

	stale := h.plan(&legacy, config, nil)
	assert.Contains(t, stale.errors, "refresh")

	refreshed := h.read(legacy, nil)
	require.Empty(t, refreshed.errors)
	assert.Equal(t, "c1", refreshed.model.ClientID.ValueString())
	p := h.plan(refreshed.model, config, refreshed.private)
	require.Empty(t, p.errors)
	assert.Equal(t, "renamed", p.model.ClientID.ValueString(), "the planned value differs from the prior one, so the replacement survives")
	require.Len(t, p.requiresReplace, 1)
	assert.Equal(t, "AttributeName(\"client_id\")", p.requiresReplace[0].String())

	// Configuring the real ID instead plans no change.
	config.ClientID = types.StringValue("c1")
	p = h.plan(refreshed.model, config, refreshed.private)
	assert.True(t, h.emptyPlan(*refreshed.model, p))
}
