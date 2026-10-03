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
		"GenerateClientSecret":          func(id string) error { _, err := c.GenerateClientSecret(ctx, id, nil); return err },
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

// A create response's ID is used only if it is what the server could
// legitimately have returned: a UUID when the server chose it, exactly the
// requested ID when the caller chose it. A key-shaped value (Pocket ID API
// keys are 32 alphanumerics, which would pass the client-ID rule) is refused
// without being echoed.
func TestClient_CreateChecksReturnedID(t *testing.T) {
	const reflected = "Zq3vR8kLm2Np7Xw4Ys9Tb6Hc1Jd5Fg0A"
	respond := func(id string) (*client.Client, func()) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + id + `","name":"n","username":"u","friendlyName":"f","endpoint":"https://scim.example.com"}`))
		}))
		c, err := client.NewClient(server.URL, "test-token", false, 30)
		require.NoError(t, err)
		return c, server.Close
	}
	ctx := context.Background()
	creates := map[string]func(c *client.Client) error{
		"client": func(c *client.Client) error {
			_, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "n"})
			return err
		},
		"user": func(c *client.Client) error {
			_, err := c.CreateUser(ctx, &client.UserCreateRequest{Username: "u"})
			return err
		},
		"group": func(c *client.Client) error {
			_, err := c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: "g"})
			return err
		},
		"SCIM": func(c *client.Client) error {
			_, err := c.CreateScimServiceProvider(ctx, &client.ScimServiceProviderCreateRequest{})
			return err
		},
	}

	t.Run("server-chosen ID", func(t *testing.T) {
		for name, create := range creates {
			c, done := respond(reflected)
			err := create(c)
			done()
			require.ErrorIs(t, err, client.ErrInvalidIdentifier, name)
			assert.NotContains(t, err.Error(), reflected, name)

			c, done = respond(validUUID)
			assert.NoError(t, create(c), name)
			done()
		}
	})
	t.Run("caller-chosen client ID", func(t *testing.T) {
		requested := "my-app"
		for returned, ok := range map[string]bool{"my-app": true, "other-app": false, reflected: false, validUUID: false, "MY-APP": false} {
			c, done := respond(returned)
			created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "n", ClientID: &requested})
			done()
			if ok {
				require.NoError(t, err)
				assert.Equal(t, requested, created.ID)
				continue
			}
			require.ErrorIs(t, err, client.ErrInvalidIdentifier, returned)
			assert.NotContains(t, err.Error(), returned)
		}
	})
}

// Pocket ID accepts any static API key of 16 or more characters, so a key
// can be shaped like a UUID and pass every format check. A server that
// returns the key it received as a created object's ID (client, user,
// group, SCIM provider, generated secret, or the secret 2.17 creates with a
// client) gets nothing: the ID is refused or dropped, never echoed.
//
// The key is checked in the form the server received it. Go sends a header
// value without surrounding spaces and tabs, so a key configured with them
// arrives bare; the server reflecting that bare key is refused just the same.
func TestClient_CreateRefusesTheReflectedKey(t *testing.T) {
	const key = "7d3f9a12-4c8e-4b6a-9f21-0e5d8c7b6a43" // a UUID-shaped static key
	require.NoError(t, client.ValidateUUID("synthetic key", key), "the key passes the UUID check on its own")
	configured := map[string]string{
		"bare":            key,
		"spaces":          "  " + key + " ",
		"tabs":            "\t" + key + "\t\t",
		"spaces and tabs": " \t " + key + "\t \t",
		"leading only":    "\t " + key,
		"trailing only":   key + " \t",
	}
	for name, token := range configured {
		t.Run(name, func(t *testing.T) {
			var wrongKey atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received := r.Header.Get("X-API-KEY")
				if received != key {
					wrongKey.Store(true)
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet { // the version check before a secret
					_, _ = w.Write([]byte(`{"currentVersion":"2.17.0"}`))
					return
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"` + received + `","name":"n","secret":"generated-secret-value-0123456789",` +
					`"createdSecret":{"id":"` + received + `"}}`))
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, token, false, 30)
			require.NoError(t, err)
			ctx := context.Background()

			refused := map[string]error{}
			_, refused["client"] = c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "n"})
			_, refused["user"] = c.CreateUser(ctx, &client.UserCreateRequest{Username: "u"})
			_, refused["group"] = c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: "g"})
			_, refused["SCIM"] = c.CreateScimServiceProvider(ctx, &client.ScimServiceProviderCreateRequest{})
			_, refused["secret"] = c.GenerateClientSecret(ctx, "c1", nil)
			assert.False(t, wrongKey.Load(), "the server receives the key without surrounding whitespace")
			for kind, err := range refused {
				require.ErrorIs(t, err, client.ErrInvalidIdentifier, kind)
				assert.NotContains(t, err.Error(), key, kind)
				assert.Contains(t, err.Error(), "contains the API key", kind)
			}

			// An answer that carries the key anywhere, here only as the
			// ID of the secret Pocket ID creates with a client, is not used
			// at all: the create's result is unread.
			requested := "my-app"
			keyed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"my-app","name":"n","createdSecret":{"id":"` + r.Header.Get("X-API-KEY") + `"}}`))
			}))
			defer keyed.Close()
			c, err = client.NewClient(keyed.URL, token, false, 30)
			require.NoError(t, err)
			created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "n", ClientID: &requested})
			require.ErrorIs(t, err, client.ErrResultUnread)
			require.ErrorIs(t, err, client.ErrInvalidIdentifier)
			assert.Nil(t, created)
			assert.NotContains(t, err.Error(), key)
		})
	}
}

// A key that is nothing but the whitespace Go would strip is no key at all.
// The error is fixed text.
func TestNewClient_RefusesAWhitespaceOnlyKey(t *testing.T) {
	for _, token := range []string{" ", "\t", " \t \t "} {
		c, err := client.NewClient("https://pocket-id.example.com", token, false, 30)
		require.Error(t, err)
		assert.Nil(t, c)
		assert.Equal(t, "API token is required: the configured value is only spaces and tabs", err.Error())
	}
}
