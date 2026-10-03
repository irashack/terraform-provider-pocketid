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

// Test User-related methods
func TestClient_CreateUser(t *testing.T) {
	expectedUser := &client.User{
		ID:        "11111111-1111-4111-8111-111111111111",
		Username:  "testuser",
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
		IsAdmin:   false,
		Disabled:  false,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/users", r.URL.Path)

		var req client.UserCreateRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "testuser", req.Username)
		assert.Equal(t, "test@example.com", req.Email)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedUser); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	createReq := &client.UserCreateRequest{
		Username:  "testuser",
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
	}

	result, err := c.CreateUser(context.Background(), createReq)
	assert.NoError(t, err)
	assert.Equal(t, expectedUser, result)
}

func TestClient_GetUser(t *testing.T) {
	expectedUser := &client.User{
		ID:        "11111111-1111-4111-8111-111111111111",
		Username:  "testuser",
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
		IsAdmin:   false,
		Disabled:  false,
		Locale:    stringPtr("en"),
		UserGroups: []client.UserGroup{
			{ID: "cccccccc-0000-4000-8000-000000000001", Name: "Group 1"},
			{ID: "cccccccc-0000-4000-8000-000000000002", Name: "Group 2"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/users/11111111-1111-4111-8111-111111111111", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedUser); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetUser(context.Background(), "11111111-1111-4111-8111-111111111111")
	assert.NoError(t, err)
	assert.Equal(t, expectedUser, result)
}

func TestClient_GetUser_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		if _, err := fmt.Fprint(w, `{"error": "User not found"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetUser(context.Background(), "77777777-7777-4777-8777-777777777777")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestClient_UpdateUser(t *testing.T) {
	updateReq := &client.UserCreateRequest{
		Username:  "updateduser",
		Email:     "updated@example.com",
		FirstName: "Updated",
		LastName:  "User",
		IsAdmin:   true,
		Disabled:  false,
		Locale:    stringPtr("fr"),
	}

	expectedUser := &client.User{
		ID:        "11111111-1111-4111-8111-111111111111",
		Username:  updateReq.Username,
		Email:     updateReq.Email,
		FirstName: updateReq.FirstName,
		LastName:  updateReq.LastName,
		IsAdmin:   updateReq.IsAdmin,
		Disabled:  updateReq.Disabled,
		Locale:    updateReq.Locale,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/users/11111111-1111-4111-8111-111111111111", r.URL.Path)

		var req client.UserCreateRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, updateReq, &req)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedUser); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.UpdateUser(context.Background(), "11111111-1111-4111-8111-111111111111", updateReq)
	assert.NoError(t, err)
	assert.Equal(t, expectedUser, result)
}

func TestClient_DeleteUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/users/11111111-1111-4111-8111-111111111111", r.URL.Path)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.DeleteUser(context.Background(), "11111111-1111-4111-8111-111111111111")
	assert.NoError(t, err)
}

func TestClient_DeleteUser_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		if _, err := fmt.Fprint(w, `{"error": "Insufficient permissions"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.DeleteUser(context.Background(), "11111111-1111-4111-8111-111111111111")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestClient_ListUsers(t *testing.T) {
	expectedUsers := []client.User{
		{
			ID:        "dddddddd-0000-4000-8000-000000000001",
			Username:  "testuser1",
			Email:     "test1@example.com",
			FirstName: "Test",
			LastName:  "User1",
			IsAdmin:   false,
			Disabled:  false,
		},
		{
			ID:        "dddddddd-0000-4000-8000-000000000002",
			Username:  "testuser2",
			Email:     "test2@example.com",
			FirstName: "Test",
			LastName:  "User2",
			IsAdmin:   true,
			Disabled:  false,
			Locale:    stringPtr("en"),
		},
	}

	expectedResponse := &client.PaginatedResponse[client.User]{
		Data: expectedUsers,
		Pagination: client.PaginationInfo{
			TotalItems:   2,
			CurrentPage:  1,
			ItemsPerPage: 10,
			TotalPages:   1,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/users", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedResponse); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.ListUsers(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, expectedResponse, result)
	assert.Equal(t, expectedUsers, result.Data)
}

func TestClient_ListUsers_Empty(t *testing.T) {
	expectedResponse := &client.PaginatedResponse[client.User]{
		Data: []client.User{},
		Pagination: client.PaginationInfo{
			TotalItems:   0,
			CurrentPage:  1,
			ItemsPerPage: 10,
			TotalPages:   0,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/users", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedResponse); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.ListUsers(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, expectedResponse, result)
	assert.Empty(t, result.Data)
}

func TestClient_UpdateUserGroups(t *testing.T) {
	const user = "11111111-1111-4111-8111-111111111111"
	const g1, unknown = "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	const other = "44444444-4444-4444-8444-444444444444"
	for name, tc := range map[string]struct {
		request    []string
		wantBody   string
		status     int
		response   string
		want       []string
		wantUnread bool
		wantStatus int
	}{
		// The response (UserDto) lists the groups the user is in afterwards.
		"unknown ID dropped": {request: []string{g1, unknown}, wantBody: `{"userGroupIds":["` + g1 + `","` + unknown + `"]}`, status: 200,
			response: `{"id":"` + user + `","userGroups":[{"id":"` + g1 + `"}]}`, want: []string{g1}},
		// UserDto.userGroups has no omitempty, so no groups is null.
		"nil sends an empty list": {request: nil, wantBody: `{"userGroupIds":[]}`, status: 200, response: `{"id":"` + user + `","userGroups":null}`, want: []string{}},
		"empty list":              {request: []string{}, wantBody: `{"userGroupIds":[]}`, status: 200, response: `{"id":"` + user + `","userGroups":[]}`, want: []string{}},
		"result not listed":       {request: []string{g1}, status: 200, response: `{"id":"` + user + `"}`, wantUnread: true},
		"empty body":              {request: []string{g1}, status: 200, response: ``, wantUnread: true},
		// The response is evidence only when it names the addressed user
		// and every group it lists carries an ID, as for a read of the user.
		"wrong user ID":                {request: []string{g1}, status: 200, response: `{"id":"` + other + `","userGroups":[{"id":"` + g1 + `"}]}`, wantUnread: true},
		"missing user ID":              {request: []string{g1}, status: 200, response: `{"userGroups":[{"id":"` + g1 + `"}]}`, wantUnread: true},
		"user ID not a string":         {request: []string{g1}, status: 200, response: `{"id":5,"userGroups":[{"id":"` + g1 + `"}]}`, wantUnread: true},
		"group without ID":             {request: []string{g1}, status: 200, response: `{"id":"` + user + `","userGroups":[{}]}`, wantUnread: true},
		"group without ID and no user": {request: []string{g1}, status: 200, response: `{"userGroups":[{}]}`, wantUnread: true},
		"other user, no groups":        {request: nil, status: 200, response: `{"id":"` + other + `","userGroups":[]}`, wantUnread: true},
		"other user, null groups":      {request: nil, status: 200, response: `{"id":"` + other + `","userGroups":null}`, wantUnread: true},
		"not an object":                {request: []string{g1}, status: 200, response: `null`, wantUnread: true},
		"groups not a list":            {request: []string{g1}, status: 200, response: `{"id":"` + user + `","userGroups":"` + g1 + `"}`, wantUnread: true},
		"rejected":                     {request: []string{g1}, status: 400, response: `{"error":"x"}`, wantStatus: 400},
	} {
		t.Run(name, func(t *testing.T) {
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "PUT", r.Method)
				assert.Equal(t, "/api/users/"+user+"/user-groups", r.URL.Path)
				puts++
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				if tc.wantBody != "" {
					assert.JSONEq(t, tc.wantBody, string(body))
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			got, err := c.UpdateUserGroups(context.Background(), user, tc.request)
			assert.Equal(t, 1, puts)
			switch {
			case tc.wantStatus != 0:
				var status *client.HTTPError
				require.ErrorAs(t, err, &status)
				assert.Equal(t, tc.wantStatus, status.StatusCode)
				assert.False(t, errors.Is(err, client.ErrResultUnread))
			case tc.wantUnread:
				assert.ErrorIs(t, err, client.ErrResultUnread)
				assert.Nil(t, got)
			default:
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestClient_CreateUser_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"username": 123}`); err != nil { // username should be string
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	createReq := &client.UserCreateRequest{
		Username: "testuser",
		Email:    "test@example.com",
	}

	result, err := c.CreateUser(context.Background(), createReq)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

func TestClient_UpdateUser_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"email": []}`); err != nil { // email should be string
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	updateReq := &client.UserCreateRequest{
		Username: "testuser",
		Email:    "test@example.com",
	}

	result, err := c.UpdateUser(context.Background(), "88888888-8888-4888-8888-888888888888", updateReq)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

func TestClient_ListUsers_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"data": "not an array", "pagination": {}}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.ListUsers(context.Background())
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

func TestClient_GetUser_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"id": [], "username": "test"}`); err != nil { // id should be string
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetUser(context.Background(), "88888888-8888-4888-8888-888888888888")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}
