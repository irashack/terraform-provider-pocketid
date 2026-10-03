package client_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestClient_CreateScimServiceProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/scim/service-provider", r.URL.Path)

		var req client.ScimServiceProviderCreateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "https://scim.example.com/v2", req.Endpoint)
		assert.Equal(t, "secret-token", req.Token)
		assert.Equal(t, "client-123", req.OidcClientID)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&client.ScimServiceProvider{
			ID:       "33333333-3333-4333-8333-333333333333",
			Endpoint: req.Endpoint,
			Token:    req.Token,
			OidcClient: &client.OIDCClientMetadata{
				ID:   "client-123",
				Name: "Test Client",
			},
			CreatedAt: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.CreateScimServiceProvider(context.Background(), &client.ScimServiceProviderCreateRequest{
		Endpoint:     "https://scim.example.com/v2",
		Token:        "secret-token",
		OidcClientID: "client-123",
	})
	assert.NoError(t, err)
	assert.Equal(t, "33333333-3333-4333-8333-333333333333", result.ID)
	assert.Equal(t, "secret-token", result.Token)
	require.NotNil(t, result.OidcClient)
	assert.Equal(t, "client-123", result.OidcClient.ID)
}

func TestClient_GetClientScimServiceProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/oidc/clients/client-123/scim-service-provider", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&client.ScimServiceProvider{
			ID:       "33333333-3333-4333-8333-333333333333",
			Endpoint: "https://scim.example.com/v2",
			Token:    "decrypted-token",
			OidcClient: &client.OIDCClientMetadata{
				ID: "client-123",
			},
			CreatedAt: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetClientScimServiceProvider(context.Background(), "client-123")
	assert.NoError(t, err)
	assert.Equal(t, "33333333-3333-4333-8333-333333333333", result.ID)
	assert.Equal(t, "decrypted-token", result.Token)
	assert.Equal(t, "https://scim.example.com/v2", result.Endpoint)
}

func TestClient_UpdateScimServiceProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/scim/service-provider/33333333-3333-4333-8333-333333333333", r.URL.Path)

		var req client.ScimServiceProviderCreateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "https://scim.example.com/v2/updated", req.Endpoint)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&client.ScimServiceProvider{
			ID:       "33333333-3333-4333-8333-333333333333",
			Endpoint: req.Endpoint,
			Token:    req.Token,
		}); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.UpdateScimServiceProvider(context.Background(), "33333333-3333-4333-8333-333333333333", &client.ScimServiceProviderCreateRequest{
		Endpoint:     "https://scim.example.com/v2/updated",
		Token:        "new-token",
		OidcClientID: "client-123",
	})
	assert.NoError(t, err)
	assert.Equal(t, "https://scim.example.com/v2/updated", result.Endpoint)
}

func TestClient_DeleteScimServiceProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/scim/service-provider/33333333-3333-4333-8333-333333333333", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.DeleteScimServiceProvider(context.Background(), "33333333-3333-4333-8333-333333333333")
	assert.NoError(t, err)
}

func TestClient_SyncScimServiceProvider(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/scim/service-provider/33333333-3333-4333-8333-333333333333/sync", r.URL.Path)
		assert.Equal(t, "test-token", r.Header.Get("X-API-Key"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Empty(t, body, "the sync takes no request body")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	require.NoError(t, c.SyncScimServiceProvider(context.Background(), "33333333-3333-4333-8333-333333333333"))
	assert.Equal(t, 1, requests)
}

// The sync is a POST: a failure, a rate limit or a server error is reported
// after one request, never retried.
func TestClient_SyncScimServiceProviderIsNeverRetried(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(status)
			}))
			defer server.Close()

			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			err = c.SyncScimServiceProvider(context.Background(), "33333333-3333-4333-8333-333333333333")
			require.Error(t, err)
			assert.Equal(t, 1, requests)
		})
	}
}

func TestClient_SyncScimServiceProviderRefusesAnInvalidID(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	for _, id := range []string{"", "not-a-uuid", "../scim/service-provider/x", "33333333-3333-4333-8333-333333333333/sync#"} {
		err := c.SyncScimServiceProvider(context.Background(), id)
		require.ErrorIs(t, err, client.ErrInvalidIdentifier, "id %q", id)
	}
	assert.Zero(t, requests)
}

func TestClient_GetScimServiceProviderToken(t *testing.T) {
	// The IDs contain letters, so their case matters. An answer in another
	// case is the same UUID (PostgreSQL answers with its own lower-case
	// spelling); the method returns the server's spelling, which the PUT
	// that follows must address.
	const (
		id      = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		upperID = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	)
	cases := []struct {
		name     string
		lookupID string // the ID the caller holds; id when empty
		body     string
		want     string
		wantID   string // the ID returned; id when empty
		wantErr  error
	}{
		{"a token", "", `{"id":"` + id + `","token":"held"}`, "held", "", nil},
		{"an explicit empty token", "", `{"id":"` + id + `","token":""}`, "", "", nil},
		{"an upper-case ID the caller holds in upper case", upperID, `{"id":"` + upperID + `","token":"held"}`, "held", upperID, nil},
		{"an ID that differs only in case (answer upper)", "", `{"id":"` + upperID + `","token":"held"}`, "held", upperID, nil},
		{"an ID that differs only in case (answer lower)", upperID, `{"id":"` + id + `","token":"held"}`, "held", id, nil},
		{"an empty object", "", `{}`, "", "", client.ErrUnexpectedAnswer},
		{"JSON null", "", `null`, "", "", client.ErrUnexpectedAnswer},
		{"a missing token", "", `{"id":"` + id + `"}`, "", "", client.ErrUnexpectedAnswer},
		{"a null token", "", `{"id":"` + id + `","token":null}`, "", "", client.ErrUnexpectedAnswer},
		{"a missing ID", "", `{"token":"held"}`, "", "", client.ErrUnexpectedAnswer},
		{"another ID", "", `{"id":"44444444-4444-4444-8444-444444444444","token":"held"}`, "", "", client.ErrUnexpectedAnswer},
		{"an ID that is not a UUID", "", `{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeeg","token":"held"}`, "", "", client.ErrUnexpectedAnswer},
		{"not JSON", "", `held`, "", "", client.ErrUnexpectedAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/oidc/clients/client-123/scim-service-provider", r.URL.Path)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			lookup := tc.lookupID
			if lookup == "" {
				lookup = id
			}
			got, gotID, err := c.GetScimServiceProviderToken(context.Background(), "client-123", lookup)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.NotContains(t, err.Error(), "held")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			wantID := tc.wantID
			if wantID == "" {
				wantID = id
			}
			assert.Equal(t, wantID, gotID)
		})
	}
}

func TestClient_GetScimServiceProviderTokenRefusesInvalidIdentifiers(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, _, err = c.GetScimServiceProviderToken(context.Background(), "client-123", "not-a-uuid")
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
	_, _, err = c.GetScimServiceProviderToken(context.Background(), "../x", "33333333-3333-4333-8333-333333333333")
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
	assert.Zero(t, requests)
}
