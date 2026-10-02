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

// Group-association writes report the set the server holds afterwards: an ID
// that names no group is dropped without an error, and an empty set is
// written as [] (the server rejects null).
func TestAccAPI_associationUpdatesReportResult(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)

	const missingUUID = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
	name := "tf-acc-assoc-" + acctest.RandString(6)
	group, err := c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: name, FriendlyName: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteUserGroup(context.Background(), group.ID) })
	user, err := c.CreateUser(ctx, &client.UserCreateRequest{Username: name, Email: name + "@example.com"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteUser(context.Background(), user.ID) })
	oidcClient, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
		Name: name, CallbackURLs: []string{"https://example.com/callback"}, IsPublic: true, PkceEnabled: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), oidcClient.ID) })

	t.Run("user groups", func(t *testing.T) {
		got, err := c.UpdateUserGroups(ctx, user.ID, []string{group.ID, missingUUID})
		require.NoError(t, err)
		assert.Equal(t, []string{group.ID}, got, "the unknown ID is dropped")

		status, err := testAccAPI("PUT", "/api/users/"+user.ID+"/user-groups", map[string]any{"userGroupIds": nil}, nil)
		require.NoError(t, err)
		assert.Equal(t, 400, status, "null is rejected")

		got, err = c.UpdateUserGroups(ctx, user.ID, nil)
		require.NoError(t, err)
		assert.Empty(t, got)
		read, err := c.GetUser(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, read.UserGroups)
	})
	t.Run("client allowed groups", func(t *testing.T) {
		got, err := c.UpdateClientAllowedUserGroups(ctx, oidcClient.ID, []string{missingUUID, group.ID})
		require.NoError(t, err)
		assert.Equal(t, []string{group.ID}, got, "the unknown ID is dropped")

		status, err := testAccAPI("PUT", "/api/oidc/clients/"+oidcClient.ID+"/allowed-user-groups", map[string]any{"userGroupIds": nil}, nil)
		require.NoError(t, err)
		assert.Equal(t, 400, status, "null is rejected")

		got, err = c.UpdateClientAllowedUserGroups(ctx, oidcClient.ID, nil)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

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
