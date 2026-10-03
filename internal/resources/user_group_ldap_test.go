package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	ldapFixtureUserID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	ldapFixtureGroupID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// usersGroupsLDAPServer serves one user and one group that carry an LDAP ID,
// with LDAP enabled or not, and refuses group updates and deletes the way
// Pocket ID does while LDAP is enabled. It counts every request.
func usersGroupsLDAPServer(t *testing.T, ldapEnabled bool) (*client.Client, map[string]int) {
	t.Helper()
	var mu sync.Mutex
	calls := map[string]int{}
	user := map[string]any{
		// The directory's display name is not first and last name joined.
		"id": ldapFixtureUserID, "username": "ldap.user", "email": "ldap@example.invalid", "firstName": "L",
		"lastName": "User", "displayName": "Directory Name", "ldapId": "uid=ldap", "userGroups": []any{}, "customClaims": []any{},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := r.Method + " " + r.URL.Path
		calls[key]++
		w.Header().Set("Content-Type", "application/json")
		switch key {
		case "GET /api/application-configuration":
			_, _ = fmt.Fprintf(w, `[{"key":"appName","type":"string","value":"Pocket ID"},{"key":"ldapEnabled","type":"bool","value":"%t"}]`, ldapEnabled)
		case "GET /api/users/" + ldapFixtureUserID:
			_ = json.NewEncoder(w).Encode(user)
		case "PUT /api/users/" + ldapFixtureUserID:
			var req map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			lastLDAPUserPut = req
			if !ldapEnabled {
				for k, v := range req {
					user[k] = v
				}
			} else {
				user["locale"] = req["locale"]
			}
			_ = json.NewEncoder(w).Encode(user)
		case "PUT /api/custom-claims/user/" + ldapFixtureUserID:
			var claims []client.CustomClaim
			require.NoError(t, json.NewDecoder(r.Body).Decode(&claims))
			lastLDAPClaimsPut = claims
			user["customClaims"] = claims
			_ = json.NewEncoder(w).Encode(claims)
		case "PUT /api/users/" + ldapFixtureUserID + "/user-groups":
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			lastLDAPGroupsPut = req.UserGroupIDs
			groups := []map[string]string{}
			for _, id := range req.UserGroupIDs {
				groups = append(groups, map[string]string{"id": id})
			}
			user["userGroups"] = groups
			_ = json.NewEncoder(w).Encode(user)
		case "PUT /api/custom-claims/user-group/" + ldapFixtureGroupID:
			_, _ = fmt.Fprint(w, `[{"key":"team","value":"a"}]`)
		case "PUT /api/user-groups/" + ldapFixtureGroupID, "DELETE /api/user-groups/" + ldapFixtureGroupID:
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error":"LDAP user groups can't be updated","code":"ldap_user_group_update"}`)
		case "DELETE /api/users/" + ldapFixtureUserID:
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error":"LDAP users can't be updated","code":"ldap_user_update"}`)
		default:
			t.Errorf("unexpected request %s", key)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c, calls
}

// The bodies of the last user, claims and groups PUTs usersGroupsLDAPServer
// received; tests reset them before the call they examine.
var (
	lastLDAPUserPut   map[string]any
	lastLDAPClaimsPut []client.CustomClaim
	lastLDAPGroupsPut []string
)

func ldapUserModel() userResourceModel {
	return userResourceModel{
		ID: types.StringValue(ldapFixtureUserID), Username: types.StringValue("ldap.user"),
		Email: types.StringValue("ldap@example.invalid"), FirstName: types.StringValue("L"), LastName: types.StringValue("User"),
		DisplayName: types.StringValue("Directory Name"), EmailVerified: types.BoolValue(false), IsAdmin: types.BoolValue(false),
		Locale: types.StringNull(), Disabled: types.BoolValue(false),
		Groups: types.SetNull(types.StringType), CustomClaims: types.MapNull(types.StringType),
	}
}

func runLDAPUserUpdate(t *testing.T, c *client.Client, change func(*userResourceModel)) resource.UpdateResponse {
	t.Helper()
	ctx := context.Background()
	r := &userResource{client: c}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	prior := tfsdk.State{Schema: sr.Schema}
	base := ldapUserModel()
	require.False(t, prior.Set(ctx, &base).HasError())
	planned := ldapUserModel()
	change(&planned)
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &planned).HasError())
	resp := resource.UpdateResponse{State: prior}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: prior}, &resp)
	return resp
}

