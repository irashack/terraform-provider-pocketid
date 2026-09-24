package client_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// paginatedUsersServer serves GET /api/users honoring pagination[page] and
// pagination[limit], splitting users into pages of the requested size (or
// pageSize when the client sends none). It records every request's raw
// query string so a test can assert what ListAllUsers actually sent.
func paginatedUsersServer(t *testing.T, users []client.User, pageSize int) (*client.Client, *[]string) {
	t.Helper()

	var queries []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/users", r.URL.Path)
		queries = append(queries, r.URL.RawQuery)

		page := 1
		limit := pageSize
		if v := r.URL.Query().Get("pagination[page]"); v != "" {
			_, _ = fmt.Sscanf(v, "%d", &page)
		}
		if v := r.URL.Query().Get("pagination[limit]"); v != "" {
			_, _ = fmt.Sscanf(v, "%d", &limit)
		}
		if limit <= 0 {
			limit = pageSize
		}

		totalPages := (len(users) + limit - 1) / limit
		if totalPages == 0 {
			totalPages = 1
		}

		start := (page - 1) * limit
		end := start + limit
		if start > len(users) {
			start = len(users)
		}
		if end > len(users) {
			end = len(users)
		}

		resp := client.PaginatedResponse[client.User]{
			Data: users[start:end],
			Pagination: client.PaginationInfo{
				TotalItems:   len(users),
				CurrentPage:  page,
				ItemsPerPage: limit,
				TotalPages:   totalPages,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	return c, &queries
}

func makeUsers(n int) []client.User {
	users := make([]client.User, n)
	for i := range users {
		users[i] = client.User{
			ID:       fmt.Sprintf("user-%d", i),
			Username: fmt.Sprintf("user%d", i),
			Email:    fmt.Sprintf("user%d@example.com", i),
		}
	}
	return users
}

func TestClient_ListUsersPage_SetsQueryParams(t *testing.T) {
	c, queries := paginatedUsersServer(t, makeUsers(1), 20)

	_, err := c.ListUsersPage(2, 50, "alice")
	require.NoError(t, err)

	require.Len(t, *queries, 1)
	values := (*queries)[0]
	assert.Contains(t, values, "pagination%5Bpage%5D=2")
	assert.Contains(t, values, "pagination%5Blimit%5D=50")
	assert.Contains(t, values, "search=alice")
}

func TestClient_ListUsersPage_NoParamsWhenZero(t *testing.T) {
	c, queries := paginatedUsersServer(t, makeUsers(1), 20)

	_, err := c.ListUsersPage(0, 0, "")
	require.NoError(t, err)

	require.Len(t, *queries, 1)
	assert.Empty(t, (*queries)[0], "no query string should be sent when page/limit/search are all unset")
}

// TestClient_ListAllUsers_FollowsPagination is the core fix for the P2
// finding: a single ListUsers() call only sees the first page (server
// default 20 items), silently missing every user beyond it. ListAllUsers
// requests pages of 100 (see client.go), so this needs more than 100 users
// to force several round trips.
func TestClient_ListAllUsers_FollowsPagination(t *testing.T) {
	want := makeUsers(250)
	c, queries := paginatedUsersServer(t, want, 20)

	got, err := c.ListAllUsers("")
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 3, len(*queries), "250 users at ListAllUsers' page size of 100 must take exactly 3 requests")
}

// TestClient_ListAllUsers_FindsUserOnPageTwo is the specific regression the
// review asked for: a user that only appears on page 2 must still be found
// by paging through fully, which is what the pocketid_user data source's
// username/email lookups and the pocketid_users data source now do via
// ListAllUsers. ListAllUsers requests pages of 100, so the target user needs
// an index past that to land on page 2.
func TestClient_ListAllUsers_FindsUserOnPageTwo(t *testing.T) {
	users := makeUsers(150) // user149 is index 149: page 2 of 100
	c, queries := paginatedUsersServer(t, users, 20)

	all, err := c.ListAllUsers("")
	require.NoError(t, err)
	require.Len(t, *queries, 2, "150 users at 100/page must take exactly 2 requests")

	var found *client.User
	for i := range all {
		if all[i].Username == "user149" {
			found = &all[i]
			break
		}
	}
	require.NotNil(t, found, "user149 (page 2) must be present in ListAllUsers' result")
	assert.Equal(t, "user-149", found.ID)
}

func TestClient_ListAllUsers_EmptyResult(t *testing.T) {
	c, queries := paginatedUsersServer(t, nil, 20)

	got, err := c.ListAllUsers("")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Len(t, *queries, 1, "an empty first page must stop immediately, not loop")
}

func TestClient_ListAllUsers_PropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error": "Internal server error"}`)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.ListAllUsers("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}
