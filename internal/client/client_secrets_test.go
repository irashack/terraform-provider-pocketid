package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// Pocket ID 2.17.0 returns the secret it generated for a new confidential
// client as createdSecret. Only its ID is decoded.
func TestClient_CreateClient_CreatedSecret(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want *client.CreatedClientSecret
	}{
		"2.17 created a secret": {`{"id":"c1","name":"n","createdSecret":{"id":"s1","prefix":"abcd","secret":"synthetic-created-secret","isActive":true}}`, &client.CreatedClientSecret{ID: "s1"}},
		"2.17 created none":     {`{"id":"c1","name":"n"}`, nil},
		"explicit null":         {`{"id":"c1","name":"n","createdSecret":null}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			result, err := c.CreateClient(&client.OIDCClientCreateRequest{Name: "n"})
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.CreatedSecret)
			// Encoding walks every field, including through the pointer, so
			// this fails if any decoded field holds the secret's value.
			encoded, err := json.Marshal(result)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "synthetic-created-secret")
		})
	}
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
				// Path segments are escaped, never interpreted as path structure.
				assert.Equal(t, "/api/oidc/clients/c%2F1/secrets/s%2F1", r.URL.EscapedPath())
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			err = c.DeleteClientSecret("c/1", "s/1")
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
		assert.Equal(t, "/api/oidc/clients/c%2F1/secrets", r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"id":"s1","prefix":"abcd","isActive":true},{"id":"s2","prefix":"efgh","isActive":false}]`)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	secrets, err := c.ListClientSecrets("c/1")
	require.NoError(t, err)
	assert.Equal(t, []client.ClientSecretMetadata{{ID: "s1", IsActive: true}, {ID: "s2"}}, secrets)

	t.Run("malformed", func(t *testing.T) {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"not":"a list"}`)
		}))
		defer bad.Close()
		c, err := client.NewClient(bad.URL, "test-token", false, 30)
		require.NoError(t, err)
		_, err = c.ListClientSecrets("c1")
		assert.Error(t, err)
	})
}

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

			_, err = c.GetClient("c1")
			require.Error(t, err)
			assert.Equal(t, tc.want, client.IsOIDCClientNotFound(err))
			assert.False(t, client.IsUserNotFound(err) && tc.want, "the two signals never coincide")
		})
	}
}
