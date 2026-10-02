package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestClient_GenerateClientSecret(t *testing.T) {
	expectedSecret := "new-client-secret-123"
	const expectedSecretID = "99999999-9999-4999-8999-999999999999"

	tests := []struct {
		currentVersion   string
		expectedEndpoint string
	}{
		{
			"1.0",
			"/secret",
		},
		{
			"2.2.0",
			"/secret",
		},
		{
			"2.13",
			"/secret",
		},
		{
			"2.13.9",
			"/secret",
		},
		{
			"2.14.0",
			"/secrets",
		},
		{
			"2.14.1",
			"/secrets",
		},
		{
			"2.9.0",
			"/secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.currentVersion, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedCreateUrl := "/api/oidc/clients/test-client-id" + tt.expectedEndpoint

				switch r.URL.Path {
				case "/api/version/current":
					assert.Equal(t, "GET", r.Method)

					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(map[string]string{"currentVersion": tt.currentVersion}); err != nil {
						t.Fatalf("Failed to encode response: %v", err)
					}
					return
				case expectedCreateUrl:
					assert.Equal(t, "POST", r.Method)
					w.Header().Set("Content-Type", "application/json")
					response := map[string]string{"secret": expectedSecret}
					if tt.expectedEndpoint == "/secrets" {
						// OidcClientSecretCreatedDto: the metadata and the value.
						response["id"] = expectedSecretID
						response["prefix"] = expectedSecret[:4]
						response["createdAt"] = "2026-08-01T00:00:00Z"
					}
					if err := json.NewEncoder(w).Encode(response); err != nil {
						t.Fatalf("Failed to encode response: %v", err)
					}
				default:
					t.Fatalf("Unexpected request path: %s", r.URL.Path)
				}
			}))
			defer server.Close()

			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			secret, err := c.GenerateClientSecret(context.Background(), "test-client-id", nil)
			require.NoError(t, err)
			assert.Equal(t, expectedSecret, secret.Value)
			if tt.expectedEndpoint == "/secrets" {
				assert.Equal(t, expectedSecretID, secret.ID)
				assert.Equal(t, "new-", secret.Prefix)
			} else {
				assert.Empty(t, secret.ID, "the singular endpoint names no secret")
			}
		})
	}

	t.Run("uses /secret endpoint if /version/current endpoint doesn't exist", func(t *testing.T) {
		// The /api/version/current endpoint was added in v2.3.0, so if not found, assume an older version
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/version/current":
				assert.Equal(t, "GET", r.Method)
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"error":"API endpoint not found"}`)
				return
			case "/api/oidc/clients/test-client-id/secret":
				assert.Equal(t, "POST", r.Method)
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]string{"secret": expectedSecret}); err != nil {
					t.Fatalf("Failed to encode response: %v", err)
				}
			default:
				t.Fatalf("Unexpected request path: %s", r.URL.Path)
			}
		}))
		defer server.Close()

		c, err := client.NewClient(server.URL, "test-token", false, 30)
		require.NoError(t, err)

		secret, err := c.GenerateClientSecret(context.Background(), "test-client-id", nil)
		require.NoError(t, err)
		assert.Equal(t, expectedSecret, secret.Value)
		assert.Empty(t, secret.ID)
	})
}

func TestClient_GenerateClientSecret_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version/current" {
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"secret": 123}`); err != nil { // secret should be string
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.GenerateClientSecret(context.Background(), "test-id", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error unmarshaling secret response")
}

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
			_, _ = fmt.Fprint(w, `{"id":"99999999-9999-4999-8999-999999999999","createdAt":"2026-08-01T00:00:00Z","secret":"synthetic-secret"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)
	secret, err := c.GenerateClientSecret(context.Background(), "fixture", nil)
	require.NoError(t, err)
	require.True(t, secret.Value == "synthetic-secret")
	require.Equal(t, "99999999-9999-4999-8999-999999999999", secret.ID)
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
			_, err := c.GenerateClientSecret(context.Background(), "fixture", nil)
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
			_, err := c.GenerateClientSecret(context.Background(), "fixture", nil)
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
	_, err := c.GenerateClientSecret(context.Background(), "fixture", nil)
	require.Error(t, err)
}

func TestClient_DeleteClientSecret(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		body     string
		wantCode int
	}{
		"revoked":         {http.StatusNoContent, "", 0},
		"secret missing":  {http.StatusNotFound, `{"error":"Client secret not found","code":"not_found"}`, 404},
		"rejected":        {http.StatusForbidden, `{"error":"synthetic"}`, 403},
		"server failure":  {http.StatusServiceUnavailable, "", 503},
		"missing route":   {http.StatusNotFound, `{"error":"API endpoint not found"}`, 404},
		"generic failure": {http.StatusInternalServerError, "", 500},
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Equal(t, "/api/oidc/clients/c1/secrets/99999999-9999-4999-8999-999999999999", r.URL.EscapedPath())
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			err = c.DeleteClientSecret(context.Background(), "c1", "99999999-9999-4999-8999-999999999999")
			assert.Equal(t, 1, requests, "a mutation is never retried")
			if tc.wantCode == 0 {
				assert.NoError(t, err)
				return
			}
			var status *client.HTTPError
			require.True(t, errors.As(err, &status))
			assert.Equal(t, tc.wantCode, status.StatusCode)
			assert.NotContains(t, err.Error(), "synthetic")
		})
	}
}

func TestClient_ListClientSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/oidc/clients/c1/secrets", r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		// OidcClientSecretDto as v2.14.0 to v2.17.0 serialize it.
		_, _ = fmt.Fprint(w, `[{"id":"s1","prefix":"abcd","createdAt":"2026-08-01T00:00:00Z","expiresAt":null,"isActive":true},`+
			`{"id":"s2","prefix":"","createdAt":"2026-07-01T12:30:00.123456789+02:00","expiresAt":"2026-09-01T00:00:00Z","isActive":false}]`)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	secrets, err := c.ListClientSecrets(context.Background(), "c1")
	require.NoError(t, err)
	expired := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.Len(t, secrets, 2)
	assert.Equal(t, client.ClientSecretMetadata{ID: "s1", Prefix: "abcd", CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), IsActive: true}, secrets[0])
	assert.Equal(t, "s2", secrets[1].ID)
	assert.Empty(t, secrets[1].Prefix, "a migrated secret has no prefix")
	assert.True(t, secrets[1].CreatedAt.Equal(time.Date(2026, 7, 1, 10, 30, 0, 123456789, time.UTC)))
	assert.Equal(t, &expired, secrets[1].ExpiresAt)
	assert.False(t, secrets[1].IsActive)

	t.Run("malformed", func(t *testing.T) {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"not":"a list"}`)
		}))
		defer bad.Close()
		c, err := client.NewClient(bad.URL, "test-token", false, 30)
		require.NoError(t, err)
		_, err = c.ListClientSecrets(context.Background(), "c1")
		assert.Error(t, err)
	})
}

