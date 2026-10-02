package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestClient_GetCurrentVersion(t *testing.T) {
	t.Run("returns the version if endpoint exists", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/api/version/current", r.URL.Path)

			w.Header().Set("Content-Type", "application/json")
			// API only returns the token
			response := map[string]string{
				"currentVersion": "1.2.3",
			}
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Fatalf("Failed to encode response: %v", err)
			}
		}))
		defer server.Close()

		c, err := client.NewClient(server.URL, "test-token", false, 30)
		require.NoError(t, err)

		version, err := c.GetCurrentVersion(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "1.2.3", version)
	})

	t.Run("returns an empty string if endpoint doesn't exist", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "/api/version/current", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":"API endpoint not found"}`)
		}))
		defer server.Close()

		c, err := client.NewClient(server.URL, "test-token", false, 30)
		require.NoError(t, err)

		version, err := c.GetCurrentVersion(context.Background())
		assert.NoError(t, err)
		assert.Empty(t, version)
	})
}
