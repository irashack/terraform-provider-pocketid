package client_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Only Pocket ID's structured not-found error for an OIDC client confirms the
// client is gone, identical from v2.14.0 to v2.17.0.
func TestIsOIDCClientNotFound(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   bool
	}{
		"structured client not found": {404, `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"},"request_id":"r"}`, true},
		"bare 404":                    {404, ``, false},
		"proxy page":                  {404, `<html><body>Not Found</body></html>`, false},
		"missing route":               {404, `{"error":"API endpoint not found"}`, false},
		"another resource":            {404, `{"error":"Client secret not found","code":"not_found","details":{"resource":"Client secret"}}`, false},
		"no resource detail":          {404, `{"error":"not found","code":"not_found"}`, false},
		"user not found":              {404, `{"error":"User not found","code":"user_not_found"}`, false},
		"wrong status":                {400, `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			_, err = c.GetClient(context.Background(), "c1")
			require.Error(t, err)
			assert.Equal(t, tc.want, client.IsOIDCClientNotFound(err))
			assert.False(t, client.IsUserNotFound(err) && tc.want, "the two signals never coincide")
		})
	}
}
