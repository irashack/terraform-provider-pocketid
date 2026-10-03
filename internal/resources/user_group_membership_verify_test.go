package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	membershipVerifyUserID     = "55555555-5555-4555-8555-555555555555"
	membershipVerifyGroupID    = "66666666-6666-4666-8666-666666666666"
	membershipVerifyMissingID  = "77777777-7777-4777-8777-777777777777"
	membershipVerifyExistingID = "88888888-8888-4888-8888-888888888888"
)

// usersGroupsMembershipServer is a user whose group writes behave like Pocket
// ID's: a requested ID that names no existing group is dropped without an
// error, and the PUT answers with the user.
func usersGroupsMembershipServer(t *testing.T, existing, current []string) (*client.Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		writeUser := func() {
			groups := []map[string]string{}
			for _, id := range current {
				groups = append(groups, map[string]string{"id": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": membershipVerifyUserID, "username": "fixture", "email": "fixture@example.invalid",
				"firstName": "F", "lastName": "L", "displayName": "F L", "userGroups": groups, "customClaims": []any{},
			})
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/users/" + membershipVerifyUserID, "PUT /api/users/" + membershipVerifyUserID:
			writeUser()
		case "PUT /api/users/" + membershipVerifyUserID + "/user-groups":
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			current = nil
			for _, id := range req.UserGroupIDs {
				if slices.Contains(existing, id) {
					current = append(current, id)
				}
			}
			writeUser()
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c, &current
}

// Adding a user to a group that does not exist is an error naming the group,
// and nothing is recorded.
func TestGroupMembershipCreateMissingGroup(t *testing.T) {
	ctx := context.Background()
	c, current := usersGroupsMembershipServer(t, []string{membershipVerifyExistingID}, []string{membershipVerifyExistingID})
	r := &groupMembershipResource{client: c}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &groupMembershipResourceModel{
		ID: types.StringUnknown(), GroupID: types.StringValue(membershipVerifyMissingID), UserID: types.StringValue(membershipVerifyUserID),
	}).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	require.True(t, resp.Diagnostics.HasError())
	require.Contains(t, resp.Diagnostics.Errors()[0].Detail(), membershipVerifyMissingID)
	require.True(t, resp.State.Raw.IsNull(), "no membership is recorded")
	require.Equal(t, []string{membershipVerifyExistingID}, *current, "other memberships are kept")
}

// A pocketid_user update whose groups include one that does not exist fails,
// names the group, and records the groups the user is actually in.
func TestUserUpdateMissingGroup(t *testing.T) {
	ctx := context.Background()
	c, _ := usersGroupsMembershipServer(t, []string{membershipVerifyGroupID}, nil)
	r := &userResource{client: c}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	base := userResourceModel{
		ID: types.StringValue(membershipVerifyUserID), Username: types.StringValue("fixture"),
		Email: types.StringValue("fixture@example.invalid"), FirstName: types.StringValue("F"), LastName: types.StringValue("L"),
		DisplayName: types.StringValue("F L"), EmailVerified: types.BoolValue(false), IsAdmin: types.BoolValue(false),
		Locale: types.StringNull(), Disabled: types.BoolValue(false),
		Groups: types.SetNull(types.StringType), CustomClaims: types.MapNull(types.StringType),
	}
	prior := tfsdk.State{Schema: sr.Schema}
	require.False(t, prior.Set(ctx, &base).HasError())
	planned := base
	planned.Groups = types.SetValueMust(types.StringType, []attr.Value{
		types.StringValue(membershipVerifyGroupID), types.StringValue(membershipVerifyMissingID),
	})
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &planned).HasError())

	resp := resource.UpdateResponse{State: prior}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: prior}, &resp)
	require.True(t, resp.Diagnostics.HasError())
	require.Contains(t, resp.Diagnostics.Errors()[0].Detail(), membershipVerifyMissingID)
	var state userResourceModel
	require.False(t, resp.State.Get(ctx, &state).HasError())
	var held []string
	require.False(t, state.Groups.ElementsAs(ctx, &held, false).HasError())
	require.Equal(t, []string{membershipVerifyGroupID}, held, "state records the groups the server holds")
}
