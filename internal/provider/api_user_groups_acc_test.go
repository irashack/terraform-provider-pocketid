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

// Writing a user's groups reports the set the server holds afterwards: an ID
// that names no group is dropped without an error, and an empty set is
// written as [] (the server rejects null).
func TestAccAPI_userGroupUpdatesReportResult(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)

	const missingUUID = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
	name := "tf-acc-ugroups-" + acctest.RandString(6)
	group, err := c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: name, FriendlyName: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteUserGroup(context.Background(), group.ID) })
	user, err := c.CreateUser(ctx, &client.UserCreateRequest{Username: name, Email: name + "@example.com"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteUser(context.Background(), user.ID) })

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
}

// LDAPEnabled reads the public application configuration; the fixture has
// LDAP disabled.
func TestAccAPI_ldapEnabledReadable(t *testing.T) {
	testAccPreCheck(t)
	c, err := testClient()
	require.NoError(t, err)
	enabled, err := c.LDAPEnabled(context.Background())
	require.NoError(t, err)
	assert.False(t, enabled)
}
