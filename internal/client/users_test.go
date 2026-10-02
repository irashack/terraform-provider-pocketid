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
			{ID: "group1", Name: "Group 1"},
			{ID: "group2", Name: "Group 2"},
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
			ID:        "user1",
			Username:  "testuser1",
			Email:     "test1@example.com",
			FirstName: "Test",
			LastName:  "User1",
			IsAdmin:   false,
			Disabled:  false,
		},
		{
			ID:        "user2",
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/users/11111111-1111-4111-8111-111111111111/user-groups", r.URL.Path)

		var req client.UpdateUserGroupsRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, []string{"group1", "group2"}, req.UserGroupIDs)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.UpdateUserGroups(context.Background(), "11111111-1111-4111-8111-111111111111", []string{"group1", "group2"})
	assert.NoError(t, err)
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
