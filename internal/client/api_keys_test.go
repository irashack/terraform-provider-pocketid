package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestClient_ListAPIKeysReadsEveryPageAndNeverKeepsAKeyValue(t *testing.T) {
	const total = 120
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/api-keys", r.URL.Path)
		page, _ := strconv.Atoi(r.URL.Query().Get("pagination[page]"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("pagination[limit]"))
		var data []map[string]any
		for i := (page - 1) * limit; i < total && i < page*limit; i++ {
			key := map[string]any{
				"id": fmt.Sprintf("00000000-0000-4000-8000-%012d", i), "name": fmt.Sprintf("key-%d", i),
				"description": nil, "expiresAt": "2026-12-01T00:00:00Z", "lastUsedAt": nil, "createdAt": "2026-10-01T00:00:00Z",
				// A server never sends these; the client must not carry them if one did.
				"key": "must-not-be-kept", "token": "must-not-be-kept-either",
			}
			if i == 0 {
				key["description"] = "management key"
				key["lastUsedAt"] = "2026-10-02T09:00:00Z"
			}
			data = append(data, key)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       data,
			"pagination": map[string]any{"totalPages": 2, "totalItems": total, "currentPage": page, "itemsPerPage": limit},
		})
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	keys, err := c.ListAPIKeys(context.Background())
	require.NoError(t, err)
	require.Len(t, keys, total)
	require.NotNil(t, keys[0].Description)
	assert.Equal(t, "management key", *keys[0].Description)
	require.NotNil(t, keys[0].LastUsedAt)
	assert.Nil(t, keys[1].Description)
	assert.Nil(t, keys[1].LastUsedAt)
	assert.Equal(t, "2026-12-01T00:00:00Z", keys[1].ExpiresAt)

	encoded, err := json.Marshal(keys)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "must-not-be-kept")
	for i := 0; i < reflect.TypeOf(client.APIKey{}).NumField(); i++ {
		name := reflect.TypeOf(client.APIKey{}).Field(i).Name
		assert.NotContains(t, []string{"Key", "Token", "Value"}, name, "the model must have no field that could hold a key")
	}
}

func TestClient_ListAPIKeysFailureIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"leaked","code":"api_key_auth_not_allowed"}`))
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.ListAPIKeys(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "leaked")
}
