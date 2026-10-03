package resources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	heldSecretID   = "00000000-0000-4000-8000-000000000000"
	heldDelete     = "DELETE /api/oidc/clients/c1/secrets/" + heldSecretID
	listSecrets    = "GET /api/oidc/clients/c1/secrets"
	clientWrite    = "PUT /api/oidc/clients/c1"
	generateSecret = "POST /api/oidc/clients/c1/secrets"
)

// revokeUnconfirmed makes the managed confidential client public (or turns
// generate_secret off) while Pocket ID revokes the secret but the answer is
// lost and the confirming list fails, so state keeps the secret with a
// pending revocation.
func revokeUnconfirmed(t *testing.T, h *protoHarness, fake *fakePocketID, change func(*clientResourceModel)) applied {
	t.Helper()
	fake.lostResponse = map[string]bool{heldDelete: true}
	fake.fail[listSecrets] = 403
	prior := managedModel()
	config := homelabConfig()
	change(&config)
	result := h.apply(&prior, config, h.plan(&prior, config, nil))
	require.Contains(t, result.errors, "may still be valid")
	require.Empty(t, fake.secrets, "Pocket ID did revoke it")
	require.Equal(t, prior.ClientSecret, result.model.ClientSecret, "unconfirmed, so state keeps it")
	delete(fake.lostResponse, heldDelete)
	return result
}

// Reversing the change after a revocation that was not confirmed must not
// keep a secret that no longer exists. A refresh that can list the secrets
// clears it from state, and the reversed configuration then generates one.
func TestClientReversedRevocationAfterRefresh(t *testing.T) {
	for name, tc := range map[string]struct {
		change, reverse func(*clientResourceModel)
	}{
		"made public, then confidential again": {
			change:  func(c *clientResourceModel) { c.IsPublic = types.BoolValue(true) },
			reverse: func(c *clientResourceModel) { c.IsPublic = types.BoolValue(false) },
		},
		"generate_secret off, then on again": {
			change:  func(c *clientResourceModel) { c.GenerateSecret = types.BoolValue(false) },
			reverse: func(c *clientResourceModel) { c.GenerateSecret = types.BoolNull() },
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := managedFake(t, "2.17.0")
			h := newProtoHarness(t, fake.start())
			result := revokeUnconfirmed(t, h, fake, tc.change)

			delete(fake.fail, listSecrets) // the secrets can be listed again
			refreshed := h.read(*result.model, result.private)
			require.Empty(t, refreshed.errors)
			assert.True(t, refreshed.model.ClientSecret.IsNull(), "refresh confirms the secret is gone")
			assert.True(t, refreshed.model.ClientSecretID.IsNull())

			config := homelabConfig()
			tc.reverse(&config)
			p := h.plan(refreshed.model, config, refreshed.private)
			require.Empty(t, p.errors)
			assert.True(t, p.model.ClientSecret.IsUnknown(), "the reversed configuration generates a secret")
			done := h.apply(refreshed.model, config, p)
			require.Empty(t, done.errors)
			require.Len(t, fake.secrets, 1)
			assert.Equal(t, fake.secrets[0].ID, done.model.ClientSecretID.ValueString())
			assert.False(t, fake.client.IsPublic)
		})
	}
}

// If the secrets still cannot be listed, the reversed configuration is
// refused before any change, asking for a refresh: keeping the secret could
// record a credential that no longer exists.
func TestClientReversedRevocationUnresolved(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	h := newProtoHarness(t, fake.start())
	result := revokeUnconfirmed(t, h, fake, func(c *clientResourceModel) { c.IsPublic = types.BoolValue(true) })
	writes := len(fake.mutations())

	refreshed := h.read(*result.model, result.private) // the list still fails
	require.Empty(t, refreshed.errors)
	assert.Equal(t, result.model.ClientSecret, refreshed.model.ClientSecret)

	config := homelabConfig() // confidential again
	p := h.plan(refreshed.model, config, refreshed.private)
	require.Empty(t, p.errors)
	done := h.apply(refreshed.model, config, p)
	assert.Contains(t, done.errors, "refresh")
	assert.Len(t, fake.mutations(), writes, "no change was made")
	assert.True(t, fake.client.IsPublic)

	// Once the list works, a refresh resolves it and the apply generates.
	delete(fake.fail, listSecrets)
	refreshed = h.read(*done.model, done.private)
	p = h.plan(refreshed.model, config, refreshed.private)
	done = h.apply(refreshed.model, config, p)
	require.Empty(t, done.errors)
	assert.Len(t, fake.secrets, 1)
	assert.False(t, done.model.ClientSecret.IsNull())
}

