package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	groupMembersGroup = "22222222-2222-4222-8222-222222222222"
	groupMembersUser1 = "33333333-3333-4333-8333-333333333333"
	groupMembersUser2 = "44444444-4444-4444-8444-444444444444"
)

func TestClient_SetGroupMembers_SendsTheWholeSetAndReturnsWhatTheServerHolds(t *testing.T) {
	var body struct {
		UserIDs []string `json:"userIds"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/api/user-groups/"+groupMembersGroup+"/users", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		// The server keeps only the first of the two IDs.
		_, _ = w.Write([]byte(`{"id":"` + groupMembersGroup + `","users":[{"id":"` + groupMembersUser1 + `"}]}`))
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	got, err := c.SetGroupMembers(context.Background(), groupMembersGroup, []string{groupMembersUser1, groupMembersUser2})
	require.NoError(t, err)
	assert.Equal(t, []string{groupMembersUser1, groupMembersUser2}, body.UserIDs)
	assert.Equal(t, []string{groupMembersUser1}, got, "what the server holds, not what was asked for")
}

// The server rejects null and drops IDs that are not users, so the client
// sends [] for no members and refuses an ID that is not a UUID.
func TestClient_SetGroupMembers_EmptyIsSentAsAnEmptyList(t *testing.T) {
	var raw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&m))
		raw = string(m["userIds"])
		_, _ = w.Write([]byte(`{"users":null}`))
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	got, err := c.SetGroupMembers(context.Background(), groupMembersGroup, nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", raw)
	assert.Empty(t, got)
}

func TestClient_SetGroupMembers_RefusesBadIdentifiersWithoutSending(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.SetGroupMembers(context.Background(), "../x", nil)
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
	_, err = c.SetGroupMembers(context.Background(), groupMembersGroup, []string{groupMembersUser1, "bob"})
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
	assert.Zero(t, requests.Load())
}

// An answer that does not list the users leaves the result unknown: the change
// was made.
func TestClient_SetGroupMembers_UnreadableAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.SetGroupMembers(context.Background(), groupMembersGroup, []string{groupMembersUser1})
	require.ErrorIs(t, err, client.ErrResultUnread)
}

// A server error is reported once and never repeated.
func TestClient_SetGroupMembers_NotRetried(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.SetGroupMembers(context.Background(), groupMembersGroup, []string{groupMembersUser1})
	require.Error(t, err)
	assert.Equal(t, int32(1), requests.Load())
}
