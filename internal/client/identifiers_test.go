package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const validUUID = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"

// Identifiers that would change which endpoint a request reaches if they were
// put into a path: a separator, a fragment, a query, a relative segment, an
// encoded separator and a suffix that turns a valid ID into another route.
var unsafeIdentifiers = []string{
	"a/b",
	"a#b",
	"a?b=c",
	"..",
	".",
	"a%2Fb",
	validUUID + "/one-time-access-token#",
	validUUID + "/../" + validUUID,
	"",
	" " + validUUID,
}

func TestValidateUUID(t *testing.T) {
	for _, id := range []string{validUUID, strings.ToUpper(validUUID), "00000000-0000-0000-0000-000000000000"} {
		assert.NoError(t, client.ValidateUUID("user", id), id)
	}
	for _, id := range append([]string{"user-1", validUUID + "0", "0b6f4f2e7c1a4d3e9f102a3b4c5d6e7f", "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7g"}, unsafeIdentifiers...) {
		err := client.ValidateUUID("user", id)
		assert.ErrorIs(t, err, client.ErrInvalidIdentifier, id)
		if err != nil && id != "" {
			assert.NotContains(t, err.Error(), id, "the rejected value is not echoed")
		}
	}
}

func TestValidateClientID(t *testing.T) {
	for _, id := range []string{"ab", "my-app", "my_app.v2", validUUID, "...", strings.Repeat("a", 128)} {
		assert.NoError(t, client.ValidateClientID(id), id)
	}
	for _, id := range append([]string{"a", strings.Repeat("a", 129), "my app", "https://example.com/client.json", "ä-client"}, unsafeIdentifiers...) {
		assert.ErrorIs(t, client.ValidateClientID(id), client.ErrInvalidIdentifier, id)
	}
}

// Every method that puts an identifier into a path refuses an unsafe one
// before sending anything.
func TestClient_RefusesUnsafeIdentifiers(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	ctx := context.Background()

	calls := map[string]func(id string) error{
		"GetClient": func(id string) error { _, err := c.GetClient(ctx, id); return err },
		"UpdateClient": func(id string) error {
			_, err := c.UpdateClient(ctx, id, &client.OIDCClientCreateRequest{})
			return err
		},
		"DeleteClient":                  func(id string) error { return c.DeleteClient(ctx, id) },
		"UpdateClientAllowedUserGroups": func(id string) error { _, err := c.UpdateClientAllowedUserGroups(ctx, id, nil); return err },
		"GenerateClientSecret":          func(id string) error { _, err := c.GenerateClientSecret(ctx, id); return err },
		"ListClientSecrets":             func(id string) error { _, err := c.ListClientSecrets(ctx, id); return err },
		"DeleteClientSecret client":     func(id string) error { return c.DeleteClientSecret(ctx, id, validUUID) },
		"DeleteClientSecret secret":     func(id string) error { return c.DeleteClientSecret(ctx, "client", id) },
		"GetClientScimServiceProvider":  func(id string) error { _, err := c.GetClientScimServiceProvider(ctx, id); return err },
		"GetUser":                       func(id string) error { _, err := c.GetUser(ctx, id); return err },
		"UpdateUser":                    func(id string) error { _, err := c.UpdateUser(ctx, id, &client.UserCreateRequest{}); return err },
		"DeleteUser":                    func(id string) error { return c.DeleteUser(ctx, id) },
		"UpdateUserGroups":              func(id string) error { _, err := c.UpdateUserGroups(ctx, id, nil); return err },
		"AddUserToGroup":                func(id string) error { return c.AddUserToGroup(ctx, id, validUUID) },
		"RemoveUserFromGroup":           func(id string) error { return c.RemoveUserFromGroup(ctx, id, validUUID) },
		"UserHasGroupMembership":        func(id string) error { _, err := c.UserHasGroupMembership(ctx, id, validUUID); return err },
		"UpdateUserCustomClaims":        func(id string) error { _, err := c.UpdateUserCustomClaims(ctx, id, nil); return err },
		"CreateOneTimeAccessToken": func(id string) error {
			_, err := c.CreateOneTimeAccessToken(ctx, id, &client.OneTimeAccessTokenRequest{TTL: "1h"})
			return err
		},
		"GetUserGroup": func(id string) error { _, err := c.GetUserGroup(ctx, id); return err },
		"UpdateUserGroup": func(id string) error {
			_, err := c.UpdateUserGroup(ctx, id, &client.UserGroupCreateRequest{})
			return err
		},
		"DeleteUserGroup":         func(id string) error { return c.DeleteUserGroup(ctx, id) },
		"UpdateGroupCustomClaims": func(id string) error { _, err := c.UpdateGroupCustomClaims(ctx, id, nil); return err },
		"UpdateScimServiceProvider": func(id string) error {
			_, err := c.UpdateScimServiceProvider(ctx, id, &client.ScimServiceProviderCreateRequest{})
			return err
		},
		"DeleteScimServiceProvider": func(id string) error { return c.DeleteScimServiceProvider(ctx, id) },
	}
	for name, call := range calls {
		for _, id := range unsafeIdentifiers {
			err := call(id)
			assert.ErrorIs(t, err, client.ErrInvalidIdentifier, "%s(%q)", name, id)
		}
	}
	assert.Zero(t, requests.Load(), "no request may be sent for an unsafe identifier")
}

// An identifier the server returns from a create is checked before anything
// uses it for a follow-up request or a cleanup.
func TestClient_CreateRefusesUnusableReturnedID(t *testing.T) {
	for _, id := range []string{"../" + validUUID, validUUID + "/x", "", "a?b"} {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + id + `","name":"n","username":"u","friendlyName":"f","endpoint":"https://scim.example.com"}`))
		}))
		c, err := client.NewClient(server.URL, "test-token", false, 30)
		require.NoError(t, err)
		ctx := context.Background()

		_, err = c.CreateUser(ctx, &client.UserCreateRequest{Username: "u"})
		assert.ErrorIs(t, err, client.ErrInvalidIdentifier, "user %q", id)
		_, err = c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: "g"})
		assert.ErrorIs(t, err, client.ErrInvalidIdentifier, "group %q", id)
		_, err = c.CreateScimServiceProvider(ctx, &client.ScimServiceProviderCreateRequest{})
		assert.ErrorIs(t, err, client.ErrInvalidIdentifier, "SCIM %q", id)
		_, err = c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "n"})
		assert.Error(t, err, "client %q", id)
		assert.Equal(t, int32(4), requests.Load(), "each create is sent once and nothing follows")
		server.Close()
	}
}
