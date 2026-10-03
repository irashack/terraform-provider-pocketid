package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

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
	putBody string
	// getStatus, when set, is the status of the GETs that fail (403 when 0);
	// putStatus, when set, answers every PUT with it and applies nothing.
	getStatus, putStatus int
	puts, gets           int
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
				status := http.StatusForbidden
				if s.getStatus != 0 {
					status = s.getStatus
				}
				w.WriteHeader(status)
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
			if s.putStatus != 0 {
				w.WriteHeader(s.putStatus)
				return
			}
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
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}}
		held, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000002", "bbbbbbbb-0000-4000-8000-000000000001"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, held)
	})
	t.Run("empty", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}}
		held, err := s.start(t).SetUserGroups(ctx, verifyUserID, nil)
		require.NoError(t, err)
		assert.Empty(t, held)
	})
	t.Run("missing_group_named", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}}
		held, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000001", "gone"})
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, []string{"gone"}, mismatch.Missing)
		assert.Empty(t, mismatch.Unexpected)
		assert.Contains(t, err.Error(), "gone")
		assert.Equal(t, []string{"bbbbbbbb-0000-4000-8000-000000000001"}, held, "the held set is returned with the error")
		assert.False(t, errors.Is(err, client.ErrResultUnread), "the result is known")
	})
	t.Run("unexpected_group_named", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, extraHeld: []string{"bbbbbbbb-0000-4000-8000-0000000000ff"}}
		_, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000001"})
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, []string{"bbbbbbbb-0000-4000-8000-0000000000ff"}, mismatch.Unexpected)
	})
	t.Run("unreadable_response_read_back", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true}
		_, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000001", "gone"})
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch, "a GET reads the result back and the comparison still runs")
		assert.Equal(t, []string{"gone"}, mismatch.Missing)
		assert.Equal(t, 1, s.gets)
		assert.Equal(t, 1, s.puts, "the PUT is never repeated")
	})
	t.Run("unreadable_response_and_read_fails", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true, readFails: true}
		_, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000001"})
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
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}}
		err := s.start(t).AddUserToGroup(ctx, verifyUserID, "gone")
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, []string{"gone"}, mismatch.Missing)
		assert.Contains(t, err.Error(), "gone")
	})
	t.Run("add_missing_group_unreadable_response", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true}
		err := s.start(t).AddUserToGroup(ctx, verifyUserID, "gone")
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
	})
	t.Run("add_unconfirmed", func(t *testing.T) {
		// The PUT's body is unreadable and so is the read-back: the change
		// was made, but whether it took cannot be confirmed. The resource
		// keeps the membership in state then
		// (resources.TestGroupMembershipCreateUnverifiable).
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true, failGetsFrom: 2}
		err := s.start(t).AddUserToGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002")
		require.ErrorIs(t, err, client.ErrResultUnread)
		assert.Equal(t, 1, s.puts)
	})
	t.Run("add_existing_group", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}}
		require.NoError(t, s.start(t).AddUserToGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002"))
		assert.ElementsMatch(t, []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, s.current)
	})
	t.Run("remove_not_applied", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, extraHeld: []string{"bbbbbbbb-0000-4000-8000-000000000002"}}
		err := s.start(t).RemoveUserFromGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002")
		var mismatch *client.UserGroupsMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, []string{"bbbbbbbb-0000-4000-8000-000000000002"}, mismatch.Unexpected)
	})
	t.Run("remove_applied", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}}
		require.NoError(t, s.start(t).RemoveUserFromGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002"))
		assert.Equal(t, []string{"bbbbbbbb-0000-4000-8000-000000000001"}, s.current)
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
	"groups_not_list":  `{"id":"` + verifyUserID + `","userGroups":"bbbbbbbb-0000-4000-8000-000000000001"}`,
	"group_without_id": `{"id":"` + verifyUserID + `","userGroups":[{"name":"bbbbbbbb-0000-4000-8000-000000000001"}]}`,
}

