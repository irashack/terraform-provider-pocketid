package client_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// newGroupMembershipTestServer serves GetUser from userGroups (mutated in
// place by the handler on each PUT) and records every PUT payload it sees.
func newGroupMembershipTestServer(t *testing.T, userID string, initialGroups []string) (*client.Client, *[]client.UpdateUserGroupsRequest) {
	t.Helper()

	current := append([]string(nil), initialGroups...)
	var puts []client.UpdateUserGroupsRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/users/"+userID:
			groups := make([]client.UserGroup, 0, len(current))
			for _, id := range current {
				groups = append(groups, client.UserGroup{ID: id})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.User{ID: userID, UserGroups: groups})
		case r.Method == http.MethodPut && r.URL.Path == "/api/users/"+userID+"/user-groups":
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			puts = append(puts, req)
			current = append([]string(nil), req.UserGroupIDs...)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": "API endpoint not found"}`)
		}
	}))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	return c, &puts
}

func TestClient_AddUserToGroup_NewMembership(t *testing.T) {
	c, puts := newGroupMembershipTestServer(t, "user-1", []string{"group-existing"})

	err := c.AddUserToGroup("user-1", "group-new")
	require.NoError(t, err)

	require.Len(t, *puts, 1)
	// The existing membership must be preserved; only the new group is added.
	assert.ElementsMatch(t, []string{"group-existing", "group-new"}, (*puts)[0].UserGroupIDs)
}

func TestClient_AddUserToGroup_AlreadyMember(t *testing.T) {
	c, puts := newGroupMembershipTestServer(t, "user-1", []string{"group-existing"})

	err := c.AddUserToGroup("user-1", "group-existing")
	require.NoError(t, err)

	// No write should happen when the user is already a member.
	assert.Empty(t, *puts)
}

func TestClient_AddUserToGroup_UserNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": "User not found"}`)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.AddUserToGroup("missing-user", "group-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestClient_RemoveUserFromGroup_ExistingMembership(t *testing.T) {
	c, puts := newGroupMembershipTestServer(t, "user-1", []string{"group-keep", "group-remove"})

	err := c.RemoveUserFromGroup("user-1", "group-remove")
	require.NoError(t, err)

	require.Len(t, *puts, 1)
	// The other membership must be preserved; only the target group is removed.
	assert.Equal(t, []string{"group-keep"}, (*puts)[0].UserGroupIDs)
}

func TestClient_RemoveUserFromGroup_NotAMember(t *testing.T) {
	c, puts := newGroupMembershipTestServer(t, "user-1", []string{"group-keep"})

	err := c.RemoveUserFromGroup("user-1", "group-absent")
	require.NoError(t, err)

	// No write should happen when the user was never a member.
	assert.Empty(t, *puts)
}

func TestClient_RemoveUserFromGroup_LastMembership(t *testing.T) {
	c, puts := newGroupMembershipTestServer(t, "user-1", []string{"group-only"})

	err := c.RemoveUserFromGroup("user-1", "group-only")
	require.NoError(t, err)

	require.Len(t, *puts, 1)
	assert.Equal(t, []string{}, (*puts)[0].UserGroupIDs)
}

func TestClient_UserHasGroupMembership(t *testing.T) {
	c, _ := newGroupMembershipTestServer(t, "user-1", []string{"group-a", "group-b"})

	has, err := c.UserHasGroupMembership("user-1", "group-a")
	require.NoError(t, err)
	assert.True(t, has)

	has, err = c.UserHasGroupMembership("user-1", "group-missing")
	require.NoError(t, err)
	assert.False(t, has)
}

func TestClient_UserHasGroupMembership_UserNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": "User not found"}`)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	has, err := c.UserHasGroupMembership("missing-user", "group-1")
	assert.Error(t, err)
	assert.False(t, has)

	var status *client.HTTPError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, 404, status.StatusCode)
}
