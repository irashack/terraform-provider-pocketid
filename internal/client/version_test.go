package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A semantic version can carry arbitrary text in its pre-release or build
// metadata, so a server can return the API key it received inside a valid
// version; a static key can be all digits, which every part accepts. Such a
// version is refused with an error that does not contain it, wherever the
// key appears and however the key was configured, so the version data source
// can never store it. Ordinary build metadata and pre-release versions still
// pass, including ones with digits of their own.
func TestClient_GetCurrentVersionRefusesTheReflectedKey(t *testing.T) {
	const key = "4815162342108151" // a synthetic numeric static key
	serve := func(version func(received string) string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"currentVersion": version(r.Header.Get("X-API-KEY"))})
		}))
	}
	reflected := map[string]func(string) string{
		"build metadata":         func(k string) string { return "2.17.0+" + k },
		"build metadata, v":      func(k string) string { return "v2.17.0+build." + k },
		"pre-release":            func(k string) string { return "2.17.0-rc." + k },
		"pre-release and build":  func(k string) string { return "2.17.0-rc.1+" + k + ".x" },
		"key as a version field": func(k string) string { return "2." + k + ".0" },
	}
	for name, version := range reflected {
		for form, token := range map[string]string{"bare": key, "padded": " \t" + key + " "} {
			t.Run(name+"/"+form, func(t *testing.T) {
				server := serve(version)
				defer server.Close()
				c, err := client.NewClient(server.URL, token, false, 30)
				require.NoError(t, err)

				got, err := c.GetCurrentVersion(context.Background())
				require.Error(t, err)
				assert.Empty(t, got)
				assert.Contains(t, err.Error(), "contains the API key")
				assert.NotContains(t, err.Error(), key)

				atLeast, err := c.VersionAtLeast(context.Background(), "2.14.0")
				require.Error(t, err)
				assert.False(t, atLeast)
				assert.NotContains(t, err.Error(), key)
			})
		}
	}

	for _, ordinary := range []string{"2.17.0+build.5", "2.17.0+4815162342108152", "2.17.0-rc.1", "v2.16.0+sha.0a1b2c3"} {
		server := serve(func(string) string { return ordinary })
		c, err := client.NewClient(server.URL, key, false, 30)
		require.NoError(t, err)
		got, err := c.GetCurrentVersion(context.Background())
		server.Close()
		require.NoError(t, err, ordinary)
		assert.Equal(t, strings.TrimPrefix(ordinary, "v"), got)
	}
}