// A change Pocket ID would silently drop for an LDAP user is refused before
// anything is written; the locale, which it does apply, goes through.
func TestUserUpdateLDAPManaged(t *testing.T) {
	t.Run("restricted_change_refused", func(t *testing.T) {
		c, calls := usersGroupsLDAPServer(t, true)
		resp := runLDAPUserUpdate(t, c, func(m *userResourceModel) {
			m.Username = types.StringValue("renamed")
			m.IsAdmin = types.BoolValue(true)
		})
		require.True(t, resp.Diagnostics.HasError())
		require.Equal(t, "User is managed by LDAP", resp.Diagnostics.Errors()[0].Summary())
		require.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "username, is_admin")
		require.Zero(t, calls["PUT /api/users/"+ldapFixtureUserID], "nothing is written")
	})
	// On any update Terraform plans the unconfigured, Computed display_name
	// as unknown. That is no requested change: the directory's display name
	// is kept, not replaced by first and last name. Each of the changes
	// Pocket ID does apply to an LDAP user is made, and only that one.
	for name, tc := range map[string]struct {
		change func(*userResourceModel)
		// want checks what was sent and what state records.
		want func(t *testing.T, calls map[string]int, state userResourceModel)
	}{
		"locale": {
			change: func(m *userResourceModel) { m.Locale = types.StringValue("fr") },
			want: func(t *testing.T, calls map[string]int, state userResourceModel) {
				require.Equal(t, "fr", lastLDAPUserPut["locale"], "the changed locale is sent")
				require.Equal(t, "fr", state.Locale.ValueString(), "and recorded")
				require.Zero(t, calls["PUT /api/custom-claims/user/"+ldapFixtureUserID], "no claims are written")
				require.Zero(t, calls["PUT /api/users/"+ldapFixtureUserID+"/user-groups"], "no groups are written")
				require.True(t, state.CustomClaims.IsNull())
				require.True(t, state.Groups.IsNull())
			},
		},
		"claims": {
			change: func(m *userResourceModel) {
				m.CustomClaims = types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("a")})
			},
			want: func(t *testing.T, calls map[string]int, state userResourceModel) {
				require.Equal(t, []client.CustomClaim{{Key: "team", Value: "a"}}, lastLDAPClaimsPut, "the changed claims are sent")
				require.Equal(t, types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("a")}), state.CustomClaims, "and recorded")
				require.Zero(t, calls["PUT /api/users/"+ldapFixtureUserID+"/user-groups"], "no groups are written")
				require.True(t, state.Locale.IsNull())
			},
		},
		"groups": {
			change: func(m *userResourceModel) {
				m.Groups = types.SetValueMust(types.StringType, []attr.Value{types.StringValue(ldapFixtureGroupID)})
			},
			want: func(t *testing.T, calls map[string]int, state userResourceModel) {
				require.Equal(t, 1, calls["PUT /api/users/"+ldapFixtureUserID+"/user-groups"])
				require.Equal(t, []string{ldapFixtureGroupID}, lastLDAPGroupsPut, "the added group is sent")
				require.Equal(t, types.SetValueMust(types.StringType, []attr.Value{types.StringValue(ldapFixtureGroupID)}), state.Groups, "and recorded")
				require.Zero(t, calls["PUT /api/custom-claims/user/"+ldapFixtureUserID], "no claims are written")
				require.True(t, state.Locale.IsNull())
			},
		},
	} {
		t.Run(name+"_allowed_with_unknown_display_name", func(t *testing.T) {
			c, calls := usersGroupsLDAPServer(t, true)
			lastLDAPUserPut, lastLDAPClaimsPut, lastLDAPGroupsPut = nil, nil, nil
			resp := runLDAPUserUpdate(t, c, func(m *userResourceModel) {
				tc.change(m)
				m.DisplayName = types.StringUnknown()
			})
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			require.Equal(t, 1, calls["PUT /api/users/"+ldapFixtureUserID])
			require.Equal(t, "Directory Name", lastLDAPUserPut["displayName"], "the directory's display name is sent back unchanged")
			var state userResourceModel
			require.False(t, resp.State.Get(context.Background(), &state).HasError())
			require.Equal(t, "Directory Name", state.DisplayName.ValueString())
			tc.want(t, calls, state)
		})
	}
	// Null and an empty set are no change of groups: nothing is written.
	t.Run("groups_null_to_empty_writes_nothing", func(t *testing.T) {
		c, calls := usersGroupsLDAPServer(t, true)
		resp := runLDAPUserUpdate(t, c, func(m *userResourceModel) {
			m.Groups = types.SetValueMust(types.StringType, []attr.Value{})
			m.DisplayName = types.StringUnknown()
		})
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		require.Zero(t, calls["PUT /api/users/"+ldapFixtureUserID+"/user-groups"])
	})
	t.Run("explicit_display_name_refused", func(t *testing.T) {
		c, calls := usersGroupsLDAPServer(t, true)
		resp := runLDAPUserUpdate(t, c, func(m *userResourceModel) { m.DisplayName = types.StringValue("Renamed") })
		require.True(t, resp.Diagnostics.HasError())
		require.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "display_name")
		require.Zero(t, calls["PUT /api/users/"+ldapFixtureUserID])
	})
	t.Run("ldap_disabled", func(t *testing.T) {
		c, calls := usersGroupsLDAPServer(t, false)
		resp := runLDAPUserUpdate(t, c, func(m *userResourceModel) { m.Username = types.StringValue("renamed") })
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		require.Equal(t, 1, calls["PUT /api/users/"+ldapFixtureUserID])
	})
	t.Run("delete_refused_explained", func(t *testing.T) {
		c, _ := usersGroupsLDAPServer(t, true)
		ctx := context.Background()
		r := &userResource{client: c}
		sr := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		state := tfsdk.State{Schema: sr.Schema}
		m := ldapUserModel()
		require.False(t, state.Set(ctx, &m).HasError())
		resp := resource.DeleteResponse{State: state}
		r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
		require.True(t, resp.Diagnostics.HasError())
		require.Equal(t, "User is managed by LDAP", resp.Diagnostics.Errors()[0].Summary())
		detail := resp.Diagnostics.Errors()[0].Detail()
		require.Contains(t, detail, "LDAP sync", "the route that works is through the directory")
		require.NotContains(t, detail, "disabled = true", "the update guard refuses that")
	})
}

