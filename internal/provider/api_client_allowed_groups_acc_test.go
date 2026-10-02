//go:build acc
// +build acc

package provider_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Writing a client's allowed groups reports the set the server holds
// afterwards: an ID that names no group is dropped without an error, and an
// empty set is written as [] (the server rejects null).
func TestAccAPI_clientAllowedGroupUpdatesReportResult(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)

	const missingUUID = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
	name := "tf-acc-cgroups-" + acctest.RandString(6)
	group, err := c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: name, FriendlyName: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteUserGroup(context.Background(), group.ID) })
	oidcClient, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
		Name: name, CallbackURLs: []string{"https://example.com/callback"}, IsPublic: true, PkceEnabled: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), oidcClient.ID) })

	got, err := c.UpdateClientAllowedUserGroups(ctx, oidcClient.ID, []string{missingUUID, group.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{group.ID}, got, "the unknown ID is dropped")

	status, err := testAccAPI("PUT", "/api/oidc/clients/"+oidcClient.ID+"/allowed-user-groups", map[string]any{"userGroupIds": nil}, nil)
	require.NoError(t, err)
	assert.Equal(t, 400, status, "null is rejected")

	got, err = c.UpdateClientAllowedUserGroups(ctx, oidcClient.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}
