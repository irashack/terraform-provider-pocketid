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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	defaultsUserID       = "99999999-9999-4999-8999-999999999999"
	defaultsDefaultGroup = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
	defaultsOtherGroup   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2"
)

// usersGroupsDefaultsServer creates users the way Pocket ID 2.14.0 to 2.17.0
// does through its admin API: without userGroupIds the new user gets the
// signup default group, and it always gets the default claim, which the
// create response does not show (createUserInternal, applyDefaultGroups,
// applyDefaultCustomClaims). Names are stored as "" when not sent.
type usersGroupsDefaultsServer struct {
	mu            sync.Mutex
	created       map[string]any
	groups        []string
	claims        []client.CustomClaim
	groupPuts     int
	createdGroups []string
	// answerGroups, when not nil, replaces the groups the create answer
	// lists (the server holds what it holds regardless).
	answerGroups []map[string]string
}

func (s *usersGroupsDefaultsServer) user() map[string]any {
	groups := []map[string]string{}
	for _, id := range s.groups {
		groups = append(groups, map[string]string{"id": id})
	}
	return map[string]any{
		"id": defaultsUserID, "username": s.created["username"], "email": s.created["email"],
		"firstName": "", "lastName": "", "displayName": "", "userGroups": groups, "customClaims": s.claims,
	}
}

func (s *usersGroupsDefaultsServer) start(t *testing.T) *client.Client {
	t.Helper()
	existing := []string{defaultsDefaultGroup, defaultsOtherGroup}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/users":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&s.created))
			s.groups = nil
			if ids, ok := s.created["userGroupIds"].([]any); ok && len(ids) > 0 {
				for _, id := range ids {
					s.createdGroups = append(s.createdGroups, id.(string))
					if slices.Contains(existing, id.(string)) {
						s.groups = append(s.groups, id.(string))
					}
				}
			} else {
				s.groups = []string{defaultsDefaultGroup}
			}
			s.claims = []client.CustomClaim{{Key: "dept", Value: "default"}}
			created := s.user()
			created["customClaims"] = []any{} // as Pocket ID answers
			if s.answerGroups != nil {
				created["userGroups"] = s.answerGroups
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(created)
		case "PUT /api/users/" + defaultsUserID + "/user-groups":
			s.groupPuts++
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			s.groups = nil
			for _, id := range req.UserGroupIDs {
				if slices.Contains(existing, id) {
					s.groups = append(s.groups, id)
				}
			}
			_ = json.NewEncoder(w).Encode(s.user())
		case "PUT /api/custom-claims/user/" + defaultsUserID:
			require.NoError(t, json.NewDecoder(r.Body).Decode(&s.claims))
			_ = json.NewEncoder(w).Encode(s.claims)
		case "GET /api/users/" + defaultsUserID:
			_ = json.NewEncoder(w).Encode(s.user())
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c
}

func defaultsPlanModel(groups types.Set, claims types.Map) userResourceModel {
	return userResourceModel{
		ID: types.StringUnknown(), Username: types.StringValue("fixture"), Email: types.StringValue("fixture@example.invalid"),
		FirstName: types.StringNull(), LastName: types.StringNull(), DisplayName: types.StringUnknown(),
		EmailVerified: types.BoolValue(false), IsAdmin: types.BoolValue(false), Locale: types.StringNull(),
		Disabled: types.BoolValue(false), Groups: groups, CustomClaims: claims,
	}
}

// After Create the user holds exactly the planned groups and claims, the
// signup defaults removed, and null and empty stay as configured.
func TestUserCreateReplacesSignupDefaults(t *testing.T) {
	ctx := context.Background()
	emptySet := types.SetValueMust(types.StringType, []attr.Value{})
	emptyMap := types.MapValueMust(types.StringType, map[string]attr.Value{})
	other := types.SetValueMust(types.StringType, []attr.Value{types.StringValue(defaultsOtherGroup)})
	team := types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("a")})
	for _, tc := range []struct {
		name          string
		groups        types.Set
		claims        types.Map
		wantGroupPuts int
		wantGroups    []string
		wantClaims    []client.CustomClaim
	}{
		{"null", types.SetNull(types.StringType), types.MapNull(types.StringType), 1, nil, []client.CustomClaim{}},
		{"explicit_empty", emptySet, emptyMap, 1, nil, []client.CustomClaim{}},
		// Groups sent with the create keep the default group from being added.
		{"planned", other, team, 0, []string{defaultsOtherGroup}, []client.CustomClaim{{Key: "team", Value: "a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &usersGroupsDefaultsServer{}
			r := &userResource{client: s.start(t)}
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			model := defaultsPlanModel(tc.groups, tc.claims)
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

			require.Equal(t, tc.wantGroupPuts, s.groupPuts)
			require.Equal(t, tc.wantGroups, s.groups, "the server holds exactly the planned groups")
			require.Equal(t, tc.wantClaims, s.claims, "the server holds exactly the planned claims")

			var state userResourceModel
			require.False(t, resp.State.Get(ctx, &state).HasError())
			require.True(t, state.Groups.Equal(tc.groups), "groups keep their planned representation: %s", state.Groups)
			require.True(t, state.CustomClaims.Equal(tc.claims), "claims keep their planned representation: %s", state.CustomClaims)
			require.True(t, state.FirstName.IsNull(), "an omitted first name stays null")
			require.True(t, state.LastName.IsNull(), "an omitted last name stays null")
		})
	}
}