// A read-back after an unreadable PUT response is evidence only when it shows
// the user's groups; anything else leaves the result unknown, never an empty
// set that would make a removal or a clearing look done.
func TestClient_UserGroupsReadBackRequiresListedGroups(t *testing.T) {
	ctx := context.Background()
	for name, body := range usersGroupsMalformedUserBodies {
		t.Run("set_"+name, func(t *testing.T) {
			s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true, getBody: body}
			_, err := s.start(t).SetUserGroups(ctx, verifyUserID, nil)
			require.ErrorIs(t, err, client.ErrResultUnread)
			assert.Equal(t, 1, s.puts)
		})
		t.Run("remove_"+name, func(t *testing.T) {
			// The first GET (the snapshot) is a proper answer; the read-back
			// after the unreadable PUT is not.
			s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true, getBody: body, getBodyFrom: 2}
			err := s.start(t).RemoveUserFromGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000001")
			require.ErrorIs(t, err, client.ErrResultUnread, "a removal is not reported done without evidence")
		})
		t.Run("snapshot_"+name, func(t *testing.T) {
			// The list written is built from the snapshot; one that does not
			// show the groups must not become a write that drops them.
			s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, getBody: body}
			require.Error(t, s.start(t).AddUserToGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002"))
			assert.Zero(t, s.puts, "nothing is written")
			assert.Equal(t, []string{"bbbbbbbb-0000-4000-8000-000000000001"}, s.current)
		})
		t.Run("membership_read_"+name, func(t *testing.T) {
			s := &usersGroupsVerifyServer{getBody: body}
			_, err := s.start(t).UserHasGroupMembership(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000001")
			require.Error(t, err)
		})
	}
	t.Run("explicit_null_is_none", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true,
			getBody: `{"id":"` + verifyUserID + `","userGroups":null}`}
		held, err := s.start(t).SetUserGroups(ctx, verifyUserID, nil)
		require.NoError(t, err)
		assert.Empty(t, held)
	})
	// A PUT response that does not list the groups is followed by a read.
	for name, body := range map[string]string{"empty_object": `{}`, "groups_not_list": `{"userGroups":"bbbbbbbb-0000-4000-8000-000000000001"}`, "not_json": `<html>ok</html>`} {
		t.Run("put_"+name, func(t *testing.T) {
			s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, putBody: body}
			held, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000001"})
			require.NoError(t, err)
			assert.Equal(t, []string{"bbbbbbbb-0000-4000-8000-000000000001"}, held)
			assert.Equal(t, 1, s.gets, "the result was read back")
		})
	}
}

// A PUT response that lists groups without being evidence of this user's
// groups (it names another user, or no user, or a group without an ID) is not
// believed: the result is read back with a GET, and the resulting set is what
// a membership change reports. A removal never reports done on such a body.
func TestClient_UserGroupsPutEvidenceMustNameTheUser(t *testing.T) {
	ctx := context.Background()
	const other = "88888888-8888-4888-8888-888888888888"
	for name, body := range map[string]string{
		"other_user_empty_list": `{"id":"` + other + `","userGroups":[]}`,
		"other_user_null":       `{"id":"` + other + `","userGroups":null}`,
		"no_user_id":            `{"userGroups":[]}`,
		"group_without_id":      `{"id":"` + verifyUserID + `","userGroups":[{}]}`,
	} {
		t.Run("remove_not_applied_"+name, func(t *testing.T) {
			// The server did not remove g2 (a concurrent writer held it), and
			// the bogus response claims nobody is in any group.
			s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, extraHeld: []string{"bbbbbbbb-0000-4000-8000-000000000002"}, putBody: body}
			err := s.start(t).RemoveUserFromGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002")
			var mismatch *client.UserGroupsMismatchError
			require.ErrorAs(t, err, &mismatch, "the read-back, not the response, decides")
			assert.Equal(t, []string{"bbbbbbbb-0000-4000-8000-000000000002"}, mismatch.Unexpected)
			assert.Equal(t, 2, s.gets, "the snapshot and the read-back")
			assert.Equal(t, 1, s.puts, "the PUT is never repeated")
		})
		t.Run("add_applied_"+name, func(t *testing.T) {
			// The server added g2, and the bogus response lists nothing, which
			// must not make a real addition look like a missing group.
			s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, putBody: body}
			require.NoError(t, s.start(t).AddUserToGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002"))
			assert.Equal(t, 2, s.gets)
			assert.Equal(t, 1, s.puts)
		})
		t.Run("set_"+name, func(t *testing.T) {
			s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, putBody: body}
			held, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000001"})
			require.NoError(t, err)
			assert.Equal(t, []string{"bbbbbbbb-0000-4000-8000-000000000001"}, held)
			assert.Equal(t, 1, s.gets, "the result was read back")
		})
		t.Run("unconfirmed_when_read_back_fails_"+name, func(t *testing.T) {
			s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, putBody: body, failGetsFrom: 1}
			_, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000001"})
			require.ErrorIs(t, err, client.ErrResultUnread)
			assert.Equal(t, 1, s.puts)
		})
	}
	t.Run("error_does_not_echo_the_body", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, putBody: `{"id":"` + other + `","userGroups":[{"id":"","name":"canary-text"}]}`, failGetsFrom: 1}
		_, err := s.start(t).SetUserGroups(ctx, verifyUserID, []string{"bbbbbbbb-0000-4000-8000-000000000001"})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "canary-text")
	})
}

