package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func groupRelationsClient(t *testing.T, body string) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return c
}

func groupRelationsPage(items ...map[string]any) string {
	encoded, _ := json.Marshal(map[string]any{
		"data":       items,
		"pagination": map[string]any{"totalPages": 1, "totalItems": len(items), "currentPage": 1, "itemsPerPage": 100},
	})
	return string(encoded)
}

// Pocket ID 2.14's client list has allowedUserGroupsCount and no
// allowedUserGroups; later versions have a list, null when it is empty. Only
// the absence of the key means the groups cannot be read from the list.
func TestClient_AllowedClientIDsByGroup_RecognizesAListWithoutGroups(t *testing.T) {
	ctx := context.Background()

	older := groupRelationsClient(t, groupRelationsPage(map[string]any{"id": "a", "allowedUserGroupsCount": 2}))
	_, ok, err := older.AllowedClientIDsByGroup(ctx)
	require.NoError(t, err)
	assert.False(t, ok)

	newer := groupRelationsClient(t, groupRelationsPage(
		map[string]any{"id": "a", "allowedUserGroups": nil},
		map[string]any{"id": "b", "allowedUserGroups": []any{map[string]any{"id": "g1"}, map[string]any{"id": "g2"}}},
		map[string]any{"id": "c", "allowedUserGroups": []any{map[string]any{"id": "g1"}}},
	))
	byGroup, ok, err := newer.AllowedClientIDsByGroup(ctx)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, map[string][]string{"g1": {"b", "c"}, "g2": {"b"}}, byGroup)

	none := groupRelationsClient(t, groupRelationsPage())
	byGroup, ok, err = none.AllowedClientIDsByGroup(ctx)
	require.NoError(t, err)
	assert.True(t, ok, "no clients is not a sign of an old server")
	assert.Empty(t, byGroup)
}

func TestClient_GroupMemberIDs_InvertsTheUserList(t *testing.T) {
	c := groupRelationsClient(t, groupRelationsPage(
		map[string]any{"id": "u1", "userGroups": []any{map[string]any{"id": "g1"}, map[string]any{"id": "g2"}}},
		map[string]any{"id": "u2", "userGroups": nil},
		map[string]any{"id": "u3", "userGroups": []any{map[string]any{"id": "g1"}}},
	))
	members, err := c.GroupMemberIDs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"g1": {"u1", "u3"}, "g2": {"u1"}}, members)
}

func TestClient_GetUserGroupDetail_RefusesANonUUID(t *testing.T) {
	c := groupRelationsClient(t, "{}")
	_, err := c.GetUserGroupDetail(context.Background(), "../x")
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
}
