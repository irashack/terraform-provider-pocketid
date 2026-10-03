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
		map[string]any{"id": "b", "allowedUserGroups": []any{map[string]any{"id": "bbbbbbbb-0000-4000-8000-000000000001"}, map[string]any{"id": "bbbbbbbb-0000-4000-8000-000000000002"}}},
		map[string]any{"id": "c", "allowedUserGroups": []any{map[string]any{"id": "bbbbbbbb-0000-4000-8000-000000000001"}}},
	))
	byGroup, ok, err := newer.AllowedClientIDsByGroup(ctx)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, map[string][]string{"bbbbbbbb-0000-4000-8000-000000000001": {"b", "c"}, "bbbbbbbb-0000-4000-8000-000000000002": {"b"}}, byGroup)

	none := groupRelationsClient(t, groupRelationsPage())
	byGroup, ok, err = none.AllowedClientIDsByGroup(ctx)
	require.NoError(t, err)
	assert.True(t, ok, "no clients is not a sign of an old server")
	assert.Empty(t, byGroup)
}

func TestClient_GroupMemberIDs_InvertsTheUserList(t *testing.T) {
	c := groupRelationsClient(t, groupRelationsPage(
		map[string]any{"id": "dddddddd-0000-4000-8000-000000000001", "userGroups": []any{map[string]any{"id": "bbbbbbbb-0000-4000-8000-000000000001"}, map[string]any{"id": "bbbbbbbb-0000-4000-8000-000000000002"}}},
		map[string]any{"id": "dddddddd-0000-4000-8000-000000000002", "userGroups": nil},
		map[string]any{"id": "dddddddd-0000-4000-8000-000000000003", "userGroups": []any{map[string]any{"id": "bbbbbbbb-0000-4000-8000-000000000001"}}},
	))
	members, err := c.GroupMemberIDs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"bbbbbbbb-0000-4000-8000-000000000001": {"dddddddd-0000-4000-8000-000000000001", "dddddddd-0000-4000-8000-000000000003"}, "bbbbbbbb-0000-4000-8000-000000000002": {"dddddddd-0000-4000-8000-000000000001"}}, members)
}

func TestClient_GetUserGroupDetail_RefusesANonUUID(t *testing.T) {
	c := groupRelationsClient(t, "{}")
	_, err := c.GetUserGroupDetail(context.Background(), "../x")
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
}

// A group record is complete or it is an error: the callers that replace a
// membership decide from it, so a record that merely lacks the users must not
// read as a group with none.
func TestClient_GetUserGroupDetail_RequiresACompleteRecordOfTheGroupAsked(t *testing.T) {
	const asked = "22222222-2222-4222-8222-222222222222"
	const other = "33333333-3333-4333-8333-333333333333"
	full := func(id string) map[string]any {
		return map[string]any{"id": id, "name": "g", "customClaims": nil, "users": nil, "allowedOidcClients": nil}
	}
	without := func(key string) map[string]any {
		m := full(asked)
		delete(m, key)
		return m
	}
	encode := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	for name, body := range map[string]string{
		"null":                "null",
		"only an id":          encode(map[string]any{"id": asked}),
		"an empty object":     "{}",
		"an array":            "[]",
		"no users":            encode(without("users")),
		"no allowed clients":  encode(without("allowedOidcClients")),
		"no claims":           encode(without("customClaims")),
		"no id":               encode(without("id")),
		"another group":       encode(full(other)),
		"users is not a list": `{"id":"` + asked + `","customClaims":null,"users":"x","allowedOidcClients":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := groupRelationsClient(t, body)
			detail, err := c.GetUserGroupDetail(context.Background(), asked)
			require.Error(t, err)
			assert.Nil(t, detail)
		})
	}

	// Explicit nulls are what Pocket ID sends for empty lists, and the ID may
	// differ in case.
	c := groupRelationsClient(t, encode(full("22222222-2222-4222-8222-222222222222")))
	detail, err := c.GetUserGroupDetail(context.Background(), asked)
	require.NoError(t, err)
	assert.Empty(t, detail.MemberIDs)
	assert.Empty(t, detail.AllowedClientIDs)

	c = groupRelationsClient(t, `{"id":"`+asked+`","customClaims":[],"users":[{"id":"dddddddd-0000-4000-8000-000000000001"},{"id":"dddddddd-0000-4000-8000-000000000001"},{"id":"dddddddd-0000-4000-8000-000000000002"}],"allowedOidcClients":[]}`)
	detail, err = c.GetUserGroupDetail(context.Background(), asked)
	require.NoError(t, err)
	assert.Equal(t, []string{"dddddddd-0000-4000-8000-000000000001", "dddddddd-0000-4000-8000-000000000002"}, detail.MemberIDs, "a repeated ID counts once")
}