// A membership change says whether its write was attempted: a failure of the
// read that precedes the PUT (a refused or failed request, a server error
// that outlasts the retries, a malformed answer) wraps ErrWriteNotAttempted,
// with no PUT sent; a failure of the PUT or of reading its result back does not.
func TestClient_GroupMembershipChangeReportsWhetherTheWriteWasAttempted(t *testing.T) {
	ctx := context.Background()
	type change struct {
		name string
		run  func(*client.Client) error
	}
	changes := []change{
		{"add", func(c *client.Client) error {
			return c.AddUserToGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002")
		}},
		{"remove", func(c *client.Client) error {
			return c.RemoveUserFromGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000001")
		}},
	}
	for _, ch := range changes {
		t.Run(ch.name+"_preflight_failures", func(t *testing.T) {
			for name, s := range map[string]*usersGroupsVerifyServer{
				"forbidden":    {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, readFails: true},
				"server_error": {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, readFails: true, getStatus: http.StatusBadGateway},
				"empty_object": {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, getBody: `{}`},
				"other_user":   {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, getBody: `{"id":"88888888-8888-4888-8888-888888888888","userGroups":[]}`},
				"not_json":     {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, getBody: `<html>ok</html>`},
				"group_no_id":  {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, getBody: `{"id":"` + verifyUserID + `","userGroups":[{}]}`},
			} {
				t.Run(name, func(t *testing.T) {
					c := s.start(t)
					client.SetRetryPolicyForTest(c, 2, time.Millisecond, 2*time.Millisecond, time.Second)
					err := ch.run(c)
					require.Error(t, err)
					assert.ErrorIs(t, err, client.ErrWriteNotAttempted)
					assert.NotErrorIs(t, err, client.ErrResultUnread)
					assert.Zero(t, s.puts, "no write was sent")
				})
			}
		})
	}
	t.Run("add_after_the_write_was_sent", func(t *testing.T) {
		for name, s := range map[string]*usersGroupsVerifyServer{
			"put_server_error":               {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, putStatus: http.StatusBadGateway},
			"put_rejected":                   {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, putStatus: http.StatusBadRequest},
			"unreadable_and_no_read":         {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true, failGetsFrom: 2},
			"unreadable_read_back_malformed": {existing: []string{"bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, unreadablePut: true, getBody: `{}`, getBodyFrom: 2},
		} {
			t.Run(name, func(t *testing.T) {
				err := s.start(t).AddUserToGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000002")
				require.Error(t, err)
				assert.NotErrorIs(t, err, client.ErrWriteNotAttempted)
				assert.Equal(t, 1, s.puts)
			})
		}
	})
	t.Run("remove_after_the_write_was_sent", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, putStatus: http.StatusBadGateway}
		err := s.start(t).RemoveUserFromGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000001")
		require.Error(t, err)
		assert.NotErrorIs(t, err, client.ErrWriteNotAttempted)
		assert.Equal(t, 1, s.puts)
	})
	t.Run("missing_user_is_still_recognized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"User not found","code":"user_not_found"}`))
		}))
		defer server.Close()
		c, err := client.NewClient(server.URL, "test-token", false, 5)
		require.NoError(t, err)
		err = c.AddUserToGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000001")
		require.Error(t, err)
		assert.ErrorIs(t, err, client.ErrWriteNotAttempted)
		assert.True(t, client.IsUserNotFound(err), "the cause is still visible")
		assert.NoError(t, c.RemoveUserFromGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000001"), "removing from a missing user is already done")
	})
	t.Run("nothing_to_write_is_no_error", func(t *testing.T) {
		s := &usersGroupsVerifyServer{existing: []string{"bbbbbbbb-0000-4000-8000-000000000001"}, current: []string{"bbbbbbbb-0000-4000-8000-000000000001"}}
		require.NoError(t, s.start(t).AddUserToGroup(ctx, verifyUserID, "bbbbbbbb-0000-4000-8000-000000000001"))
		assert.Zero(t, s.puts)
	})
}
