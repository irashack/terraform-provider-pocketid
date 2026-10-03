package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	failedCreateUserID  = "33333333-3333-4333-8333-333333333333"
	failedCreateGroupID = "44444444-4444-4444-8444-444444444444"
)

// usersGroupsCancelAfterCreate is an apply cancelled right after the create
// has answered: once cancel runs, Err reports cancellation, so the provider's
// next request is refused before it is sent, while Done never closes, so the
// create's own response (already on its way) is read in full. This puts the
// cancellation exactly between the create and the next request.
type usersGroupsCancelAfterCreate struct {
	context.Context
	cancelled atomic.Bool
}

func (c *usersGroupsCancelAfterCreate) Err() error {
	if c.cancelled.Load() {
		return context.Canceled
	}
	return nil
}

func (c *usersGroupsCancelAfterCreate) cancel() { c.cancelled.Store(true) }

// usersGroupsFailedCreateCase is one way a step after the create can fail,
// and what the cleanup DELETE and the read that checks it answer.
type usersGroupsFailedCreateCase struct {
	name string
	// stepStatus answers the claims PUT; 0 means the context is cancelled
	// as the create's response is written, so the PUT is never sent.
	stepStatus               int
	deleteStatus, readStatus int
	deleteBody, readBody     string
	retained                 bool
	title                    string
}

func usersGroupsFailedCreateCases(notFoundBody string) []usersGroupsFailedCreateCase {
	return []usersGroupsFailedCreateCase{
		{name: "rejected_rolled_back", stepStatus: 400, deleteStatus: 204, title: "creation rolled back"},
		{name: "server_error_rolled_back", stepStatus: 503, deleteStatus: 204, title: "creation rolled back"},
		{name: "delete_reports_gone", stepStatus: 400, deleteStatus: 404, deleteBody: notFoundBody, title: "rollback verified"},
		{name: "delete_failed_read_confirms_gone", stepStatus: 400, deleteStatus: 503, readStatus: 404, readBody: notFoundBody, title: "rollback verified"},
		{name: "delete_failed_still_exists", stepStatus: 400, deleteStatus: 403, readStatus: 200, readBody: "{}", retained: true, title: "cleanup failed"},
		{name: "delete_failed_bare_404", stepStatus: 400, deleteStatus: 503, readStatus: 404, retained: true, title: "cleanup failed"},
		{name: "delete_failed_missing_route", stepStatus: 400, deleteStatus: 503, readStatus: 404, readBody: `{"error":"API endpoint not found"}`, retained: true, title: "cleanup failed"},
		{name: "delete_bare_404", stepStatus: 400, deleteStatus: 404, readStatus: 403, retained: true, title: "cleanup failed"},
		// The apply is cancelled once the create has answered: the next
		// request is never sent, and the cleanup still runs.
		{name: "cancelled_rolled_back", deleteStatus: 204, title: "creation rolled back"},
		{name: "cancelled_cleanup_failed", deleteStatus: 503, readStatus: 403, retained: true, title: "cleanup failed"},
		{name: "cancelled_read_confirms_gone", deleteStatus: 503, readStatus: 404, readBody: notFoundBody, title: "rollback verified"},
	}
}

// usersGroupsFailedCreateServer answers the create at createPath with
// createBody, the claims PUT at stepPath, and the DELETE and GET of objectPath.
func usersGroupsFailedCreateServer(t *testing.T, tc usersGroupsFailedCreateCase, cancel func(), createPath, createBody, stepPath, objectPath string) (*client.Client, *map[string]int) {
	t.Helper()
	var mu sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST " + createPath:
			if tc.stepStatus == 0 {
				cancel()
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, createBody)
		case "PUT " + stepPath:
			w.WriteHeader(tc.stepStatus)
			_, _ = fmt.Fprint(w, `{"error":"synthetic step failure"}`)
		case "PUT " + objectPath + "/user-groups":
			// A user created without groups gets an empty group list
			// written and verified before its claims are set.
			_, _ = fmt.Fprint(w, `{"id":"`+strings.TrimPrefix(objectPath, "/api/users/")+`","userGroups":[]}`)
		case "DELETE " + objectPath:
			w.WriteHeader(tc.deleteStatus)
			_, _ = fmt.Fprint(w, tc.deleteBody)
		case "GET " + objectPath:
			w.WriteHeader(tc.readStatus)
			_, _ = fmt.Fprint(w, tc.readBody)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c, &calls
}

