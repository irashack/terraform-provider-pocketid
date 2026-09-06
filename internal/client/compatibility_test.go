package client_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// Regression for upstream #96: the real 2.14 response includes metadata and
// the create-only secret at the top level.
func TestIssue96PocketID214(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/version/current":
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
		case "POST /api/oidc/clients/fixture/secrets":
			posts.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"secret-id","createdAt":"2026-08-01T00:00:00Z","secret":"synthetic-secret"}`)
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
	require.Equal(t, int32(1), posts.Load())
}

func TestSecretVersionFailuresDoNotPost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"absent", 200, `{}`}, {"empty", 200, `{"currentVersion":""}`},
		{"malformed", 200, `{"currentVersion":"invalid"}`}, {"json", 200, `{`},
		{"unauthorized", 401, `{"error":"synthetic-token"}`},
		{"forbidden", 403, `{"error":"synthetic-token"}`},
		{"server", 500, `{"error":"synthetic-token"}`},
		{"unverified404", 404, `<html>not found</html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					posts.Add(1)
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			c, _ := client.NewClient(s.URL, "synthetic-token", false, 1)
			_, err := c.GenerateClientSecret("fixture")
			require.Error(t, err)
			require.NotContains(t, err.Error(), "synthetic-token")
			require.Zero(t, posts.Load())
		})
	}
}

func TestSecretMutationNeverRetries(t *testing.T) {
	for _, response := range []string{"server", "malformed", "empty", "disconnect", "timeout"} {
		t.Run(response, func(t *testing.T) {
			var posts atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					_, _ = fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
					return
				}
				posts.Add(1)
				switch response {
				case "server":
					w.WriteHeader(503)
					_, _ = fmt.Fprint(w, `{"error":"synthetic-secret"}`)
				case "malformed":
					_, _ = fmt.Fprint(w, `{"secret":`)
				case "empty":
					_, _ = fmt.Fprint(w, `{}`)
				case "timeout":
					time.Sleep(1100 * time.Millisecond)
				case "disconnect":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
				}
			}))
			defer s.Close()
			c, _ := client.NewClient(s.URL, "synthetic-token", false, 1)
			_, err := c.GenerateClientSecret("fixture")
			require.Error(t, err)
			require.NotContains(t, err.Error(), "synthetic-secret")
			require.Equal(t, int32(1), posts.Load())
		})
	}
}

func TestVersionTransportFailureDoesNotPost(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	s.Close()
	c, _ := client.NewClient(s.URL, "synthetic-token", false, 1)
	_, err := c.GenerateClientSecret("fixture")
	require.Error(t, err)
}
