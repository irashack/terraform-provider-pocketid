package client_test

import (
	"context"
	"encoding/json"
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
