package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestClient_CreateClient(t *testing.T) {
	expectedClient := &client.OIDCClient{
		ID:                       "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
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
		"2.17 created a secret": {`{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","name":"n","createdSecret":{"id":"99999999-9999-4999-8999-999999999999","prefix":"abcd","secret":"synthetic-created-secret","isActive":true}}`, &client.CreatedClientSecret{ID: "99999999-9999-4999-8999-999999999999"}},
		// An ID that cannot be put into a path reads as no ID, which the
		// client resource treats as an unidentified created secret.
		"unusable secret ID": {`{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","name":"n","createdSecret":{"id":"../c1","secret":"synthetic-created-secret"}}`, &client.CreatedClientSecret{}},
		"2.17 created none":  {`{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","name":"n"}`, nil},
		"explicit null":      {`{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","name":"n","createdSecret":null}`, nil},
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
		AllowedUserGroups:        []client.UserGroup{{ID: "aaaaaaaa-0000-4000-8000-000000000001", Name: "g", FriendlyName: "G"}},
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
		AllowedUserGroups:        []client.UserGroup{{ID: "aaaaaaaa-0000-4000-8000-000000000001", Name: "g", FriendlyName: "G"}},
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
	assert.Equal(t, expectedResponse.Data, result)
}

func TestClient_UpdateClientAllowedUserGroups(t *testing.T) {
	const g1, g2, unknown = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	for name, tc := range map[string]struct {
		request    []string
		wantBody   string
		putStatus  int
		getStatus  int
		getGroups  string
		want       []string
		wantUnread bool
		wantStatus int
		wantGets   int
	}{
		// The PUT's response (OidcClientDto) carries no groups: the result
		// comes from reading the client back, and shows what was dropped.
		"unknown ID dropped": {request: []string{g1, unknown, g2}, wantBody: `{"userGroupIds":["` + g1 + `","` + unknown + `","` + g2 + `"]}`,
			putStatus: 200, getStatus: 200, getGroups: `[{"id":"` + g1 + `"},{"id":"` + g2 + `"}]`, want: []string{g1, g2}, wantGets: 1},
		"nil sends an empty list": {request: nil, wantBody: `{"userGroupIds":[]}`, putStatus: 200, getStatus: 200, getGroups: `[]`, want: []string{}, wantGets: 1},
		"groups null on read":     {request: []string{}, wantBody: `{"userGroupIds":[]}`, putStatus: 200, getStatus: 200, getGroups: `null`, want: []string{}, wantGets: 1},
		// A response without the field never confirms an empty set.
		"groups omitted on read":    {request: []string{}, putStatus: 200, getStatus: 200, getGroups: ``, wantUnread: true, wantGets: 1},
		"groups not a list on read": {request: []string{g1}, putStatus: 200, getStatus: 200, getGroups: `{"id":"x"}`, wantUnread: true, wantGets: 1},
		"rejected":                  {request: []string{g1}, putStatus: 400, wantStatus: 400},
		"server error not retried":  {request: []string{g1}, putStatus: 503, wantStatus: 503},
		"read back fails":           {request: []string{g1}, putStatus: 200, getStatus: 403, wantUnread: true, wantStatus: 403, wantGets: 1},
	} {
		t.Run(name, func(t *testing.T) {
			puts, gets := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "PUT /api/oidc/clients/test-client-id/allowed-user-groups":
					puts++
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					if tc.wantBody != "" {
						assert.JSONEq(t, tc.wantBody, string(body))
					}
					w.WriteHeader(tc.putStatus)
					_, _ = fmt.Fprint(w, `{"id":"test-client-id","name":"n","isGroupRestricted":true}`)
				case "GET /api/oidc/clients/test-client-id":
					gets++
					w.WriteHeader(tc.getStatus)
					groups := ""
					if tc.getGroups != "" {
						groups = `,"allowedUserGroups":` + tc.getGroups
					}
					_, _ = fmt.Fprint(w, `{"id":"test-client-id","name":"n"`+groups+`}`)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			got, err := c.UpdateClientAllowedUserGroups(context.Background(), "test-client-id", tc.request)
			assert.Equal(t, 1, puts, "the PUT is sent once")
			assert.Equal(t, tc.wantGets, gets)
			if tc.wantUnread && tc.wantStatus == 0 {
				require.ErrorIs(t, err, client.ErrResultUnread)
				assert.Nil(t, got)
				return
			}
			if tc.wantStatus != 0 {
				var status *client.HTTPError
				require.ErrorAs(t, err, &status)
				assert.Equal(t, tc.wantStatus, status.StatusCode)
				assert.Equal(t, tc.wantUnread, errors.Is(err, client.ErrResultUnread))
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
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
