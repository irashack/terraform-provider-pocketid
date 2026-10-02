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

func TestClient_CreateClient(t *testing.T) {
	expectedClient := &client.OIDCClient{
		ID:                       "test-client-id",
		Name:                     "Test Client",
		CallbackURLs:             []string{"https://example.com/callback"},
		IsPublic:                 false,
		PkceEnabled:              true,
		HasLogo:                  false,
		RequiresReauthentication: true,
		LaunchURL:                "https://example.com/start",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/oidc/clients", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-token", r.Header.Get("X-API-KEY"))

		var req client.OIDCClientCreateRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "Test Client", req.Name)
		assert.Equal(t, []string{"https://example.com/callback"}, req.CallbackURLs)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedClient); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	launchUrl := "https://example.com/start"
	createReq := &client.OIDCClientCreateRequest{
		Name:                     "Test Client",
		CallbackURLs:             []string{"https://example.com/callback"},
		IsPublic:                 false,
		PkceEnabled:              true,
		RequiresReauthentication: true,
		LaunchURL:                &launchUrl,
	}

	result, err := c.CreateClient(context.Background(), createReq)
	assert.NoError(t, err)
	assert.Equal(t, expectedClient, result)
}

// Pocket ID 2.17.0 returns the secret it generated for a new confidential
// client as createdSecret. Only its ID is decoded.
func TestClient_CreateClient_CreatedSecret(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want *client.CreatedClientSecret
	}{
		"2.17 created a secret": {`{"id":"c1","name":"n","createdSecret":{"id":"99999999-9999-4999-8999-999999999999","prefix":"abcd","secret":"synthetic-created-secret","isActive":true}}`, &client.CreatedClientSecret{ID: "99999999-9999-4999-8999-999999999999"}},
		// An ID that cannot be put into a path reads as no ID, which the
		// client resource treats as an unidentified created secret.
		"unusable secret ID": {`{"id":"c1","name":"n","createdSecret":{"id":"../c1","secret":"synthetic-created-secret"}}`, &client.CreatedClientSecret{}},
		"2.17 created none":  {`{"id":"c1","name":"n"}`, nil},
		"explicit null":      {`{"id":"c1","name":"n","createdSecret":null}`, nil},
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

			result, err := c.CreateClient(context.Background(), &client.OIDCClientCreateRequest{Name: "n"})
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

func TestClient_GetClient(t *testing.T) {
	expectedClient := &client.OIDCClient{
		ID:                       "test-client-id",
		Name:                     "Test Client",
		CallbackURLs:             []string{"https://example.com/callback"},
		IsPublic:                 false,
		PkceEnabled:              true,
		HasLogo:                  false,
		RequiresReauthentication: true,
		LaunchURL:                "https://example.com/start",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/oidc/clients/test-client-id", r.URL.Path)
		assert.Equal(t, "test-token", r.Header.Get("X-API-KEY"))

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedClient); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetClient(context.Background(), "test-client-id")
	assert.NoError(t, err)
	assert.Equal(t, expectedClient, result)
}

func TestClient_UpdateClient(t *testing.T) {
	expectedClient := &client.OIDCClient{
		ID:                       "test-client-id",
		Name:                     "Updated Client",
		CallbackURLs:             []string{"https://example.com/callback", "https://example.com/callback2"},
		IsPublic:                 false,
		PkceEnabled:              true,
		HasLogo:                  false,
		RequiresReauthentication: true,
		LaunchURL:                "https://example.com/start",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/oidc/clients/test-client-id", r.URL.Path)
		assert.Equal(t, "test-token", r.Header.Get("X-API-KEY"))

		var req client.OIDCClientCreateRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "Updated Client", req.Name)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedClient); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	launchUrl := "https://example.com/start"
	updateReq := &client.OIDCClientCreateRequest{
		Name:                     "Updated Client",
		CallbackURLs:             []string{"https://example.com/callback", "https://example.com/callback2"},
		IsPublic:                 false,
		PkceEnabled:              true,
		RequiresReauthentication: true,
		LaunchURL:                &launchUrl,
	}

	result, err := c.UpdateClient(context.Background(), "test-client-id", updateReq)
	assert.NoError(t, err)
	assert.Equal(t, expectedClient, result)
}

func TestClient_DeleteClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/oidc/clients/test-client-id", r.URL.Path)
		assert.Equal(t, "test-token", r.Header.Get("X-API-KEY"))

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.DeleteClient(context.Background(), "test-client-id")
	assert.NoError(t, err)
}

func TestClient_ListClients(t *testing.T) {
	expectedResponse := &client.PaginatedResponse[client.OIDCClient]{
		Data: []client.OIDCClient{
			{
				ID:   "client1",
				Name: "Client 1",
			},
			{
				ID:   "client2",
				Name: "Client 2",
			},
		},
		Pagination: client.PaginationInfo{
			TotalItems:   2,
			CurrentPage:  1,
			ItemsPerPage: 20,
			TotalPages:   1,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/oidc/clients", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedResponse); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.ListClients(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, expectedResponse, result)
}

func TestClient_UpdateClientAllowedUserGroups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/oidc/clients/test-client-id/allowed-user-groups", r.URL.Path)

		var req client.UpdateAllowedUserGroupsRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, []string{"group1", "group2"}, req.UserGroupIDs)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.UpdateClientAllowedUserGroups(context.Background(), "test-client-id", []string{"group1", "group2"})
	assert.NoError(t, err)
}

func TestClient_CreateClient_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"invalid json":}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	createReq := &client.OIDCClientCreateRequest{
		Name:         "test-client",
		CallbackURLs: []string{"https://example.com/callback"},
		IsPublic:     false,
		PkceEnabled:  true,
	}

	result, err := c.CreateClient(context.Background(), createReq)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

func TestClient_UpdateClient_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"invalid json":}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	updateReq := &client.OIDCClientCreateRequest{
		Name:         "updated-client",
		CallbackURLs: []string{"https://example.com/callback"},
		IsPublic:     false,
		PkceEnabled:  true,
	}

	result, err := c.UpdateClient(context.Background(), "test-id", updateReq)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

func TestClient_ListClients_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"data": "should be array"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.ListClients(context.Background())
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}
