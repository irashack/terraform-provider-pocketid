package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const tokenFixtureUserID = "abababab-abab-4bab-8bab-abababababab"

// A token resource is recorded only with a token. A response without one,
// or a failure that may have created one, is reported as uncertain, never
// recorded, and never repeated.
func TestOneTimeAccessTokenCreateOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, body, title string
		status            int
		recorded          bool
	}{
		{"token", `{"token":"synthetic-one-time-token"}`, "", http.StatusCreated, true},
		{"empty_object", `{}`, "One-time access token creation result uncertain", http.StatusCreated, false},
		{"empty_token", `{"token":""}`, "One-time access token creation result uncertain", http.StatusCreated, false},
		{"null", `null`, "One-time access token creation result uncertain", http.StatusCreated, false},
		{"not_json", `<html>ok</html>`, "One-time access token creation result uncertain", http.StatusOK, false},
		{"server_error", `{"error":"synthetic"}`, "One-time access token creation result uncertain", http.StatusBadGateway, false},
		{"user_missing", `{"error":"User not found","code":"user_not_found"}`, "Error creating one-time access token", http.StatusNotFound, false},
		{"rejected", `{"error":"synthetic"}`, "Error creating one-time access token", http.StatusBadRequest, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "POST /api/users/"+tokenFixtureUserID+"/one-time-access-token", r.Method+" "+r.URL.Path)
				posts++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
			require.NoError(t, err)
			r := &OneTimeAccessTokenResource{client: c}
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &OneTimeAccessTokenResourceModel{
				ID: types.StringUnknown(), UserID: types.StringValue(tokenFixtureUserID), TTL: types.StringValue("1h"),
				Token: types.StringUnknown(), ExpiresAt: types.StringUnknown(), CreatedAt: types.StringUnknown(),
			}).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

			require.Equal(t, 1, posts, "the POST is sent once")
			require.Equal(t, !tc.recorded, resp.Diagnostics.HasError())
			if tc.recorded {
				var state OneTimeAccessTokenResourceModel
				require.False(t, resp.State.Get(ctx, &state).HasError())
				require.Equal(t, "synthetic-one-time-token", state.Token.ValueString())
				return
			}
			require.True(t, resp.State.Raw.IsNull(), "nothing is recorded without a token")
			require.Equal(t, tc.title, resp.Diagnostics.Errors()[0].Summary())
			detail := resp.Diagnostics.Errors()[0].Detail()
			require.False(t, strings.Contains(detail, "synthetic-token"))
			if tc.title == "One-time access token creation result uncertain" {
				require.Contains(t, detail, "may have been created")
				require.Contains(t, detail, "not repeated")
			}
		})
	}
}