// Read keeps null and empty as they were when the server has no groups,
// claims or names.
func TestUserReadKeepsNullAndEmpty(t *testing.T) {
	ctx := context.Background()
	s := &usersGroupsDefaultsServer{created: map[string]any{"username": "fixture", "email": "fixture@example.invalid"}}
	r := &userResource{client: s.start(t)}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	for _, explicit := range []bool{false, true} {
		model := defaultsPlanModel(types.SetNull(types.StringType), types.MapNull(types.StringType))
		model.ID, model.DisplayName = types.StringValue(defaultsUserID), types.StringValue("")
		if explicit {
			model.Groups = types.SetValueMust(types.StringType, []attr.Value{})
			model.CustomClaims = types.MapValueMust(types.StringType, map[string]attr.Value{})
			model.FirstName, model.LastName = types.StringValue(""), types.StringValue("")
		}
		state := tfsdk.State{Schema: sr.Schema}
		require.False(t, state.Set(ctx, &model).HasError())
		resp := resource.ReadResponse{State: state}
		r.Read(ctx, resource.ReadRequest{State: state}, &resp)
		require.False(t, resp.Diagnostics.HasError())
		var got userResourceModel
		require.False(t, resp.State.Get(ctx, &got).HasError())
		require.Equal(t, model, got, "explicit=%v", explicit)
	}
}

// The create answer's groups are not taken as proof of what the new user is
// in. With no groups planned, the empty list is always written and verified,
// so default groups the answer leaves out, or lists under an unusable ID, are
// removed. With groups planned and an answer whose groups cannot be used, the
// planned groups are written and verified instead of the create being
// rolled back.
func TestUserCreateDoesNotTrustTheAnswersGroups(t *testing.T) {
	ctx := context.Background()
	other := types.SetValueMust(types.StringType, []attr.Value{types.StringValue(defaultsOtherGroup)})
	for _, tc := range []struct {
		name       string
		answer     []map[string]string
		groups     types.Set
		wantGroups []string
	}{
		{"no groups planned, the answer lists none", []map[string]string{}, types.SetNull(types.StringType), nil},
		{"no groups planned, the answer lists an unusable ID", []map[string]string{{"id": "not-a-group-id"}}, types.SetNull(types.StringType), nil},
		{"groups planned, the answer lists an unusable ID", []map[string]string{{"id": "not-a-group-id"}}, other, []string{defaultsOtherGroup}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &usersGroupsDefaultsServer{answerGroups: tc.answer}
			r := &userResource{client: s.start(t)}
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			model := defaultsPlanModel(tc.groups, types.MapNull(types.StringType))
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, 1, s.groupPuts, "the groups are written and verified")
			assert.Equal(t, tc.wantGroups, s.groups, "the server holds exactly the planned groups")
		})
	}
}
