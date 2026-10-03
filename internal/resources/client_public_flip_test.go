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
	// A public client that kept the secret a 2.4.x provider stored while it
	// was confidential (that provider and the server both kept it).
	legacyPublic := managedModel()
	legacyPublic.IsPublic, legacyPublic.GenerateSecret, legacyPublic.ClientSecretID = types.BoolValue(true), types.BoolNull(), types.StringNull()
	for name, tc := range map[string]struct {
		prior       clientResourceModel
		public, gen bool
		pending     bool
		want        secretAction
	}{
		"legacy public client keeps its secret":      {legacyPublic, true, true, false, secretKeep},
		"legacy public client, generate_secret off":  {legacyPublic, true, false, false, secretRevoke},
		"legacy public client becomes confidential":  {legacyPublic, false, true, false, secretKeep},
		"public with a pending revocation":           {legacyPublic, true, true, true, secretRevoke},
		"pending revocation, back to confidential":   {legacyPublic, false, true, true, secretKeep},
		"becomes public: revoke":                     {confidential, true, true, false, secretRevoke},
		"becomes confidential: generate":             {public, false, true, false, secretGenerate},
		"becomes confidential without a secret":      {public, false, false, false, secretNone},
		"becomes confidential and generate turns on": {publicNoGenerate, false, true, false, secretGenerate},
		"stays public":       {public, true, true, false, secretNone},
		"stays confidential": {confidential, false, true, false, secretKeep},
	} {
		t.Run(name, func(t *testing.T) {
			planned := tc.prior
			planned.IsPublic, planned.GenerateSecret = types.BoolValue(tc.public), types.BoolValue(tc.gen)
			action, known := planSecretAction(tc.prior, planned, tc.pending)
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
		h := newProtoHarness(t, fake.start())
		prior := managedModel()
		config := homelabConfig()
		config.IsPublic = types.BoolValue(true)
		p := h.plan(&prior, config, nil)
		require.Empty(t, p.errors)
		result := h.apply(&prior, config, p)
		require.Contains(t, result.errors, "may still be valid")
		assert.True(t, result.model.IsPublic.ValueBool(), "the client update is recorded")
		assert.Equal(t, prior.ClientSecret, result.model.ClientSecret, "the unconfirmed secret stays in state")

		// The revocation stays due through a refresh, and the next plan
		// with the unchanged configuration revokes it.
		refreshed := h.read(*result.model, result.private)
		require.Empty(t, refreshed.errors)
		next := h.plan(refreshed.model, config, refreshed.private)
		require.Empty(t, next.errors)
		assert.True(t, next.model.ClientSecret.IsNull(), "the next plan revokes the secret")
		delete(fake.fail, "DELETE /api/oidc/clients/c1/secrets/00000000-0000-4000-8000-000000000000")
		done := h.apply(refreshed.model, config, next)
		require.Empty(t, done.errors)
		assert.Empty(t, fake.secrets)
		assert.True(t, done.model.ClientSecret.IsNull())
		again := h.plan(done.model, config, done.private)
		assert.True(t, h.emptyPlan(*done.model, again), "nothing left to do")
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
		action, _ := planSecretAction(after, next, false)
		assert.Equal(t, secretGenerate, action)
	})
}

// A public client whose 2.4.x state kept the secret stored while it was
// confidential (that provider and Pocket ID both kept it) upgrades to an
// empty plan, and an unrelated update leaves the secret alone.
func TestClientLegacyPublicClientKeepsItsSecret(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	fake.client.IsPublic = true
	h := newProtoHarness(t, fake.start())
	legacy := managedModel()
	legacy.IsPublic, legacy.GenerateSecret, legacy.ClientSecretID = types.BoolValue(true), types.BoolNull(), types.StringNull()
	config := homelabConfig()
	config.IsPublic = types.BoolValue(true)

	refreshed := h.read(legacy, nil)
	require.Empty(t, refreshed.errors)
	p := h.plan(refreshed.model, config, refreshed.private)
	require.Empty(t, p.errors)
	assert.True(t, h.emptyPlan(*refreshed.model, p), "the upgrade plans no change")

	config.Name = types.StringValue("renamed")
	p = h.plan(refreshed.model, config, refreshed.private)
	result := h.apply(refreshed.model, config, p)
	require.Empty(t, result.errors)
	assert.Len(t, fake.secrets, 1, "the secret is not revoked")
	assert.Equal(t, refreshed.model.ClientSecret, result.model.ClientSecret)
	assert.NotContains(t, fake.mutations(), "DELETE /api/oidc/clients/c1/secrets/00000000-0000-4000-8000-000000000000")
}

