//go:build acc
// +build acc

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Creating a secret returns its ID, metadata and value, and accepts a
// caller-chosen value and an expiry (Pocket ID 2.14.0+).
func TestAccAPI_clientSecretCreation(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)

	created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
		Name: "tf-acc-secret-" + acctest.RandString(6), CallbackURLs: []string{"https://example.com/callback"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), created.ID) })

	generated, err := c.GenerateClientSecret(ctx, created.ID, nil)
	require.NoError(t, err)
	require.NoError(t, client.ValidateUUID("client secret", generated.ID))
	testAccRequireFormat(t, "the generated secret", generated.Value, testAccSecretFormat)
	testAccAssertSame(t, "the generated secret's prefix", generated.Value[:4], generated.Prefix)
	assert.True(t, generated.IsActive)
	assert.Nil(t, generated.ExpiresAt)

	const chosen = "tf-acc-chosen-secret-0123456789"
	expires := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	supplied, err := c.GenerateClientSecret(ctx, created.ID, &client.ClientSecretOptions{Value: chosen, ExpiresAt: &expires})
	require.NoError(t, err)
	testAccAssertSame(t, "the supplied secret's value", chosen, supplied.Value)
	testAccAssertSame(t, "the supplied secret's prefix", chosen[:4], supplied.Prefix)
	require.NotNil(t, supplied.ExpiresAt)
	assert.True(t, supplied.ExpiresAt.Equal(expires), "expiry %s, want %s", supplied.ExpiresAt, expires)

	listed, err := c.ListClientSecrets(ctx, created.ID)
	require.NoError(t, err)
	byID := map[string]client.ClientSecretMetadata{}
	for _, secret := range listed {
		byID[secret.ID] = secret
	}
	_, generatedListed := byID[generated.ID]
	_, suppliedListed := byID[supplied.ID]
	require.True(t, generatedListed, "the generated secret is listed")
	require.True(t, suppliedListed, "the supplied secret is listed")
	testAccAssertSame(t, "the listed prefix of the generated secret", generated.Prefix, byID[generated.ID].Prefix)
	assert.False(t, byID[generated.ID].CreatedAt.IsZero())
	require.NotNil(t, byID[supplied.ID].ExpiresAt)
	assert.True(t, byID[supplied.ID].ExpiresAt.Equal(expires))
	assert.True(t, byID[supplied.ID].IsActive)

	past := time.Now().Add(-time.Hour)
	_, err = c.GenerateClientSecret(ctx, created.ID, &client.ClientSecretOptions{ExpiresAt: &past})
	require.Error(t, err)
	assert.True(t, client.IsDefiniteRejection(err), "a past expiry is rejected")
}

// The per-client secret limit is 20 and counts expired secrets; the refusal
// is a definite rejection. RevokeClientSecret confirms absence, also for a
// secret that is already gone.
func TestAccAPI_clientSecretLimitAndRevoke(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)

	created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
		Name: "tf-acc-secret-limit-" + acctest.RandString(6), CallbackURLs: []string{"https://example.com/callback"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), created.ID) })

	kept, err := c.CreateClientSecret(ctx, created.ID, nil)
	require.NoError(t, err)
	listed, err := c.ListClientSecrets(ctx, created.ID)
	require.NoError(t, err)
	expires := time.Now().Add(4 * time.Second)
	for i := len(listed); i < client.MaxClientSecrets; i++ {
		_, err := c.CreateClientSecret(ctx, created.ID, &client.ClientSecretOptions{ExpiresAt: &expires})
		require.NoError(t, err)
	}
	time.Sleep(time.Until(expires.Add(time.Second)))
	listed, err = c.ListClientSecrets(ctx, created.ID)
	require.NoError(t, err)
	testAccRequireCount(t, "secrets on the client at the limit", client.MaxClientSecrets, len(listed))
	expired := 0
	for _, secret := range listed {
		if !secret.IsActive {
			expired++
		}
	}
	require.Greater(t, expired, 0, "some secrets have expired")

	_, err = c.CreateClientSecret(ctx, created.ID, nil)
	require.Error(t, err, "expired secrets count toward the limit")
	assert.True(t, client.IsDefiniteRejection(err))
	after, err := c.ListClientSecrets(ctx, created.ID)
	require.NoError(t, err)
	testAccRequireCount(t, "secrets on the client after the refused request", client.MaxClientSecrets, len(after))

	require.NoError(t, c.RevokeClientSecret(ctx, created.ID, kept.ID))
	after, err = c.ListClientSecrets(ctx, created.ID)
	require.NoError(t, err)
	for _, secret := range after {
		assert.NotEqual(t, kept.ID, secret.ID)
	}
	require.NoError(t, c.RevokeClientSecret(ctx, created.ID, kept.ID), "an absent secret is confirmed absent")
	_, err = c.CreateClientSecret(ctx, created.ID, nil)
	require.NoError(t, err, "below the limit again")
}
