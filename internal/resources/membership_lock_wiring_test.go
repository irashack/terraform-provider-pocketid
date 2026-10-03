package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// membershipLockHeld reports whether some holder has the membership lock
// right now.
func membershipLockHeld() bool {
	if membershipWriteMu.TryLock() {
		membershipWriteMu.Unlock()
		return false
	}
	return true
}

// membershipLockServer is one user (membershipVerifyUserID) whose group
// writes behave like Pocket ID's, which records every request in order with
// whether the membership lock was held while the server handled it.
type membershipLockServer struct {
	mu     sync.Mutex
	groups []string
	events []membershipLockEvent
}

type membershipLockEvent struct {
	call   string
	locked bool
}

func (s *membershipLockServer) start(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		locked := membershipLockHeld()
		s.mu.Lock()
		defer s.mu.Unlock()
		call := r.Method + " " + r.URL.Path
		s.events = append(s.events, membershipLockEvent{call, locked})
		w.Header().Set("Content-Type", "application/json")
		writeUser := func() {
			groups := []map[string]string{}
			for _, id := range s.groups {
				groups = append(groups, map[string]string{"id": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": membershipVerifyUserID, "username": "fixture", "email": "fixture@example.invalid",
				"firstName": "F", "lastName": "L", "displayName": "F L", "userGroups": groups, "customClaims": []any{},
			})
		}
		switch call {
		case "GET /api/version/current":
			_, _ = w.Write([]byte(`{"currentVersion":"2.17.0"}`))
		case "POST /api/users":
			var req client.UserCreateRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			s.groups = append([]string(nil), req.UserGroupIDs...)
			w.WriteHeader(http.StatusCreated)
			writeUser()
		case "GET /api/users/" + membershipVerifyUserID, "PUT /api/users/" + membershipVerifyUserID:
			writeUser()
		case "PUT /api/users/" + membershipVerifyUserID + "/user-groups":
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			s.groups = append([]string(nil), req.UserGroupIDs...)
			writeUser()
		case "PUT /api/custom-claims/user/" + membershipVerifyUserID:
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request %s", call)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c
}

func (s *membershipLockServer) recorded() []membershipLockEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]membershipLockEvent(nil), s.events...)
}

func membershipLockUser(groups ...string) userResourceModel {
	set := types.SetNull(types.StringType)
	if len(groups) > 0 {
		values := make([]attr.Value, 0, len(groups))
		for _, id := range groups {
			values = append(values, types.StringValue(id))
		}
		set = types.SetValueMust(types.StringType, values)
	}
	return userResourceModel{
		ID: types.StringValue(membershipVerifyUserID), Username: types.StringValue("fixture"),
		Email: types.StringValue("fixture@example.invalid"), FirstName: types.StringValue("F"), LastName: types.StringValue("L"),
		DisplayName: types.StringValue("F L"), EmailVerified: types.BoolValue(false), IsAdmin: types.BoolValue(false),
		Locale: types.StringNull(), Disabled: types.BoolValue(false),
		Groups: set, CustomClaims: types.MapNull(types.StringType), UnresolvedCreation: types.BoolNull(),
	}
}

func membershipLockUserUpdate(t *testing.T, c *client.Client, prior, planned userResourceModel) resource.UpdateResponse {
	t.Helper()
	ctx := context.Background()
	r := &userResource{client: c}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	state := tfsdk.State{Schema: sr.Schema}
	require.False(t, state.Set(ctx, &prior).HasError())
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &planned).HasError())
	resp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config{Schema: sr.Schema, Raw: plan.Raw}}, &resp)
	return resp
}

func membershipLockMembershipCreate(t *testing.T, c *client.Client, groupID string) resource.CreateResponse {
	t.Helper()
	ctx := context.Background()
	r := &groupMembershipResource{client: c}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &groupMembershipResourceModel{
		ID: types.StringUnknown(), GroupID: types.StringValue(groupID), UserID: types.StringValue(membershipVerifyUserID),
		UnresolvedCreation: types.BoolUnknown(),
	}).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	return resp
}

