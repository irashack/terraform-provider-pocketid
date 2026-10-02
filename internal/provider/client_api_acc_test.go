//go:build acc
// +build acc

package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// The real server's not-found answers are what client.IsNotFound relies on:
// each kind is confirmed by its own error and by no other kind's.
func TestAccAPI_notFoundErrors(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)

	const missingUUID = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
	missingClient := "tf-acc-missing-" + acctest.RandString(8)
	kinds := []client.Resource{
		client.ResourceUser, client.ResourceUserGroup, client.ResourceOIDCClient,
		client.ResourceClientSecret, client.ResourceSCIMServiceProvider,
	}
	only := func(t *testing.T, err error, want client.Resource) {
		t.Helper()
		require.Error(t, err)
		for _, kind := range kinds {
			assert.Equal(t, kind == want, client.IsNotFound(err, kind), "kind %+v", kind)
		}
	}

	existing, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
		Name:         "tf-acc-not-found",
		CallbackURLs: []string{"https://example.com/callback"},
		IsPublic:     true,
		PkceEnabled:  true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), existing.ID) })

	t.Run("user", func(t *testing.T) {
		_, err := c.GetUser(ctx, missingUUID)
		only(t, err, client.ResourceUser)
		only(t, c.DeleteUser(ctx, missingUUID), client.ResourceUser)
	})
	t.Run("user group", func(t *testing.T) {
		_, err := c.GetUserGroup(ctx, missingUUID)
		only(t, err, client.ResourceUserGroup)
		only(t, c.DeleteUserGroup(ctx, missingUUID), client.ResourceUserGroup)
	})
	t.Run("OIDC client", func(t *testing.T) {
		_, err := c.GetClient(ctx, missingClient)
		only(t, err, client.ResourceOIDCClient)
		only(t, c.DeleteClient(ctx, missingClient), client.ResourceOIDCClient)
		_, err = c.ListClientSecrets(ctx, missingClient)
		only(t, err, client.ResourceOIDCClient)
		// A secret of a missing client reports the client.
		only(t, c.DeleteClientSecret(ctx, missingClient, missingUUID), client.ResourceOIDCClient)
	})
	t.Run("client secret", func(t *testing.T) {
		only(t, c.DeleteClientSecret(ctx, existing.ID, missingUUID), client.ResourceClientSecret)
	})
	t.Run("SCIM service provider", func(t *testing.T) {
		_, err := c.GetClientScimServiceProvider(ctx, existing.ID)
		only(t, err, client.ResourceSCIMServiceProvider)
		// Looked up by client ID, a missing client also reads as a missing
		// SCIM service provider.
		_, err = c.GetClientScimServiceProvider(ctx, missingClient)
		only(t, err, client.ResourceSCIMServiceProvider)
		only(t, c.DeleteScimServiceProvider(ctx, missingUUID), client.ResourceSCIMServiceProvider)
	})
}

// Lists follow every page of the real server's pagination (100 per page at
// most, 20 by default) and miss nothing. Pocket ID rate-limits /api to 100
// requests a second per client address (burst 300), and the later tests in
// this run share the fixture, so requests here are paced and the cleanup is
// checked.
func TestAccAPI_listAllPages(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)

	pace := time.NewTicker(20 * time.Millisecond)
	defer pace.Stop()
	const n = 101
	prefix := "tf-acc-page-" + acctest.RandString(6)
	var groupIDs, clientIDs []string
	t.Cleanup(func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for _, id := range groupIDs {
			<-ticker.C
			if err := c.DeleteUserGroup(context.Background(), id); err != nil {
				t.Errorf("cleanup of group %s: %s", id, err)
			}
		}
		for _, id := range clientIDs {
			<-ticker.C
			if err := c.DeleteClient(context.Background(), id); err != nil {
				t.Errorf("cleanup of client %s: %s", id, err)
			}
		}
	})
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s-%03d", prefix, i)
		<-pace.C
		group, err := c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: name, FriendlyName: name})
		require.NoError(t, err)
		groupIDs = append(groupIDs, group.ID)

		<-pace.C
		created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
			Name: name, CallbackURLs: []string{"https://example.com/callback"}, IsPublic: true, PkceEnabled: true,
		})
		require.NoError(t, err)
		clientIDs = append(clientIDs, created.ID)
	}

	groups, err := c.ListUserGroups(ctx)
	require.NoError(t, err)
	listedGroups := map[string]bool{}
	for _, group := range groups {
		listedGroups[group.ID] = true
	}
	for _, id := range groupIDs {
		assert.True(t, listedGroups[id], "group %s is listed", id)
	}

	clients, err := c.ListClients(ctx)
	require.NoError(t, err)
	listedClients := map[string]bool{}
	for _, listed := range clients {
		listedClients[listed.ID] = true
	}
	for _, id := range clientIDs {
		assert.True(t, listedClients[id], "client %s is listed", id)
	}
}
