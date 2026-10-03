package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// notFoundAnswers are 404 bodies; only Pocket ID's own error for an OIDC
// client confirms the client does not exist.
var notFoundAnswers = map[string]struct {
	body      string
	confirmed bool
}{
	"client not found":      {clientNotFoundBody, true},
	"bare 404":              {``, false},
	"proxy page":            {`<html>Not Found</html>`, false},
	"missing route":         {`{"error":"API endpoint not found"}`, false},
	"another kind of thing": {`{"error":"User group not found","code":"not_found","details":{"resource":"User group"}}`, false},
}

func notFoundServer(t *testing.T, body string) (*client.Client, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/oidc/clients/c1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c, &calls
}

// Read removes a client from state only when Pocket ID confirms it is gone.
func TestClientReadConfirmedMissing(t *testing.T) {
	for name, tc := range notFoundAnswers {
		t.Run(name, func(t *testing.T) {
			c, _ := notFoundServer(t, tc.body)
			resp, _ := runRead(t, &clientResource{client: c}, managedModel())
			if tc.confirmed {
				require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
				assert.True(t, resp.State.Raw.IsNull(), "removed from state")
				return
			}
			require.True(t, resp.Diagnostics.HasError())
			assert.False(t, resp.State.Raw.IsNull(), "kept in state")
		})
	}
}

// Delete succeeds on a client that is already gone only when Pocket ID
// confirms it; the DELETE is sent once.
func TestClientDeleteConfirmedMissing(t *testing.T) {
	for name, tc := range notFoundAnswers {
		t.Run(name, func(t *testing.T) {
			c, calls := notFoundServer(t, tc.body)
			ctx := context.Background()
			s := clientSchema(t).Schema
			state := tfsdk.State{Schema: s}
			prior := managedModel()
			require.False(t, state.Set(ctx, &prior).HasError())
			resp := resource.DeleteResponse{State: state}
			(&clientResource{client: c}).Delete(ctx, resource.DeleteRequest{State: state}, &resp)
			assert.Equal(t, 1, *calls)
			assert.Equal(t, !tc.confirmed, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		})
	}
}