// generate_secret true to false whose revocation fails stays due: state
// already records generate_secret = false, and the next plan still revokes.
func TestClientPendingRevocationAfterGenerateSecretOff(t *testing.T) {
	const held = "DELETE /api/oidc/clients/c1/secrets/00000000-0000-4000-8000-000000000000"
	fake := managedFake(t, "2.17.0")
	fake.fail[held] = 503
	h := newProtoHarness(t, fake.start())
	prior := managedModel()
	config := homelabConfig()
	config.GenerateSecret = types.BoolValue(false)
	result := h.apply(&prior, config, h.plan(&prior, config, nil))
	require.NotEmpty(t, result.errors)
	assert.False(t, result.model.GenerateSecret.ValueBool())
	assert.False(t, result.model.ClientSecret.IsNull())

	refreshed := h.read(*result.model, result.private)
	next := h.plan(refreshed.model, config, refreshed.private)
	require.Empty(t, next.errors)
	assert.True(t, next.model.ClientSecret.IsNull(), "the revocation is planned again")
	delete(fake.fail, held)
	done := h.apply(refreshed.model, config, next)
	require.Empty(t, done.errors)
	assert.Empty(t, fake.secrets)
	assert.Equal(t, 2, fake.called(held), "one attempt per apply")
}

// Making a public client confidential whose client PUT is carried out but
// whose response is lost: the generation it planned is not lost with it.
// The next refresh shows the client confidential, and the next plan still
// generates the secret.
func TestClientLostUpdateResponseKeepsGenerationDue(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	fake.client.IsPublic, fake.secrets = true, nil
	fake.lostResponse = map[string]bool{"PUT /api/oidc/clients/c1": true}
	h := newProtoHarness(t, fake.start())
	prior := managedModel()
	prior.IsPublic = types.BoolValue(true)
	prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
	config := homelabConfig() // confidential, generate_secret default true

	result := h.apply(&prior, config, h.plan(&prior, config, nil))
	require.NotEmpty(t, result.errors)
	assert.False(t, fake.client.IsPublic, "the server applied the update")
	assert.Zero(t, fake.called("POST /api/oidc/clients/c1/secrets"))

	fake.lostResponse = nil
	refreshed := h.read(*result.model, result.private)
	require.Empty(t, refreshed.errors)
	assert.False(t, refreshed.model.IsPublic.ValueBool())
	next := h.plan(refreshed.model, config, refreshed.private)
	require.Empty(t, next.errors)
	assert.True(t, next.model.ClientSecret.IsUnknown(), "the next plan still generates the secret")
	done := h.apply(refreshed.model, config, next)
	require.Empty(t, done.errors)
	assert.Len(t, fake.secrets, 1)
	assert.False(t, done.model.ClientSecret.IsNull())
	assert.True(t, done.model.GenerateSecret.ValueBool())
}

// The same for a confidential client made public: the revocation it planned
// stays due after a lost response.
func TestClientLostUpdateResponseKeepsRevocationDue(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	fake.lostResponse = map[string]bool{"PUT /api/oidc/clients/c1": true}
	h := newProtoHarness(t, fake.start())
	prior := managedModel()
	config := homelabConfig()
	config.IsPublic = types.BoolValue(true)

	result := h.apply(&prior, config, h.plan(&prior, config, nil))
	require.NotEmpty(t, result.errors)
	assert.True(t, fake.client.IsPublic)
	assert.Len(t, fake.secrets, 1)

	fake.lostResponse = nil
	refreshed := h.read(*result.model, result.private)
	next := h.plan(refreshed.model, config, refreshed.private)
	require.Empty(t, next.errors)
	assert.True(t, next.model.ClientSecret.IsNull(), "the next plan still revokes the secret")
	done := h.apply(refreshed.model, config, next)
	require.Empty(t, done.errors)
	assert.Empty(t, fake.secrets)
}
