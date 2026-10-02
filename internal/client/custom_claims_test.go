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

func TestClient_UpdateUserCustomClaims(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/custom-claims/user/test-user-id", r.URL.Path)

		var req []client.CustomClaim
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, []client.CustomClaim{
			{Key: "department", Value: "engineering"},
			{Key: "level", Value: "senior"},
		}, req)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(req)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	claims, err := c.UpdateUserCustomClaims(context.Background(), "test-user-id", []client.CustomClaim{
		{Key: "department", Value: "engineering"},
		{Key: "level", Value: "senior"},
	})
	require.NoError(t, err)
	assert.Len(t, claims, 2)
}

func TestClient_UpdateUserCustomClaims_Clear(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/custom-claims/user/test-user-id", r.URL.Path)

		// A nil slice must serialize as an empty array, not null.
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "[]", string(body))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	claims, err := c.UpdateUserCustomClaims(context.Background(), "test-user-id", nil)
	require.NoError(t, err)
	assert.Empty(t, claims)
}

func TestClient_UpdateGroupCustomClaims(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/custom-claims/user-group/test-group-id", r.URL.Path)

		var req []client.CustomClaim
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, []client.CustomClaim{{Key: "role", Value: "admin"}}, req)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(req)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	claims, err := c.UpdateGroupCustomClaims(context.Background(), "test-group-id", []client.CustomClaim{
		{Key: "role", Value: "admin"},
	})
	require.NoError(t, err)
	assert.Len(t, claims, 1)
}