// A pending revocation whose secret does still exist is cancelled by the
// reversed configuration, keeping the secret.
func TestClientReversedRevocationSecretStillExists(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	fake.fail[heldDelete] = 503 // not revoked
	h := newProtoHarness(t, fake.start())
	prior := managedModel()
	config := homelabConfig()
	config.IsPublic = types.BoolValue(true)
	result := h.apply(&prior, config, h.plan(&prior, config, nil))
	require.NotEmpty(t, result.errors)
	require.Len(t, fake.secrets, 1)
	delete(fake.fail, heldDelete)

	refreshed := h.read(*result.model, result.private)
	config = homelabConfig()
	p := h.plan(refreshed.model, config, refreshed.private)
	done := h.apply(refreshed.model, config, p)
	require.Empty(t, done.errors)
	assert.Len(t, fake.secrets, 1)
	assert.Equal(t, prior.ClientSecret, done.model.ClientSecret)
	again := h.plan(done.model, config, done.private)
	assert.True(t, h.emptyPlan(*done.model, again))
}

// A generation whose POST Pocket ID carried out but whose answer was lost
// leaves a valid secret nobody knows the value of. The next apply must not
// create another while that secret exists: it stops before any change and
// lists the secret by ID and prefix. Once the operator revokes it, the
// generation goes ahead.
func TestClientUncertainGenerationNotRepeated(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	fake.client.IsPublic, fake.secrets = true, nil
	fake.lostResponse = map[string]bool{generateSecret: true}
	h := newProtoHarness(t, fake.start())
	prior := managedModel()
	prior.IsPublic = types.BoolValue(true)
	prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
	config := homelabConfig() // confidential, generate_secret default true

	result := h.apply(&prior, config, h.plan(&prior, config, nil))
	require.Contains(t, result.errors, "may have been created")
	require.Len(t, fake.secrets, 1, "Pocket ID created it")
	assert.Equal(t, 1, fake.called(generateSecret))
	fake.lostResponse = nil

	refreshed := h.read(*result.model, result.private)
	require.Empty(t, refreshed.errors)
	p := h.plan(refreshed.model, config, refreshed.private)
	require.Empty(t, p.errors)
	assert.True(t, p.model.ClientSecret.IsUnknown())
	assert.Equal(t, 1, p.warnings, "the plan says the apply checks first")
	writes := len(fake.mutations())
	refused := h.apply(refreshed.model, config, p)
	assert.Contains(t, refused.errors, fake.secrets[0].ID, "the unaccounted secret is listed")
	assert.Contains(t, refused.errors, fake.secrets[0].Prefix)
	assert.NotContains(t, refused.errors, "synthetic-generated-secret-value")
	assert.Len(t, fake.mutations(), writes, "no change was made")
	assert.Equal(t, 1, fake.called(generateSecret), "no second POST")

	// Still refused on the next attempt, until the operator revokes it.
	p = h.plan(refused.model, config, refused.private)
	refused = h.apply(refused.model, config, p)
	require.NotEmpty(t, refused.errors)
	fake.secrets = nil
	p = h.plan(refused.model, config, refused.private)
	done := h.apply(refused.model, config, p)
	require.Empty(t, done.errors)
	assert.Equal(t, 2, fake.called(generateSecret))
	require.Len(t, fake.secrets, 1)
	assert.Equal(t, fake.secrets[0].ID, done.model.ClientSecretID.ValueString())
	again := h.plan(done.model, config, done.private)
	assert.True(t, h.emptyPlan(*done.model, again))
	assert.Zero(t, again.warnings)
}

// A generation that certainly created nothing (the POST failed before Pocket
// ID acted, so no unknown secret appears) is simply tried again.
func TestClientUncertainGenerationThatCreatedNothing(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	fake.client.IsPublic, fake.secrets = true, nil
	fake.fail[generateSecret] = 503
	h := newProtoHarness(t, fake.start())
	prior := managedModel()
	prior.IsPublic = types.BoolValue(true)
	prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
	config := homelabConfig()
	result := h.apply(&prior, config, h.plan(&prior, config, nil))
	require.NotEmpty(t, result.errors)
	delete(fake.fail, generateSecret)

	refreshed := h.read(*result.model, result.private)
	done := h.apply(refreshed.model, config, h.plan(refreshed.model, config, refreshed.private))
	require.Empty(t, done.errors)
	assert.Equal(t, 2, fake.called(generateSecret))
	assert.Len(t, fake.secrets, 1)
}
