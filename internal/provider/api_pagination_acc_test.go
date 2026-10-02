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
