package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	signupTokenTestID    = "55555555-5555-4555-8555-555555555555"
	signupTokenTestGroup = "66666666-6666-4666-8666-666666666666"
)

func signupTokenJSON(id, token string, usageCount int) map[string]any {
	return map[string]any{
		"id": id, "token": token, "expiresAt": "2026-10-03T10:00:00Z", "usageLimit": 3, "usageCount": usageCount,
		"userGroups": []map[string]any{{"id": signupTokenTestGroup, "name": "staff", "friendlyName": "Staff"}},
		"createdAt":  "2026-10-02T10:00:00Z",
	}
}

func TestClient_CreateSignupToken(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/signup-tokens", r.URL.Path)
		body = map[string]any{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(signupTokenJSON(signupTokenTestID, "token-value", 0))
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.CreateSignupToken(context.Background(), &client.SignupTokenCreateRequest{TTL: "24h", UsageLimit: 3, UserGroupIDs: []string{signupTokenTestGroup}})
	require.NoError(t, err)
	assert.Equal(t, signupTokenTestID, result.ID)
	assert.Equal(t, "token-value", result.Token)
	assert.Equal(t, 3, result.UsageLimit)
	assert.Equal(t, []string{signupTokenTestGroup}, result.UserGroupIDs())
	assert.Equal(t, map[string]any{"ttl": "24h", "usageLimit": float64(3), "userGroupIds": []any{signupTokenTestGroup}}, body)

	// No groups is sent as an empty list, never null.
	_, err = c.CreateSignupToken(context.Background(), &client.SignupTokenCreateRequest{UsageLimit: 1})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"usageLimit": float64(1), "userGroupIds": []any{}}, body)
}

func TestClient_CreateSignupTokenRefusesAGroupIDThatIsNotAUUIDWithoutARequest(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.CreateSignupToken(context.Background(), &client.SignupTokenCreateRequest{UsageLimit: 1, UserGroupIDs: []string{"not-a-uuid"}})
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
	assert.Zero(t, requests)
}

// A failed or ambiguous creation is reported once and never repeated: a
// second POST would mint a second token.
func TestClient_CreateSignupTokenIsNeverRetried(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(status)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			_, err = c.CreateSignupToken(context.Background(), &client.SignupTokenCreateRequest{UsageLimit: 1})
			require.Error(t, err)
			if status >= 500 {
				assert.False(t, client.IsDefiniteRejection(err), "a server error does not say the token was not created")
			}
			assert.Equal(t, 1, requests)
		})
	}
}

func TestClient_CreateSignupTokenReportsAnUnusableAnswer(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantResult bool
	}{
		{"no JSON", `<html>ok</html>`, false},
		{"no ID", `{"token":"token-value"}`, false},
		{"an ID that is not a UUID", `{"id":"../x","token":"token-value"}`, false},
		{"no token value", fmt.Sprintf(`{"id":%q}`, signupTokenTestID), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			result, err := c.CreateSignupToken(context.Background(), &client.SignupTokenCreateRequest{UsageLimit: 1})
			require.ErrorIs(t, err, client.ErrResultUnread)
			assert.Equal(t, tc.wantResult, result != nil, "the ID of a token that exists is returned")
			assert.NotContains(t, err.Error(), "token-value")
			assert.Equal(t, 1, requests)
		})
	}
}

func TestClient_ListSignupTokensReadsEveryPage(t *testing.T) {
	const total = 205
	var pages []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/signup-tokens", r.URL.Path)
		page, _ := strconv.Atoi(r.URL.Query().Get("pagination[page]"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("pagination[limit]"))
		assert.Equal(t, 100, limit)
		pages = append(pages, page)
		var data []map[string]any
		for i := (page - 1) * limit; i < total && i < page*limit; i++ {
			data = append(data, signupTokenJSON(fmt.Sprintf("00000000-0000-4000-8000-%012d", i), fmt.Sprintf("token-%d", i), 0))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       data,
			"pagination": map[string]any{"totalPages": 3, "totalItems": total, "currentPage": page, "itemsPerPage": limit},
		})
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	tokens, err := c.ListSignupTokens(context.Background())
	require.NoError(t, err)
	assert.Len(t, tokens, total)
	assert.Equal(t, []int{1, 2, 3}, pages)
	assert.Equal(t, "token-204", tokens[total-1].Token)
}

func TestClient_DeleteSignupToken(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/signup-tokens/"+signupTokenTestID, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	require.NoError(t, c.DeleteSignupToken(context.Background(), signupTokenTestID))
	assert.Equal(t, 1, requests)

	for _, id := range []string{"", "not-a-uuid", "../x", signupTokenTestID + "#"} {
		require.ErrorIs(t, c.DeleteSignupToken(context.Background(), id), client.ErrInvalidIdentifier, "id %q", id)
	}
	assert.Equal(t, 1, requests, "an invalid ID sends nothing")
}
