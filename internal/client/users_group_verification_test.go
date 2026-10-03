package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const verifyUserID = "77777777-7777-4777-8777-777777777777"

// usersGroupsVerifyServer behaves like Pocket ID's user-groups PUT: it keeps
// only requested IDs that name an existing group (UserService.UpdateUserGroups
// looks them up with "id IN ?") and answers with the user, unless
// unreadablePut is set, in which case the PUT's body is empty. readFails makes
// every GET of the user fail. extraHeld is added to whatever the PUT stores,
// as a concurrent writer would.
type usersGroupsVerifyServer struct {
	existing      []string
	current       []string
	extraHeld     []string
	unreadablePut bool
	readFails     bool
	// failGetsFrom, when positive, makes the GET with that number and every
	// later one fail.
	failGetsFrom int
	// getBody, when set, is the raw body of every GET of the user from the
	// GET numbered getBodyFrom on (from the first when 0).
	getBody     string
	getBodyFrom int
	// putBody, when set, is the raw body of every PUT's response.
	putBody    string
	puts, gets int
}

func (s *usersGroupsVerifyServer) start(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		userJSON := func() {
			groups := []client.UserGroup{}
			for _, id := range s.current {
				groups = append(groups, client.UserGroup{ID: id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": verifyUserID, "userGroups": groups})
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/users/" + verifyUserID:
			s.gets++
			if s.readFails || (s.failGetsFrom > 0 && s.gets >= s.failGetsFrom) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if s.getBody != "" && s.gets >= s.getBodyFrom {
				_, _ = w.Write([]byte(s.getBody))
				return
			}
			userJSON()
		case "PUT /api/users/" + verifyUserID + "/user-groups":
			s.puts++
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			s.current = nil
			for _, id := range req.UserGroupIDs {
				if slices.Contains(s.existing, id) {
					s.current = append(s.current, id)
				}
			}
			s.current = append(s.current, s.extraHeld...)
			if s.unreadablePut {
				return
			}
			if s.putBody != "" {
				_, _ = w.Write([]byte(s.putBody))
				return
			}
			userJSON()
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 5)
	require.NoError(t, err)
	return c
}

func TestClient_SetUserGroups_Verifies(t *testing.T) {
	ctx := context.Background()
	t.Run("exact", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1", "g2"}}
		held, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"g2", "g1"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"g1", "g2"}, held)
	})
	t.Run("empty", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1"}, current: []string{"g1"}}
		held, err := s.start(t).SetUserGroups(ctx, verifyUserID, nil)
		require.NoError(t, err)
		assert.Empty(t, held)
	})
	t.Run("missing_group_named", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1"}}
		held, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"g1", "gone"})
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, []string{"gone"}, mismatch.Missing)
		assert.Empty(t, mismatch.Unexpected)
		assert.Contains(t, err.Error(), "gone")
		assert.Equal(t, []string{"g1"}, held, "the held set is returned with the error")
		assert.False(t, errors.Is(err, client.ErrResultUnread), "the result is known")
	})
	t.Run("unexpected_group_named", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1"}, extraHeld: []string{"other"}}
		_, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"g1"})
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, []string{"other"}, mismatch.Unexpected)
	})
	t.Run("unreadable_response_read_back", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1"}, unreadablePut: true}
		_, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"g1", "gone"})
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch, "a GET reads the result back and the comparison still runs")
		assert.Equal(t, []string{"gone"}, mismatch.Missing)
		assert.Equal(t, 1, s.gets)
		assert.Equal(t, 1, s.puts, "the PUT is never repeated")
	})
	t.Run("unreadable_response_and_read_fails", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1"}, unreadablePut: true, readFails: true}
		_, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"g1"})
		require.ErrorIs(t, err, client.ErrResultUnread)
		assert.Equal(t, 1, s.puts)
	})
}

