package resources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanSecretActionOnPublicChange(t *testing.T) {
	confidential := managedModel()
	public := managedModel()
	public.IsPublic = types.BoolValue(true)
	public.ClientSecret, public.ClientSecretID = types.StringNull(), types.StringNull()
	publicNoGenerate := public
	publicNoGenerate.GenerateSecret = types.BoolValue(false)
	for name, tc := range map[string]struct {
		prior       clientResourceModel
		public, gen bool
		want        secretAction
	}{
		"becomes public: revoke":                     {confidential, true, true, secretRevoke},
		"becomes confidential: generate":             {public, false, true, secretGenerate},
		"becomes confidential without a secret":      {public, false, false, secretNone},
		"becomes confidential and generate turns on": {publicNoGenerate, false, true, secretGenerate},
		"stays public":                               {public, true, true, secretNone},
		"stays confidential":                         {confidential, false, true, secretKeep},
	} {
		t.Run(name, func(t *testing.T) {
			planned := tc.prior
			planned.IsPublic, planned.GenerateSecret = types.BoolValue(tc.public), types.BoolValue(tc.gen)
			action, known := planSecretAction(tc.prior, planned)
			require.True(t, known)
			assert.Equal(t, tc.want, action)
		})
	}
}

// is_public changes in place: a client that becomes confidential gets a
// secret after the update (Pocket ID refuses one for a public client), and
// one that becomes public has the held secret revoked after the update.
func TestClientUpdatePublicFlip(t *testing.T) {
	t.Run("public to confidential", func(t *testing.T) {
		fake := managedFake(t, "2.17.0")
		fake.client.IsPublic, fake.secrets = true, nil
		r := &clientResource{client: fake.start()}
		prior := managedModel()
		prior.IsPublic = types.BoolValue(true)
		prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
		planned := prior
		planned.IsPublic = types.BoolValue(false)
		planned.ClientSecret, planned.ClientSecretID = types.StringUnknown(), types.StringUnknown()
		resp, after := runUpdate(t, r, prior, planned, configOf(planned))
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assert.Equal(t, []string{"PUT /api/oidc/clients/c1", "POST /api/oidc/clients/c1/secrets"}, fake.mutations())
		assert.False(t, after.IsPublic.ValueBool())
		assert.Equal(t, "gen1synthetic-generated-secret-value", after.ClientSecret.ValueString())
		require.Len(t, fake.secrets, 1)
		assert.Equal(t, fake.secrets[0].ID, after.ClientSecretID.ValueString())
	})
	t.Run("confidential to public", func(t *testing.T) {
		fake := managedFake(t, "2.17.0")
		r := &clientResource{client: fake.start()}
		prior := managedModel()
		planned := prior
		planned.IsPublic = types.BoolValue(true)
		planned.ClientSecret, planned.ClientSecretID = types.StringNull(), types.StringNull()
		resp, after := runUpdate(t, r, prior, planned, configOf(planned))
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assert.Equal(t, []string{"PUT /api/oidc/clients/c1", "DELETE /api/oidc/clients/c1/secrets/00000000-0000-4000-8000-000000000000"}, fake.mutations())
		assert.True(t, fake.client.IsPublic)
		assert.Empty(t, fake.secrets)
		assert.True(t, after.ClientSecret.IsNull())
		assert.True(t, after.ClientSecretID.IsNull())
	})
	t.Run("confidential to public, revoke unconfirmed", func(t *testing.T) {
		fake := managedFake(t, "2.17.0")
		fake.fail["DELETE /api/oidc/clients/c1/secrets/00000000-0000-4000-8000-000000000000"] = 503
		r := &clientResource{client: fake.start()}
		prior := managedModel()
		planned := prior
		planned.IsPublic = types.BoolValue(true)
		planned.ClientSecret, planned.ClientSecretID = types.StringNull(), types.StringNull()
		resp, after := runUpdate(t, r, prior, planned, configOf(planned))
		require.True(t, resp.Diagnostics.HasError())
		requireNoSecret(t, resp.Diagnostics)
		assert.True(t, after.IsPublic.ValueBool(), "the client update is recorded")
		assert.Equal(t, prior.ClientSecret, after.ClientSecret, "the unconfirmed secret stays in state")
		// The next plan revokes it again.
		action, _ := planSecretAction(after, after)
		assert.Equal(t, secretRevoke, action)
	})
	t.Run("public to confidential, generation refused", func(t *testing.T) {
		fake := managedFake(t, "2.17.0")
		fake.client.IsPublic, fake.secrets = true, nil
		fake.fail["POST /api/oidc/clients/c1/secrets"] = 400
		r := &clientResource{client: fake.start()}
		prior := managedModel()
		prior.IsPublic = types.BoolValue(true)
		prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
		planned := prior
		planned.IsPublic = types.BoolValue(false)
		planned.ClientSecret, planned.ClientSecretID = types.StringUnknown(), types.StringUnknown()
		resp, after := runUpdate(t, r, prior, planned, configOf(planned))
		require.True(t, resp.Diagnostics.HasError())
		assert.False(t, after.IsPublic.ValueBool())
		assert.False(t, after.GenerateSecret.ValueBool(), "recorded so that the next plan generates again")
		// The next plan (configuration still generate_secret = true) generates.
		next := after
		next.GenerateSecret = types.BoolValue(true)
		action, _ := planSecretAction(after, next)
		assert.Equal(t, secretGenerate, action)
	})
}