func TestClient_GenerateClientSecret_Options(t *testing.T) {
	const secretID = "99999999-9999-4999-8999-999999999999"
	const value = "caller-chosen-value-0123"
	expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	type seen struct {
		posts int
		gets  int
		body  string
	}
	start := func(t *testing.T, version, response string) (*client.Client, *seen) {
		got := &seen{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == "GET" {
				got.gets++
				_, _ = fmt.Fprintf(w, `{"currentVersion":%q}`, version)
				return
			}
			got.posts++
			body, _ := io.ReadAll(r.Body)
			got.body = string(body)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, response)
		}))
		t.Cleanup(server.Close)
		c, err := client.NewClient(server.URL, "test-token", false, 30)
		require.NoError(t, err)
		return c, got
	}
	created := `{"id":"` + secretID + `","prefix":"call","createdAt":"2026-10-02T10:00:00Z","expiresAt":"2030-01-02T03:04:05Z","isActive":true,"secret":"` + value + `"}`

	t.Run("value and expiry are sent", func(t *testing.T) {
		c, got := start(t, "2.17.0", created)
		secret, err := c.GenerateClientSecret(context.Background(), "c1", &client.ClientSecretOptions{Value: value, ExpiresAt: &expires})
		require.NoError(t, err)
		assert.JSONEq(t, `{"secret":"`+value+`","expiresAt":"2030-01-02T03:04:05Z"}`, got.body)
		assert.Equal(t, value, secret.Value)
		assert.Equal(t, client.ClientSecretMetadata{
			ID: secretID, Prefix: "call", CreatedAt: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC), ExpiresAt: &expires, IsActive: true,
		}, secret.ClientSecretMetadata)
	})
	t.Run("no options send no body", func(t *testing.T) {
		for _, opts := range []*client.ClientSecretOptions{nil, {}} {
			c, got := start(t, "2.17.0", created)
			_, err := c.GenerateClientSecret(context.Background(), "c1", opts)
			require.NoError(t, err)
			assert.Empty(t, got.body)
		}
	})
	t.Run("options refused before 2.14", func(t *testing.T) {
		c, got := start(t, "2.13.0", `{"secret":"x"}`)
		_, err := c.GenerateClientSecret(context.Background(), "c1", &client.ClientSecretOptions{ExpiresAt: &expires})
		require.ErrorContains(t, err, "requires Pocket ID 2.14.0")
		assert.Zero(t, got.posts)
	})
	t.Run("value checked before anything is sent", func(t *testing.T) {
		for _, bad := range []string{"short", "sixteen-chars-é!", "sixteen\tchars-xx\n"} {
			c, got := start(t, "2.17.0", created)
			_, err := c.GenerateClientSecret(context.Background(), "c1", &client.ClientSecretOptions{Value: bad})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), bad)
			assert.Zero(t, got.posts+got.gets)
		}
	})
	t.Run("unusable ID is an uncertain result", func(t *testing.T) {
		for _, response := range []string{`{"secret":"` + value + `"}`, `{"id":"../x","secret":"` + value + `"}`} {
			c, got := start(t, "2.17.0", response)
			secret, err := c.GenerateClientSecret(context.Background(), "c1", nil)
			require.ErrorContains(t, err, "result uncertain")
			assert.NotContains(t, err.Error(), value)
			assert.Nil(t, secret)
			assert.Equal(t, 1, got.posts)
		}
	})
	t.Run("value never printed or encoded", func(t *testing.T) {
		c, _ := start(t, "2.17.0", created)
		secret, err := c.GenerateClientSecret(context.Background(), "c1", nil)
		require.NoError(t, err)
		encoded, err := json.Marshal(secret)
		require.NoError(t, err)
		for _, text := range []string{fmt.Sprint(secret), fmt.Sprintf("%v %+v %#v %s", *secret, *secret, *secret, *secret), string(encoded)} {
			assert.NotContains(t, text, value)
		}
		assert.Contains(t, fmt.Sprint(secret), secretID)
	})
}
