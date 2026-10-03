package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Refresh records the settings the provider used to carry silently, and the
// computed client attributes, for state written before they existed.
func TestClientReadSettings(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	c := fake.client
	c.Description, c.SkipConsent, c.AccessMinutes, c.RefreshMinutes = "set outside", true, 120, 4320
	c.HasDarkLogo, c.PkceSupported = true, true
	prior := managedModel()
	prior.Description, prior.SkipConsent = types.StringNull(), types.BoolNull()
	prior.AccessTokenDurationMinutes, prior.RefreshTokenDurationMinutes = types.Int64Null(), types.Int64Null()
	prior.HasDarkLogo, prior.ClientType, prior.PkceSupported = types.BoolNull(), types.StringNull(), types.BoolNull()
	resp, after := runRead(t, &clientResource{client: fake.start()}, prior)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, "set outside", after.Description.ValueString())
	assert.True(t, after.SkipConsent.ValueBool())
	assert.Equal(t, int64(120), after.AccessTokenDurationMinutes.ValueInt64())
	assert.Equal(t, int64(4320), after.RefreshTokenDurationMinutes.ValueInt64())
	assert.True(t, after.HasDarkLogo.ValueBool())
	assert.Equal(t, "standard", after.ClientType.ValueString())
	assert.True(t, after.PkceSupported.ValueBool())
}

// Unset, the settings keep the server's current values through an update,
// also values changed since the last refresh; set, they are authoritative.
func TestClientUpdateSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		configured bool
		unknown    bool // planned values unknown: state from before the attributes, unrefreshed
		sent       map[string]any
	}{
		"unmanaged keeps the server's values": {sent: map[string]any{"description": "set outside", "skipConsent": true, "accessTokenDurationMinutes": float64(120), "refreshTokenDurationMinutes": float64(4320)}},
		"unmanaged, unrefreshed old state":    {unknown: true, sent: map[string]any{"description": "set outside", "skipConsent": true, "accessTokenDurationMinutes": float64(120), "refreshTokenDurationMinutes": float64(4320)}},
		"configured":                          {configured: true, sent: map[string]any{"description": "configured", "skipConsent": false, "accessTokenDurationMinutes": float64(5), "refreshTokenDurationMinutes": float64(10)}},
	} {
		t.Run(name, func(t *testing.T) {
			fake := managedFake(t, "2.17.0")
			c := fake.client
			c.Description, c.SkipConsent, c.AccessMinutes, c.RefreshMinutes = "set outside", true, 120, 4320
			r := &clientResource{client: fake.start()}
			prior := managedModel() // state from the last refresh: defaults
			planned := prior
			planned.Name = types.StringValue("renamed")
			if tc.unknown {
				prior.Description, prior.SkipConsent = types.StringNull(), types.BoolNull()
				prior.AccessTokenDurationMinutes, prior.RefreshTokenDurationMinutes = types.Int64Null(), types.Int64Null()
				planned.Description, planned.SkipConsent = types.StringUnknown(), types.BoolUnknown()
				planned.AccessTokenDurationMinutes, planned.RefreshTokenDurationMinutes = types.Int64Unknown(), types.Int64Unknown()
			}
			config := configOf(planned)
			config.Description, config.SkipConsent = types.StringNull(), types.BoolNull()
			config.AccessTokenDurationMinutes, config.RefreshTokenDurationMinutes = types.Int64Null(), types.Int64Null()
			if tc.configured {
				planned.Description, planned.SkipConsent = types.StringValue("configured"), types.BoolValue(false)
				planned.AccessTokenDurationMinutes, planned.RefreshTokenDurationMinutes = types.Int64Value(5), types.Int64Value(10)
				config.Description, config.SkipConsent = planned.Description, planned.SkipConsent
				config.AccessTokenDurationMinutes, config.RefreshTokenDurationMinutes = planned.AccessTokenDurationMinutes, planned.RefreshTokenDurationMinutes
			}
			resp, after := runUpdate(t, r, prior, planned, config)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			require.Len(t, fake.puts, 1)
			for key, want := range tc.sent {
				assert.Equal(t, want, fake.puts[0][key], key)
			}
			switch {
			case tc.configured:
				assert.Equal(t, "configured", after.Description.ValueString())
				assert.Equal(t, int64(5), after.AccessTokenDurationMinutes.ValueInt64())
			case tc.unknown:
				assert.Equal(t, "set outside", after.Description.ValueString(), "filled from the response")
				assert.Equal(t, int64(120), after.AccessTokenDurationMinutes.ValueInt64())
			default:
				assert.Equal(t, prior.Description, after.Description, "the plan is recorded; the next refresh shows the server's value")
			}
		})
	}
}

// A create leaves unset settings to Pocket ID's defaults and records them.
func TestClientCreateSettings(t *testing.T) {
	for name, configured := range map[string]bool{"defaults": false, "configured": true} {
		t.Run(name, func(t *testing.T) {
			fake := newFakePocketID(t, "2.16.0", &fakeClient{ID: "c1"})
			r := &clientResource{client: fake.start()}
			ctx := context.Background()
			s := clientSchema(t).Schema
			model := lifecycleModel()
			if configured {
				model.Description, model.SkipConsent = types.StringValue("configured"), types.BoolValue(true)
				model.AccessTokenDurationMinutes, model.RefreshTokenDurationMinutes = types.Int64Value(5), types.Int64Value(10)
			}
			plan := tfsdk.Plan{Schema: s}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			var after clientResourceModel
			require.False(t, resp.State.Get(ctx, &after).HasError())
			if configured {
				assert.Equal(t, "configured", fake.puts[0]["description"])
				assert.Equal(t, float64(5), fake.puts[0]["accessTokenDurationMinutes"])
				assert.Equal(t, "configured", after.Description.ValueString())
				assert.True(t, after.SkipConsent.ValueBool())
				return
			}
			_, sent := fake.puts[0]["accessTokenDurationMinutes"]
			assert.False(t, sent, "an unset lifetime is left to the server's default")
			assert.Equal(t, "", after.Description.ValueString())
			assert.Equal(t, int64(60), after.AccessTokenDurationMinutes.ValueInt64())
			assert.Equal(t, int64(43200), after.RefreshTokenDurationMinutes.ValueInt64())
			assert.Equal(t, "standard", after.ClientType.ValueString())
			assert.False(t, after.HasDarkLogo.ValueBool())
		})
	}
}

func TestPlanPkceSupported(t *testing.T) {
	for name, tc := range map[string]struct {
		state, want types.Bool
		pkce        types.Bool
		create      bool
	}{
		"pkce off resets it":   {types.BoolValue(true), types.BoolValue(false), types.BoolValue(false), false},
		"pkce on keeps it":     {types.BoolValue(true), types.BoolValue(true), types.BoolValue(true), false},
		"pkce unknown":         {types.BoolValue(true), types.BoolUnknown(), types.BoolUnknown(), false},
		"old state":            {types.BoolNull(), types.BoolUnknown(), types.BoolValue(true), false},
		"create stays unknown": {types.BoolNull(), types.BoolUnknown(), types.BoolValue(false), true},
	} {
		t.Run(name, func(t *testing.T) {
			plan := managedModel()
			plan.PkceEnabled, plan.PkceSupported = tc.pkce, types.BoolUnknown()
			var state *clientResourceModel
			if !tc.create {
				prior := managedModel()
				prior.PkceSupported = tc.state
				state = &prior
			}
			planPkceSupported(state, &plan)
			assert.Equal(t, tc.want, plan.PkceSupported)
		})
	}
}