func usersGroupsCheckFailedCreate(t *testing.T, tc usersGroupsFailedCreateCase, diags []string, details string, stateNull bool, calls map[string]int, objectPath string) {
	t.Helper()
	require.NotEmpty(t, diags)
	require.Contains(t, diags[len(diags)-1], tc.title)
	require.NotContains(t, details, "synthetic-token")
	require.Equal(t, 1, calls["DELETE "+objectPath], "the cleanup DELETE is sent exactly once, cancelled or not")
	require.Equal(t, !tc.retained, stateNull, "state is kept exactly when the deletion is unconfirmed")
	if tc.retained {
		require.Contains(t, details, "kept in state")
		require.NotContains(t, details, "was deleted")
	}
}

func TestUserFailedCreateCleanup(t *testing.T) {
	userPath := "/api/users/" + failedCreateUserID
	for _, tc := range usersGroupsFailedCreateCases(userNotFoundBody) {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &usersGroupsCancelAfterCreate{Context: context.Background()}
			c, calls := usersGroupsFailedCreateServer(t, tc, ctx.cancel, "/api/users",
				`{"id":"`+failedCreateUserID+`","username":"fixture","email":"fixture@example.invalid","firstName":"","lastName":"","displayName":""}`,
				"/api/custom-claims/user/"+failedCreateUserID, userPath)
			r := &userResource{client: c}
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			model := userResourceModel{
				ID: types.StringUnknown(), Username: types.StringValue("fixture"),
				Email: types.StringValue("fixture@example.invalid"), FirstName: types.StringNull(), LastName: types.StringNull(),
				DisplayName: types.StringUnknown(), EmailVerified: types.BoolValue(false), IsAdmin: types.BoolValue(false),
				Locale: types.StringNull(), Disabled: types.BoolValue(false), Groups: types.SetNull(types.StringType),
				CustomClaims: types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("a")}),
			}
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

			require.True(t, resp.Diagnostics.HasError())
			var summaries []string
			var details strings.Builder
			for _, d := range resp.Diagnostics {
				summaries = append(summaries, d.Summary())
				details.WriteString(d.Detail())
			}
			usersGroupsCheckFailedCreate(t, tc, summaries, details.String(), resp.State.Raw.IsNull(), *calls, userPath)
			if tc.retained {
				var state userResourceModel
				require.False(t, resp.State.Get(ctx, &state).HasError())
				require.Equal(t, failedCreateUserID, state.ID.ValueString())
			}
		})
	}
}

func TestGroupFailedCreateCleanup(t *testing.T) {
	groupPath := "/api/user-groups/" + failedCreateGroupID
	for _, tc := range usersGroupsFailedCreateCases(groupNotFoundBody) {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &usersGroupsCancelAfterCreate{Context: context.Background()}
			c, calls := usersGroupsFailedCreateServer(t, tc, ctx.cancel, "/api/user-groups",
				`{"id":"`+failedCreateGroupID+`","name":"fixture","friendlyName":"Fixture"}`,
				"/api/custom-claims/user-group/"+failedCreateGroupID, groupPath)
			r := &groupResource{client: c}
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			model := groupResourceModel{
				ID: types.StringUnknown(), Name: types.StringValue("fixture"), FriendlyName: types.StringValue("Fixture"),
				CustomClaims: types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("a")}),
			}
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

			require.True(t, resp.Diagnostics.HasError())
			var summaries []string
			var details strings.Builder
			for _, d := range resp.Diagnostics {
				summaries = append(summaries, d.Summary())
				details.WriteString(d.Detail())
			}
			usersGroupsCheckFailedCreate(t, tc, summaries, details.String(), resp.State.Raw.IsNull(), *calls, groupPath)
			if tc.retained {
				var state groupResourceModel
				require.False(t, resp.State.Get(ctx, &state).HasError())
				require.Equal(t, failedCreateGroupID, state.ID.ValueString())
			}
		})
	}
}
