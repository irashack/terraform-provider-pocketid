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
		_, _ = fmt.Fprint(w, `{"error": "User not found", "code": "user_not_found"}`)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	// Create never tolerates a missing user - unlike RemoveUserFromGroup,
	// there is nothing sensible to add the group to.
	err = c.AddUserToGroup("missing-user", "group-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

// TestClient_RemoveUserFromGroup_UserConfirmedNotFound_NoOp is the positive
// case: Pocket-ID's own structured "user_not_found" error on the initial GET
// positively confirms the user is gone, so there is nothing to remove and no
// PUT is attempted.
func TestClient_RemoveUserFromGroup_UserConfirmedNotFound_NoOp(t *testing.T) {
	var putCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalled = true
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error": "User not found", "code": "user_not_found", "request_id": "abc"}`)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.RemoveUserFromGroup("missing-user", "group-1")
	assert.NoError(t, err)
	assert.False(t, putCalled, "a confirmed-missing user must not trigger any write")
}

// TestClient_RemoveUserFromGroup_GenericNotFoundOnGet_ReturnsError proves the
// P2 fix: a 404 on the initial GET that is not Pocket-ID's structured
// "user_not_found" body (a proxy's own not-found page, a wrong base URL, or
// any other generic 404) must not be assumed to mean the user is gone.
func TestClient_RemoveUserFromGroup_GenericNotFoundOnGet_ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "<html><body>404 Not Found</body></html>")
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.RemoveUserFromGroup("some-user", "group-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

// TestClient_RemoveUserFromGroup_PUT404_UserStillPresent_ReturnsError proves
// the second half of the P2 fix: a 404 from the update-user-groups PUT does
// not by itself prove the user is gone. Here a re-GET after the PUT failure
// still finds the user, so the original PUT error must be returned.
func TestClient_RemoveUserFromGroup_PUT404_UserStillPresent_ReturnsError(t *testing.T) {
	var getCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCount++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.User{
				ID:         "user-1",
				UserGroups: []client.UserGroup{{ID: "group-remove"}},
			})
		case http.MethodPut:
			// Some unrelated cause returns 404 for the update itself (for
			// example, a load balancer's own not-found page for a transient
			// routing hiccup) - this alone must not be read as "user gone".
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": "Not Found"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.RemoveUserFromGroup("user-1", "group-remove")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
	assert.Equal(t, 2, getCount, "the PUT failure must trigger exactly one re-GET to check")
}

// TestClient_RemoveUserFromGroup_PUT404_UserConfirmedGoneOnReGet_ReturnsNil
// is the success half: the user existed when read, the PUT failed with a
// 404, and the re-GET this time positively confirms the user is gone (for
// example, deleted by another process between the two requests) - Delete
// must treat this as already satisfied.
func TestClient_RemoveUserFromGroup_PUT404_UserConfirmedGoneOnReGet_ReturnsNil(t *testing.T) {
	var getCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCount++
			if getCount == 1 {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(client.User{
					ID:         "user-1",
					UserGroups: []client.UserGroup{{ID: "group-remove"}},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": "User not found", "code": "user_not_found"}`)
		case http.MethodPut:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": "User not found", "code": "user_not_found"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.RemoveUserFromGroup("user-1", "group-remove")
	assert.NoError(t, err)
	assert.Equal(t, 2, getCount)
}

// TestClient_RemoveUserFromGroup_PUT500_NoReGetAttempted proves the re-GET
// leniency is specific to a 404 from the PUT: any other failure (a real
// server error) is returned directly.
func TestClient_RemoveUserFromGroup_PUT500_NoReGetAttempted(t *testing.T) {
	var getCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCount++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.User{
				ID:         "user-1",
				UserGroups: []client.UserGroup{{ID: "group-remove"}},
			})
		case http.MethodPut:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": "Internal server error"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.RemoveUserFromGroup("user-1", "group-remove")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
	assert.Equal(t, 1, getCount, "a non-404 PUT failure must not trigger a re-GET")
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
		_, _ = fmt.Fprint(w, `{"error": "User not found", "code": "user_not_found"}`)
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
	assert.True(t, status.UserNotFound)
	assert.True(t, client.IsUserNotFound(err))
}

// TestClient_UserHasGroupMembership_GenericNotFound proves the P2 fix at the
// client.HTTPError level: a 404 that is not Pocket-ID's structured
// "user_not_found" body must not report UserNotFound.
func TestClient_UserHasGroupMembership_GenericNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "<html>404 Not Found</html>")
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.UserHasGroupMembership("some-user", "group-1")
	assert.Error(t, err)

	var status *client.HTTPError
	require.ErrorAs(t, err, &status)
	assert.False(t, status.UserNotFound)
	assert.False(t, client.IsUserNotFound(err))
}