// Every request that reads, writes or verifies a user's groups for
// pocketid_user (create and update) and pocketid_group_membership (create
// and delete) runs under the membership lock that pocketid_group_members
// takes.
func TestMembershipLock_UserAndMembershipHoldItForTheirWholeSequence(t *testing.T) {
	ctx := context.Background()
	groupsCall := "PUT /api/users/" + membershipVerifyUserID + "/user-groups"
	userRead := "GET /api/users/" + membershipVerifyUserID
	requireLocked := func(t *testing.T, events []membershipLockEvent, calls ...string) {
		t.Helper()
		for _, call := range calls {
			seen := false
			for _, e := range events {
				if e.call == call {
					seen = true
					assert.True(t, e.locked, "%s runs under the membership lock", call)
				}
			}
			assert.True(t, seen, "%s was requested", call)
		}
		assert.False(t, membershipLockHeld(), "the lock is released afterwards")
	}

	t.Run("pocketid_user create", func(t *testing.T) {
		s := &membershipLockServer{}
		r := &userResource{client: s.start(t)}
		sr := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		planned := membershipLockUser(membershipVerifyGroupID)
		planned.ID = types.StringUnknown()
		plan := tfsdk.Plan{Schema: sr.Schema}
		require.False(t, plan.Set(ctx, &planned).HasError())
		resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
		r.Create(ctx, resource.CreateRequest{Plan: plan, Config: tfsdk.Config{Schema: sr.Schema, Raw: plan.Raw}}, &resp)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		requireLocked(t, s.recorded(), "POST /api/users")
	})
	t.Run("pocketid_user update", func(t *testing.T) {
		s := &membershipLockServer{groups: []string{membershipVerifyExistingID}}
		resp := membershipLockUserUpdate(t, s.start(t), membershipLockUser(membershipVerifyExistingID), membershipLockUser(membershipVerifyGroupID))
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		requireLocked(t, s.recorded(), groupsCall)
	})
	t.Run("pocketid_group_membership create", func(t *testing.T) {
		s := &membershipLockServer{groups: []string{membershipVerifyExistingID}}
		resp := membershipLockMembershipCreate(t, s.start(t), membershipVerifyGroupID)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		requireLocked(t, s.recorded(), userRead, groupsCall)
	})
	t.Run("pocketid_group_membership delete", func(t *testing.T) {
		s := &membershipLockServer{groups: []string{membershipVerifyExistingID, membershipVerifyGroupID}}
		c := s.start(t)
		r := &groupMembershipResource{client: c}
		sr := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		state := tfsdk.State{Schema: sr.Schema}
		require.False(t, state.Set(ctx, &groupMembershipResourceModel{
			ID:      types.StringValue(groupMembershipID(membershipVerifyGroupID, membershipVerifyUserID)),
			GroupID: types.StringValue(membershipVerifyGroupID), UserID: types.StringValue(membershipVerifyUserID),
			UnresolvedCreation: types.BoolNull(),
		}).HasError())
		resp := resource.DeleteResponse{State: state}
		r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		requireLocked(t, s.recorded(), userRead, groupsCall)
	})
}

// While pocketid_group_members holds the membership lock (as its Create,
// Update and Delete do around their whole read, write and verification), a
// pocketid_user update and a pocketid_group_membership create of the same
// user wait: neither reads nor writes the user's groups until it is
// released. Then each runs its sequence whole, one after the other, and
// neither loses the other's change.
func TestMembershipLock_TheThreeResourceTypesSerialize(t *testing.T) {
	s := &membershipLockServer{groups: []string{membershipVerifyExistingID}}
	c := s.start(t)
	groupsCall := "PUT /api/users/" + membershipVerifyUserID + "/user-groups"

	release := lockMembershipWrites() // what pocketid_group_members holds
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	userDone := make(chan resource.UpdateResponse, 1)
	go func() {
		// The user's planned groups include the membership's group, as an
		// operator managing both would plan them.
		userDone <- membershipLockUserUpdate(t, c,
			membershipLockUser(membershipVerifyExistingID),
			membershipLockUser(membershipVerifyExistingID, membershipVerifyGroupID))
	}()
	membershipDone := make(chan resource.CreateResponse, 1)
	go func() { membershipDone <- membershipLockMembershipCreate(t, c, membershipVerifyGroupID) }()

	time.Sleep(300 * time.Millisecond)
	for _, e := range s.recorded() {
		assert.NotEqual(t, groupsCall, e.call, "no group write while pocketid_group_members holds the lock")
	}
	release()
	released = true

	for range 2 {
		select {
		case resp := <-userDone:
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		case resp := <-membershipDone:
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		case <-time.After(10 * time.Second):
			t.Fatal("the resources did not finish: deadlock")
		}
	}
	for _, e := range s.recorded() {
		if e.call == groupsCall {
			assert.True(t, e.locked, "every group write ran under the lock")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.True(t, slices.Contains(s.groups, membershipVerifyGroupID))
	assert.True(t, slices.Contains(s.groups, membershipVerifyExistingID))
}

// pocketid_group_members states that it conflicts with the other two
// writers of a group's membership; they say the same of it.
func TestMembershipExclusivityIsDocumented(t *testing.T) {
	ctx := context.Background()
	membership := resource.SchemaResponse{}
	(&groupMembershipResource{}).Schema(ctx, resource.SchemaRequest{}, &membership)
	assert.Contains(t, membership.Schema.MarkdownDescription, "Do not combine with `pocketid_group_members` on the same group")

	user := resource.SchemaResponse{}
	(&userResource{}).Schema(ctx, resource.SchemaRequest{}, &user)
	groups := user.Schema.Attributes["groups"].GetDescription()
	assert.Contains(t, groups, "Do not list a group whose members `pocketid_group_members` manages")

	members := resource.SchemaResponse{}
	(&groupMembersResource{}).Schema(ctx, resource.SchemaRequest{}, &members)
	assert.Contains(t, members.Schema.MarkdownDescription, "do not use this resource together with `pocketid_group_membership`")
	assert.Contains(t, members.Schema.MarkdownDescription, "with the `groups` attribute of `pocketid_user`")
}
