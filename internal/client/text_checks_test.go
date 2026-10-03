package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// textKey is the API key these tests use; textKeyEscaped is the same text as
// a JSON string body with every character escaped, so only a check of the
// decoded value finds it.
const textKey = "Synthetic-Static-Key-0123456789"

func textKeyEscaped() string {
	var b strings.Builder
	for _, r := range textKey {
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}

// textServer answers every request with body (the version request with
// 2.17.0) and counts the requests other than the version request.
func textServer(t *testing.T, body string) (*client.Client, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/version/current" {
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
			return
		}
		requests.Add(1)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, textKey, false, 5)
	require.NoError(t, err)
	return c, &requests
}

// An answer that carries the API key in any text that can reach state, a
// log line or a diagnostic is not used: a read fails with a fixed error, and
// a mutation's error also says the change was made. The key is found in its
// plain and its escaped JSON form, and as an object key.
func TestClient_ResponsesCarryingTheKeyAreRefused(t *testing.T) {
	ctx := context.Background()
	const user = "aaaaaaaa-0000-4000-8000-0000000000a1"
	const group = "bbbbbbbb-0000-4000-8000-0000000000b1"
	for _, form := range []struct{ name, key string }{{"plain", textKey}, {"escaped", textKeyEscaped()}} {
		cases := map[string]struct {
			body     string
			call     func(c *client.Client) error
			mutation bool
		}{
			"a username": {`{"id":"` + user + `","username":"` + form.key + `","userGroups":[]}`, func(c *client.Client) error {
				_, err := c.GetUser(ctx, user)
				return err
			}, false},
			"a group's name in a user list": {`{"data":[{"id":"` + user + `","username":"u","userGroups":[{"id":"` + group + `","name":"x` + form.key + `"}]}],"pagination":{"totalPages":1,"totalItems":1,"currentPage":1,"itemsPerPage":100}}`, func(c *client.Client) error {
				_, err := c.ListAllUsers(ctx, "")
				return err
			}, false},
			"a custom claim's key": {`[{"key":"` + form.key + `","value":"v"}]`, func(c *client.Client) error {
				_, err := c.UpdateUserCustomClaims(ctx, user, nil)
				return err
			}, true},
			"a custom claim's value": {`[{"key":"k","value":"` + form.key + `"}]`, func(c *client.Client) error {
				_, err := c.UpdateUserCustomClaims(ctx, user, nil)
				return err
			}, true},
			"an object key": {`{"id":"` + user + `","username":"u","userGroups":[],"` + form.key + `":1}`, func(c *client.Client) error {
				_, err := c.GetUser(ctx, user)
				return err
			}, false},
			"an API key's name": {`{"data":[{"id":"` + user + `","name":"` + form.key + `"}],"pagination":{"totalPages":1,"totalItems":1,"currentPage":1,"itemsPerPage":100}}`, func(c *client.Client) error {
				_, err := c.ListAPIKeys(ctx)
				return err
			}, false},
			"a signup token's expiry": {`{"data":[{"id":"` + user + `","token":"t","expiresAt":"` + form.key + `"}],"pagination":{"totalPages":1,"totalItems":1,"currentPage":1,"itemsPerPage":100}}`, func(c *client.Client) error {
				_, err := c.ListSignupTokens(ctx)
				return err
			}, false},
			"a SCIM endpoint": {`{"id":"` + user + `","endpoint":"https://scim.example.com/` + form.key + `","token":"t"}`, func(c *client.Client) error {
				_, err := c.GetClientScimServiceProvider(ctx, "app")
				return err
			}, false},
			"an application setting": {`[{"key":"appName","value":"` + form.key + `"}]`, func(c *client.Client) error {
				_, err := c.UpdateApplicationConfig(ctx, &client.ApplicationConfig{})
				return err
			}, true},
		}
		for name, tc := range cases {
			t.Run(form.name+": "+name, func(t *testing.T) {
				c, _ := textServer(t, tc.body)
				err := tc.call(c)
				require.ErrorIs(t, err, client.ErrInvalidIdentifier)
				assert.Contains(t, err.Error(), "contains the API key")
				assert.NotContains(t, err.Error(), textKey)
				if tc.mutation {
					assert.ErrorIs(t, err, client.ErrResultUnread, "the change was made")
				} else {
					assert.NotErrorIs(t, err, client.ErrResultUnread)
				}
			})
		}
	}

	// A number that is the key is found too: a static key can be all digits.
	numeric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"app","allowedUserGroups":[],"accessTokenDurationMinutes":4815162342108151}`)
	}))
	defer numeric.Close()
	c, err := client.NewClient(numeric.URL, "4815162342108151", false, 5)
	require.NoError(t, err)
	_, err = c.GetClient(ctx, "app")
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
}

// Secret values (a SCIM, signup or one-time token, a client secret, the SMTP
// and LDAP passwords) go only to sensitive state and are never shown, so they
// are not refused for containing the key.
func TestClient_SecretValuesAreNotChecked(t *testing.T) {
	ctx := context.Background()
	c, _ := textServer(t, `{"id":"cccccccc-0000-4000-8000-0000000000c1","endpoint":"https://scim.example.com","token":"`+textKey+`"}`)
	provider, err := c.GetClientScimServiceProvider(ctx, "app")
	require.NoError(t, err)
	assert.Equal(t, textKey, provider.Token)

	c, _ = textServer(t, `[{"key":"smtpPassword","value":"`+textKey+`"},{"key":"appName","value":"Fixture"}]`)
	cfg, err := c.GetApplicationConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, textKey, cfg.SmtpPassword)
}

// A request whose body carries the key outside a secret value is not sent;
// one that carries it only as a secret value is.
func TestClient_RequestsCarryingTheKeyAreNotSent(t *testing.T) {
	ctx := context.Background()
	const user = "aaaaaaaa-0000-4000-8000-0000000000a1"
	for name, call := range map[string]func(c *client.Client) error{
		"a username": func(c *client.Client) error {
			_, err := c.UpdateUser(ctx, user, &client.UserCreateRequest{Username: "u-" + textKey})
			return err
		},
		"a custom claim": func(c *client.Client) error {
			_, err := c.UpdateUserCustomClaims(ctx, user, []client.CustomClaim{{Key: "team", Value: textKey}})
			return err
		},
		"a client's callback URL": func(c *client.Client) error {
			_, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "n", CallbackURLs: []string{"https://example.com/" + textKey}})
			return err
		},
		"an application setting": func(c *client.Client) error {
			_, err := c.UpdateApplicationConfig(ctx, &client.ApplicationConfig{AppName: textKey})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, requests := textServer(t, `{}`)
			err := call(c)
			require.ErrorIs(t, err, client.ErrInvalidIdentifier)
			assert.NotErrorIs(t, err, client.ErrResultUnread)
			assert.NotContains(t, err.Error(), textKey)
			assert.Zero(t, requests.Load(), "nothing is sent")
		})
	}

	// Secret values are sent.
	var sent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent = string(body)
		_, _ = fmt.Fprint(w, `{"id":"cccccccc-0000-4000-8000-0000000000c1","endpoint":"https://scim.example.com","oidcClient":{"id":""}}`)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, textKey, false, 5)
	require.NoError(t, err)
	_, err = c.UpdateScimServiceProvider(ctx, "cccccccc-0000-4000-8000-0000000000c1", &client.ScimServiceProviderCreateRequest{Endpoint: "https://scim.example.com", Token: textKey, OidcClientID: "app"})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent), &decoded))
	assert.Equal(t, textKey, decoded["token"])
}
