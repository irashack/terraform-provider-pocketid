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

func TestClient_CreateOneTimeAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/users/11111111-1111-4111-8111-111111111111/one-time-access-token", r.URL.Path)

		var req client.OneTimeAccessTokenRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)

		w.Header().Set("Content-Type", "application/json")
		// API only returns the token
		response := map[string]string{
			"token": "test-token-123456",
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	token, err := c.CreateOneTimeAccessToken(context.Background(), "11111111-1111-4111-8111-111111111111", &client.OneTimeAccessTokenRequest{TTL: "15m"})
	assert.NoError(t, err)
	assert.Equal(t, "test-token-123456", token.Token)
}

func TestClient_CreateOneTimeAccessToken_SendsTTL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		// The provider must send the lifetime as a "ttl" duration string.
		assert.Equal(t, "1h", body["ttl"])
		_, hasExpiresAt := body["expiresAt"]
		assert.False(t, hasExpiresAt, "request must not send expiresAt")

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"token": "tok"}); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	token, err := c.CreateOneTimeAccessToken(context.Background(), "11111111-1111-4111-8111-111111111111", &client.OneTimeAccessTokenRequest{TTL: "1h"})
	assert.NoError(t, err)
	assert.Equal(t, "tok", token.Token)
}

func TestClient_CreateOneTimeAccessToken_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if err := json.NewEncoder(w).Encode(&client.ErrorResponse{
			Error: "invalid ttl",
		}); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.CreateOneTimeAccessToken(context.Background(), "11111111-1111-4111-8111-111111111111", &client.OneTimeAccessTokenRequest{TTL: "1s"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 400")
}
