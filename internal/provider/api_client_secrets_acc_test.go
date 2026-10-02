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
	assert.Len(t, generated.Value, 32)
	assert.Equal(t, generated.Value[:4], generated.Prefix)
	assert.True(t, generated.IsActive)
	assert.Nil(t, generated.ExpiresAt)

	const chosen = "tf-acc-chosen-secret-0123456789"
	expires := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	supplied, err := c.GenerateClientSecret(ctx, created.ID, &client.ClientSecretOptions{Value: chosen, ExpiresAt: &expires})
	require.NoError(t, err)
	assert.Equal(t, chosen, supplied.Value)
	assert.Equal(t, chosen[:4], supplied.Prefix)
	require.NotNil(t, supplied.ExpiresAt)
	assert.True(t, supplied.ExpiresAt.Equal(expires), "expiry %s, want %s", supplied.ExpiresAt, expires)

	listed, err := c.ListClientSecrets(ctx, created.ID)
	require.NoError(t, err)
	byID := map[string]client.ClientSecretMetadata{}
	for _, secret := range listed {
		byID[secret.ID] = secret
	}
	require.Contains(t, byID, generated.ID)
	require.Contains(t, byID, supplied.ID)
	assert.Equal(t, generated.Prefix, byID[generated.ID].Prefix)
	assert.False(t, byID[generated.ID].CreatedAt.IsZero())
	require.NotNil(t, byID[supplied.ID].ExpiresAt)
	assert.True(t, byID[supplied.ID].ExpiresAt.Equal(expires))
	assert.True(t, byID[supplied.ID].IsActive)

	past := time.Now().Add(-time.Hour)
	_, err = c.GenerateClientSecret(ctx, created.ID, &client.ClientSecretOptions{ExpiresAt: &past})
	require.Error(t, err)
	assert.True(t, client.IsDefiniteRejection(err), "a past expiry is rejected")
}
