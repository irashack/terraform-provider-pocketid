package client_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
	"github.com/stretchr/testify/require"
)

// Regression for upstream #96: the real 2.14 response includes metadata and
// the create-only secret at the top level.
func TestIssue96PocketID214(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/version/current":
			fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
		case "POST /api/oidc/clients/fixture/secrets":
			posts++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"secret-id","createdAt":"2026-08-01T00:00:00Z","secret":"synthetic-secret"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)
	secret, err := c.GenerateClientSecret("fixture")
	require.NoError(t, err)
	require.True(t, secret == "synthetic-secret")
	require.Equal(t, 1, posts)
}