// A membership for a group that does not exist (or was deleted during the
// apply) is an error naming the group, never a recorded success; a removal
// the server did not make is an error too.
func TestClient_GroupMembershipChangesAreVerified(t *testing.T) {
	ctx := context.Background()
	t.Run("add_missing_group", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1"}, current: []string{"g1"}}
		err := s.start(t).AddUserToGroup(ctx, verifyUserID, "gone")
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, []string{"gone"}, mismatch.Missing)
		assert.Contains(t, err.Error(), "gone")
	})
	t.Run("add_missing_group_unreadable_response", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1"}, current: []string{"g1"}, unreadablePut: true}
		err := s.start(t).AddUserToGroup(ctx, verifyUserID, "gone")
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
	})
	t.Run("add_unconfirmed", func(t *testing.T) {
		// The PUT's body is unreadable and so is the read-back: the change
		// was made, but whether it took cannot be confirmed. The resource
		// keeps the membership in state then
		// (resources.TestGroupMembershipCreateUnverifiable).
		s := &usersGroupsVerifyServer{existing: []string{"g1", "g2"}, current: []string{"g1"}, unreadablePut: true, failGetsFrom: 2}
		err := s.start(t).AddUserToGroup(ctx, verifyUserID, "g2")
		require.ErrorIs(t, err, client.ErrResultUnread)
		assert.Equal(t, 1, s.puts)
	})
	t.Run("add_existing_group", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1", "g2"}, current: []string{"g1"}}
		require.NoError(t, s.start(t).AddUserToGroup(ctx, verifyUserID, "g2"))
		assert.ElementsMatch(t, []string{"g1", "g2"}, s.current)
	})
	t.Run("remove_not_applied", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1", "g2"}, current: []string{"g1", "g2"}, extraHeld: []string{"g2"}}
		err := s.start(t).RemoveUserFromGroup(ctx, verifyUserID, "g2")
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, []string{"g2"}, mismatch.Unexpected)
	})
	t.Run("remove_applied", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1", "g2"}, current: []string{"g1", "g2"}}
		require.NoError(t, s.start(t).RemoveUserFromGroup(ctx, verifyUserID, "g2"))
		assert.Equal(t, []string{"g1"}, s.current)
	})
}

// usersGroupsMalformedUserBodies are GET answers that do not show which
// groups the user is in. Explicit "userGroups": null does (none).
var usersGroupsMalformedUserBodies = map[string]string{
	"empty_object":     `{}`,
	"null":             `null`,
	"not_json":         `<html>ok</html>`,
	"no_user_groups":   `{"id":"` + verifyUserID + `"}`,
	"other_user":       `{"id":"88888888-8888-4888-8888-888888888888","userGroups":[]}`,
	"no_id":            `{"userGroups":[]}`,
	"groups_not_list":  `{"id":"` + verifyUserID + `","userGroups":"g1"}`,
	"group_without_id": `{"id":"` + verifyUserID + `","userGroups":[{"name":"g1"}]}`,
}

// A read-back after an unreadable PUT response is evidence only when it shows
// the user's groups; anything else leaves the result unknown, never an empty
// set that would make a removal or a clearing look done.
func TestClient_UserGroupsReadBackRequiresListedGroups(t *testing.T) {
	ctx := context.Background()
	for name, body := range usersGroupsMalformedUserBodies {
		t.Run("set_"+name, func(t *testing.T) {
			s := &usersGroupsVerifyServer{existing: []string{"g1"}, current: []string{"g1"}, unreadablePut: true, getBody: body}
			_, err := s.start(t).SetUserGroups(ctx, verifyUserID, nil)
			require.ErrorIs(t, err, client.ErrResultUnread)
			assert.Equal(t, 1, s.puts)
		})
		t.Run("remove_"+name, func(t *testing.T) {
			// The first GET (the snapshot) is a proper answer; the read-back
			// after the unreadable PUT is not.
			s := &usersGroupsVerifyServer{existing: []string{"g1"}, current: []string{"g1"}, unreadablePut: true, getBody: body, getBodyFrom: 2}
			err := s.start(t).RemoveUserFromGroup(ctx, verifyUserID, "g1")
			require.ErrorIs(t, err, client.ErrResultUnread, "a removal is not reported done without evidence")
		})
		t.Run("snapshot_"+name, func(t *testing.T) {
			// The list written is built from the snapshot; one that does not
			// show the groups must not become a write that drops them.
			s := &usersGroupsVerifyServer{existing: []string{"g1", "g2"}, current: []string{"g1"}, getBody: body}
			require.Error(t, s.start(t).AddUserToGroup(ctx, verifyUserID, "g2"))
			assert.Zero(t, s.puts, "nothing is written")
			assert.Equal(t, []string{"g1"}, s.current)
		})
		t.Run("membership_read_"+name, func(t *testing.T) {
			s := &usersGroupsVerifyServer{getBody: body}
			_, err := s.start(t).UserHasGroupMembership(ctx, verifyUserID, "g1")
			require.Error(t, err)
		})
	}
	t.Run("explicit_null_is_none", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"g1"}, current: []string{"g1"}, unreadablePut: true,
			getBody: `{"id":"` + verifyUserID + `","userGroups":null}`}
		held, err := s.start(t).SetUserGroups(ctx, verifyUserID, nil)
		require.NoError(t, err)
		assert.Empty(t, held)
	})
	// A PUT response that does not list the groups is followed by a read.
	for name, body := range map[string]string{"empty_object": `{}`, "groups_not_list": `{"userGroups":"g1"}`, "not_json": `<html>ok</html>`} {
		t.Run("put_"+name, func(t *testing.T) {
			s := &usersGroupsVerifyServer{existing: []string{"g1"}, putBody: body}
			held, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"g1"})
			require.NoError(t, err)
			assert.Equal(t, []string{"g1"}, held)
			assert.Equal(t, 1, s.gets, "the result was read back")
		})
	}
}