// A claims-only change to an LDAP group does not touch the group itself,
// which Pocket ID would refuse; a name change is refused with a clear error.
func TestGroupUpdateLDAPManaged(t *testing.T) {
	ctx := context.Background()
	base := groupResourceModel{
		ID: types.StringValue(ldapFixtureGroupID), Name: types.StringValue("ldap-group"),
		FriendlyName: types.StringValue("LDAP group"), CustomClaims: types.MapNull(types.StringType),
	}
	run := func(t *testing.T, change func(*groupResourceModel)) (resource.UpdateResponse, map[string]int) {
		c, calls := usersGroupsLDAPServer(t, true)
		r := &groupResource{client: c}
		sr := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		prior := tfsdk.State{Schema: sr.Schema}
		require.False(t, prior.Set(ctx, &base).HasError())
		planned := base
		change(&planned)
		plan := tfsdk.Plan{Schema: sr.Schema}
		require.False(t, plan.Set(ctx, &planned).HasError())
		resp := resource.UpdateResponse{State: prior}
		r.Update(ctx, resource.UpdateRequest{Plan: plan, State: prior}, &resp)
		return resp, calls
	}
	t.Run("claims_only", func(t *testing.T) {
		resp, calls := run(t, func(m *groupResourceModel) {
			m.CustomClaims = types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("a")})
		})
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		require.Zero(t, calls["PUT /api/user-groups/"+ldapFixtureGroupID])
	})
	t.Run("rename_refused", func(t *testing.T) {
		resp, calls := run(t, func(m *groupResourceModel) { m.FriendlyName = types.StringValue("Renamed") })
		require.True(t, resp.Diagnostics.HasError())
		require.Equal(t, "User group is managed by LDAP", resp.Diagnostics.Errors()[0].Summary())
		require.Zero(t, calls["PUT /api/custom-claims/user-group/"+ldapFixtureGroupID])
	})
	t.Run("delete_refused_explained", func(t *testing.T) {
		c, _ := usersGroupsLDAPServer(t, true)
		r := &groupResource{client: c}
		sr := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		state := tfsdk.State{Schema: sr.Schema}
		require.False(t, state.Set(ctx, &base).HasError())
		resp := resource.DeleteResponse{State: state}
		r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
		require.True(t, resp.Diagnostics.HasError())
		require.Equal(t, "User group is managed by LDAP", resp.Diagnostics.Errors()[0].Summary())
	})
}

// An omitted email is not sent (Pocket ID rejects ""), and stays null.
func TestUserCreateWithoutEmail(t *testing.T) {
	ctx := context.Background()
	s := &usersGroupsDefaultsServer{}
	r := &userResource{client: s.start(t)}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	model := defaultsPlanModel(types.SetNull(types.StringType), types.MapNull(types.StringType))
	model.Email = types.StringNull()
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	_, sent := s.created["email"]
	require.False(t, sent, "no email key is sent")
	var state userResourceModel
	require.False(t, resp.State.Get(ctx, &state).HasError())
	require.True(t, state.Email.IsNull())
}
