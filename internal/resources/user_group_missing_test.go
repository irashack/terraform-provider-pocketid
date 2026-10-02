package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	missingFixtureUserID  = "11111111-1111-4111-8111-111111111111"
	missingFixtureGroupID = "22222222-2222-4222-8222-222222222222"
	// userNotFoundBody and groupNotFoundBody are Pocket ID's structured
	// not-found errors (apperror.UserNotFound and apperror.NotFound("User
	// group"), identical in v2.14.0 to v2.17.0).
	userNotFoundBody  = `{"error":"User not found","code":"user_not_found","request_id":"r"}`
	groupNotFoundBody = `{"error":"User group not found","code":"not_found","details":{"resource":"User group"},"request_id":"r"}`
)

// Responses that are 404s but do not prove the object is gone.
var unconfirmedNotFoundBodies = map[string]string{
	"bare":           ``,
	"proxy":          `<html>Not Found</html>`,
	"missing_route":  `{"error":"API endpoint not found"}`,
	"other_resource": `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`,
}

func missingObjectServer(t *testing.T, body string) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c
}

func userStateFixture(t *testing.T) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	sr := resource.SchemaResponse{}
	(&userResource{}).Schema(ctx, resource.SchemaRequest{}, &sr)
	state := tfsdk.State{Schema: sr.Schema}
	model := userResourceModel{
		ID: types.StringValue(missingFixtureUserID), Username: types.StringValue("fixture"),
		Email: types.StringValue("fixture@example.invalid"), FirstName: types.StringNull(), LastName: types.StringNull(),
		DisplayName: types.StringValue(""), EmailVerified: types.BoolValue(false), IsAdmin: types.BoolValue(false),
		Locale: types.StringNull(), Disabled: types.BoolValue(false),
		Groups: types.SetNull(types.StringType), CustomClaims: types.MapNull(types.StringType),
	}
	require.False(t, state.Set(ctx, &model).HasError())
	return state
}

func groupStateFixture(t *testing.T) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	sr := resource.SchemaResponse{}
	(&groupResource{}).Schema(ctx, resource.SchemaRequest{}, &sr)
	state := tfsdk.State{Schema: sr.Schema}
	model := groupResourceModel{
		ID: types.StringValue(missingFixtureGroupID), Name: types.StringValue("fixture"),
		FriendlyName: types.StringValue("Fixture"), CustomClaims: types.MapNull(types.StringType),
	}
	require.False(t, state.Set(ctx, &model).HasError())
	return state
}

// Read drops a user or group from state only on Pocket ID's own not-found
// error for that kind of object; every other 404 is an error that keeps it.
func TestUserGroupReadOnMissingObject(t *testing.T) {
	ctx := context.Background()
	type readCase struct {
		body    string
		removed bool
	}
	cases := map[string]readCase{"confirmed": {userNotFoundBody, true}}
	for name, body := range unconfirmedNotFoundBodies {
		cases[name] = readCase{body, false}
	}
	cases["group_error_for_user"] = readCase{groupNotFoundBody, false}
	for name, tc := range cases {
		t.Run("user_"+name, func(t *testing.T) {
			r := &userResource{client: missingObjectServer(t, tc.body)}
			state := userStateFixture(t)
			resp := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			require.Equal(t, !tc.removed, resp.Diagnostics.HasError())
			require.Equal(t, tc.removed, resp.State.Raw.IsNull())
		})
	}

	groupCases := map[string]readCase{"confirmed": {groupNotFoundBody, true}, "user_error_for_group": {userNotFoundBody, false}}
	for name, body := range unconfirmedNotFoundBodies {
		groupCases[name] = readCase{body, false}
	}
	for name, tc := range groupCases {
		t.Run("group_"+name, func(t *testing.T) {
			r := &groupResource{client: missingObjectServer(t, tc.body)}
			state := groupStateFixture(t)
			resp := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			require.Equal(t, !tc.removed, resp.Diagnostics.HasError())
			require.Equal(t, tc.removed, resp.State.Raw.IsNull())
		})
	}
}

// Delete of an object Pocket ID confirms is already gone succeeds; any other
// 404 is an error.
func TestUserGroupDeleteOnMissingObject(t *testing.T) {
	ctx := context.Background()
	for name, body := range unconfirmedNotFoundBodies {
		t.Run("user_"+name, func(t *testing.T) {
			r := &userResource{client: missingObjectServer(t, body)}
			resp := resource.DeleteResponse{}
			r.Delete(ctx, resource.DeleteRequest{State: userStateFixture(t)}, &resp)
			require.True(t, resp.Diagnostics.HasError())
		})
		t.Run("group_"+name, func(t *testing.T) {
			r := &groupResource{client: missingObjectServer(t, body)}
			resp := resource.DeleteResponse{}
			r.Delete(ctx, resource.DeleteRequest{State: groupStateFixture(t)}, &resp)
			require.True(t, resp.Diagnostics.HasError())
		})
	}
	t.Run("user_confirmed", func(t *testing.T) {
		r := &userResource{client: missingObjectServer(t, userNotFoundBody)}
		resp := resource.DeleteResponse{}
		r.Delete(ctx, resource.DeleteRequest{State: userStateFixture(t)}, &resp)
		require.False(t, resp.Diagnostics.HasError())
	})
	t.Run("group_confirmed", func(t *testing.T) {
		r := &groupResource{client: missingObjectServer(t, groupNotFoundBody)}
		resp := resource.DeleteResponse{}
		r.Delete(ctx, resource.DeleteRequest{State: groupStateFixture(t)}, &resp)
		require.False(t, resp.Diagnostics.HasError())
	})
	t.Run("group_error_for_user", func(t *testing.T) {
		r := &userResource{client: missingObjectServer(t, groupNotFoundBody)}
		resp := resource.DeleteResponse{}
		r.Delete(ctx, resource.DeleteRequest{State: userStateFixture(t)}, &resp)
		require.True(t, resp.Diagnostics.HasError())
	})
}
