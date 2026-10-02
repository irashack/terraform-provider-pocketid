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

// Test Group-related methods
func TestClient_CreateUserGroup(t *testing.T) {
	expectedGroup := &client.UserGroup{
		ID:           "test-group-id",
		Name:         "test-group",
		FriendlyName: "Test Group",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/user-groups", r.URL.Path)

		var req client.UserGroupCreateRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "test-group", req.Name)
		assert.Equal(t, "Test Group", req.FriendlyName)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedGroup); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	createReq := &client.UserGroupCreateRequest{
		Name:         "test-group",
		FriendlyName: "Test Group",
	}

	result, err := c.CreateUserGroup(context.Background(), createReq)
	assert.NoError(t, err)
	assert.Equal(t, expectedGroup, result)
}

func TestClient_GetUserGroup(t *testing.T) {
	expectedGroup := &client.UserGroup{
		ID:           "test-group-id",
		Name:         "test-group",
		FriendlyName: "Test Group",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/user-groups/test-group-id", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedGroup); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetUserGroup(context.Background(), "test-group-id")
	assert.NoError(t, err)
	assert.Equal(t, expectedGroup, result)
}

func TestClient_GetUserGroup_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		if _, err := fmt.Fprint(w, `{"error": "Group not found"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetUserGroup(context.Background(), "nonexistent-id")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestClient_UpdateUserGroup(t *testing.T) {
	updateReq := &client.UserGroupCreateRequest{
		Name:         "updated-group",
		FriendlyName: "Updated Group",
	}

	expectedGroup := &client.UserGroup{
		ID:           "test-group-id",
		Name:         updateReq.Name,
		FriendlyName: updateReq.FriendlyName,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/user-groups/test-group-id", r.URL.Path)

		var req client.UserGroupCreateRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, updateReq, &req)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedGroup); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.UpdateUserGroup(context.Background(), "test-group-id", updateReq)
	assert.NoError(t, err)
	assert.Equal(t, expectedGroup, result)
}

func TestClient_DeleteUserGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/user-groups/test-group-id", r.URL.Path)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.DeleteUserGroup(context.Background(), "test-group-id")
	assert.NoError(t, err)
}

func TestClient_DeleteUserGroup_InUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		if _, err := fmt.Fprint(w, `{"error": "Group is in use"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.DeleteUserGroup(context.Background(), "test-group-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 409")
}

func TestClient_ListUserGroups(t *testing.T) {
	expectedGroups := []client.UserGroup{
		{
			ID:           "group1",
			Name:         "admins",
			FriendlyName: "Administrators",
		},
		{
			ID:           "group2",
			Name:         "users",
			FriendlyName: "Regular Users",
		},
		{
			ID:           "group3",
			Name:         "developers",
			FriendlyName: "Developers",
		},
	}

	expectedResponse := &client.PaginatedResponse[client.UserGroup]{
		Data: expectedGroups,
		Pagination: client.PaginationInfo{
			TotalItems:   3,
			CurrentPage:  1,
			ItemsPerPage: 10,
			TotalPages:   1,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/user-groups", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(expectedResponse); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.ListUserGroups(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, expectedResponse, result)
	assert.Equal(t, expectedGroups, result.Data)
}

func TestClient_ListUserGroups_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := fmt.Fprint(w, `{"error": "Internal server error"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.ListUserGroups(context.Background())
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestClient_CreateUserGroup_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"name": true}`); err != nil { // name should be string
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	createReq := &client.UserGroupCreateRequest{
		Name:         "test-group",
		FriendlyName: "Test Group",
	}

	result, err := c.CreateUserGroup(context.Background(), createReq)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

func TestClient_UpdateUserGroup_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"friendlyName": 123}`); err != nil { // friendlyName should be string
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	updateReq := &client.UserGroupCreateRequest{
		Name:         "test-group",
		FriendlyName: "Test Group",
	}

	result, err := c.UpdateUserGroup(context.Background(), "test-id", updateReq)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

func TestClient_GetUserGroup_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"id": {}, "name": "test"}`); err != nil { // id should be string
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetUserGroup(context.Background(), "test-id")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

func TestClient_ListUserGroups_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"data": {}, "pagination": "invalid"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.ListUserGroups(context.Background())
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}
