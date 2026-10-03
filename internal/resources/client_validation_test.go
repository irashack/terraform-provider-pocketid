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

// validateClientConfig runs ValidateConfig on a configuration.
func validateClientConfig(t *testing.T, config clientResourceModel) resource.ValidateConfigResponse {
	t.Helper()
	ctx := context.Background()
	s := clientSchema(t).Schema
	built := tfsdk.Plan{Schema: s}
	require.False(t, built.Set(ctx, &config).HasError())
	resp := resource.ValidateConfigResponse{}
	(&clientResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: built.Raw}}, &resp)
	return resp
}

// A public client must keep PKCE (Pocket ID forces it on); PAR is no longer
// refused for one.
func TestClientValidateConfigPublicClient(t *testing.T) {
	for name, tc := range map[string]struct {
		public, pkce, par types.Bool
		errors            int
	}{
		"public without pkce":       {types.BoolValue(true), types.BoolValue(false), types.BoolNull(), 1},
		"public, pkce default":      {types.BoolValue(true), types.BoolNull(), types.BoolNull(), 0},
		"public, pkce unknown":      {types.BoolValue(true), types.BoolUnknown(), types.BoolNull(), 0},
		"confidential without pkce": {types.BoolValue(false), types.BoolValue(false), types.BoolNull(), 0},
		"public with PAR":           {types.BoolValue(true), types.BoolValue(true), types.BoolValue(true), 0},
	} {
		t.Run(name, func(t *testing.T) {
			config := configOf(lifecycleModel())
			config.ClientID, config.GenerateSecret = types.StringNull(), types.BoolNull()
			config.IsPublic, config.PkceEnabled, config.RequiresPushedAuthorizationRequests = tc.public, tc.pkce, tc.par
			resp := validateClientConfig(t, config)
			assert.Equal(t, tc.errors, resp.Diagnostics.ErrorsCount(), "%v", resp.Diagnostics)
		})
	}
}

// A public client requiring PAR is refused before any mutation on a server
// that would drop the setting (2.9.x), and accepted from 2.10.0.
func TestClientPublicPARVersionGate(t *testing.T) {
	for version, refused := range map[string]bool{"2.9.0": true, "2.10.0": false, "2.17.0": false} {
		t.Run(version, func(t *testing.T) {
			fake := newFakePocketID(t, version, &fakeClient{ID: "c1"})
			r := &clientResource{client: fake.start()}
			ctx := context.Background()
			s := clientSchema(t).Schema
			model := lifecycleModel()
			model.IsPublic = types.BoolValue(true)
			model.RequiresPushedAuthorizationRequests = types.BoolValue(true)
			model.ClientSecret, model.ClientSecretID = types.StringNull(), types.StringNull()
			plan := tfsdk.Plan{Schema: s}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			assert.Equal(t, refused, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			if refused {
				assert.Empty(t, fake.mutations(), "refused before any mutation")
				assert.Contains(t, resp.Diagnostics[0].Detail(), "2.10.0")
				return
			}
			var after clientResourceModel
			require.False(t, resp.State.Get(ctx, &after).HasError())
			assert.True(t, after.RequiresPushedAuthorizationRequests.ValueBool())

			// An update is gated the same way.
			planned := after
			planned.Name = types.StringValue("renamed")
			updateResp, _ := runUpdate(t, r, after, planned, configOf(planned))
			assert.False(t, updateResp.Diagnostics.HasError(), "%v", updateResp.Diagnostics)
		})
	}
	fake := newFakePocketID(t, "2.9.0", &fakeClient{ID: "c1", IsPublic: true, PkceEnabled: true})
	r := &clientResource{client: fake.start()}
	prior := managedModel()
	prior.IsPublic = types.BoolValue(true)
	prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
	planned := prior
	planned.RequiresPushedAuthorizationRequests = types.BoolValue(true)
	resp, after := runUpdate(t, r, prior, planned, configOf(planned))
	require.True(t, resp.Diagnostics.HasError())
	assert.Empty(t, fake.mutations(), "refused before any mutation")
	assert.False(t, after.RequiresPushedAuthorizationRequests.ValueBool())
}
