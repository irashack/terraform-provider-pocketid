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
	"time"

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

const (
	textUser   = "aaaaaaaa-0000-4000-8000-0000000000a1"
	textGroup  = "bbbbbbbb-0000-4000-8000-0000000000b1"
	textSCIM   = "cccccccc-0000-4000-8000-0000000000c1"
	textSecret = "dddddddd-0000-4000-8000-0000000000d1"
)

// textServerWithKey answers every request with body (the version request with
// 2.17.0) and counts the requests other than the version request.
func textServerWithKey(t *testing.T, key, body string) (*client.Client, *atomic.Int32) {
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
	c, err := client.NewClient(server.URL, key, false, 5)
	require.NoError(t, err)
	return c, &requests
}

func textServer(t *testing.T, body string) (*client.Client, *atomic.Int32) {
	t.Helper()
	return textServerWithKey(t, textKey, body)
}

func textPage(items ...string) string {
	return `{"data":[` + strings.Join(items, ",") + `],"pagination":{"totalPages":1,"totalItems":` + fmt.Sprint(len(items)) + `,"currentPage":1,"itemsPerPage":100}}`
}

func textUserJSON(fields string) string {
	return `{"id":"` + textUser + `","username":"u","email":"u@example.com","userGroups":[],"customClaims":[]` + fields + `}`
}

func textClientJSON(fields string) string {
	return `{"id":"app","name":"app","callbackURLs":["https://app.example.com/cb"],"isPublic":false,"pkceEnabled":true,"isGroupRestricted":false,"allowedUserGroups":[],"accessTokenDurationMinutes":60,"refreshTokenDurationMinutes":43200` + fields + `}`
}

// An answer whose text carries the API key, in a field the provider takes
// into state, a log line or a diagnostic, is not used: a read fails with
// ErrKeyInResponse, and a mutation's error also says the change was made. The
// key is found in plain and escaped JSON, under a field name in another
// letter case (which Go's decoder maps to the same field), and in a custom
// claim named like a secret setting.
func TestClient_ResponsesCarryingTheKeyAreRefused(t *testing.T) {
	ctx := context.Background()
	for _, form := range []struct{ name, key string }{{"plain", textKey}, {"escaped", textKeyEscaped()}} {
		cases := map[string]struct {
			body     string
			call     func(c *client.Client) error
			mutation bool
		}{
			"a username": {textUserJSON(`,"username":"` + form.key + `"`), func(c *client.Client) error {
				_, err := c.GetUser(ctx, textUser)
				return err
			}, false},
			"a display name under a field name in upper case": {textUserJSON(`,"DISPLAYNAME":"x` + form.key + `"`), func(c *client.Client) error {
				_, err := c.GetUser(ctx, textUser)
				return err
			}, false},
			"a custom claim named smtpPassword in a user": {textUserJSON(`,"customClaims":[{"key":"smtpPassword","value":"` + form.key + `"}]`), func(c *client.Client) error {
				_, err := c.GetUser(ctx, textUser)
				return err
			}, false},
			"the signed-in user's e-mail address": {textUserJSON(`,"email":"` + form.key + `@example.com"`), func(c *client.Client) error {
				_, err := c.GetCurrentUser(ctx)
				return err
			}, false},
			"a group's name in a user list": {textPage(`{"id":"` + textUser + `","username":"u","userGroups":[{"id":"` + textGroup + `","name":"x` + form.key + `"}]}`), func(c *client.Client) error {
				_, err := c.ListAllUsers(ctx, "")
				return err
			}, false},
			"a group's friendly name": {`{"id":"` + textGroup + `","name":"g","friendlyName":"` + form.key + `","customClaims":[],"users":[],"allowedOidcClients":[]}`, func(c *client.Client) error {
				_, err := c.GetUserGroupDetail(ctx, textGroup)
				return err
			}, false},
			"a custom claim's key": {`[{"key":"` + form.key + `","value":"v"}]`, func(c *client.Client) error {
				_, err := c.UpdateUserCustomClaims(ctx, textUser, nil)
				return err
			}, true},
			"a custom claim's value": {`[{"key":"k","value":"` + form.key + `"}]`, func(c *client.Client) error {
				_, err := c.UpdateGroupCustomClaims(ctx, textGroup, nil)
				return err
			}, true},
			"a custom claim named smtpPassword": {`[{"key":"smtpPassword","value":"` + form.key + `"}]`, func(c *client.Client) error {
				_, err := c.UpdateUserCustomClaims(ctx, textUser, nil)
				return err
			}, true},
			"a custom claim named ldapBindPassword": {`[{"key":"ldapBindPassword","value":"` + form.key + `"}]`, func(c *client.Client) error {
				_, err := c.UpdateUserCustomClaims(ctx, textUser, nil)
				return err
			}, true},
			"a client's callback URL": {textClientJSON(`,"callbackURLs":["https://app.example.com/` + form.key + `"]`), func(c *client.Client) error {
				_, err := c.GetClient(ctx, "app")
				return err
			}, false},
			"a client's name, on update": {textClientJSON(`,"name":"` + form.key + `"`), func(c *client.Client) error {
				_, err := c.UpdateClient(ctx, "app", &client.OIDCClientCreateRequest{Name: "app"})
				return err
			}, true},
			"a federated identity's public key": {textClientJSON(`,"credentials":{"federatedIdentities":[{"issuer":"https://idp.example.com","publicKeys":[{"kty":"OKP","x":"` + form.key + `"}]}]}`), func(c *client.Client) error {
				_, err := c.GetClient(ctx, "app")
				return err
			}, false},
			"an API key's name": {textPage(`{"id":"` + textUser + `","name":"` + form.key + `"}`), func(c *client.Client) error {
				_, err := c.ListAPIKeys(ctx)
				return err
			}, false},
			"a signup token's expiry": {textPage(`{"id":"` + textUser + `","token":"t","expiresAt":"` + form.key + `"}`), func(c *client.Client) error {
				_, err := c.ListSignupTokens(ctx)
				return err
			}, false},
			"a passkey's name": {`[{"id":"` + textSecret + `","name":"` + form.key + `","createdAt":"2026-10-02T10:00:00Z"}]`, func(c *client.Client) error {
				_, err := c.ListUserPasskeys(ctx, textUser)
				return err
			}, false},
			"a SCIM endpoint": {`{"id":"` + textSCIM + `","endpoint":"https://scim.example.com/` + form.key + `","token":"t"}`, func(c *client.Client) error {
				_, err := c.GetClientScimServiceProvider(ctx, "app")
				return err
			}, false},
			"a SCIM endpoint, on update": {`{"id":"` + textSCIM + `","endpoint":"https://scim.example.com/` + form.key + `","oidcClient":{"id":""}}`, func(c *client.Client) error {
				_, err := c.UpdateScimServiceProvider(ctx, textSCIM, &client.ScimServiceProviderCreateRequest{Endpoint: "https://scim.example.com", OidcClientID: "app"})
				return err
			}, true},
			"an application setting": {`[{"key":"appName","value":"` + form.key + `"}]`, func(c *client.Client) error {
				_, err := c.GetApplicationConfig(ctx)
				return err
			}, false},
			"an application setting, on update": {`[{"key":"smtpUser","value":"` + form.key + `"}]`, func(c *client.Client) error {
				_, err := c.UpdateApplicationConfig(ctx, &client.ApplicationConfig{})
				return err
			}, true},
		}
		for name, tc := range cases {
			t.Run(form.name+": "+name, func(t *testing.T) {
				c, _ := textServer(t, tc.body)
				err := tc.call(c)
				require.ErrorIs(t, err, client.ErrKeyInResponse)
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
}

// A JSON field name is never data: an answer whose field names contain the
// key (a key equal to a field Pocket ID always sends, or a field the provider
// does not decode) is used as usual when its values do not.
func TestClient_FieldNamesAreNotChecked(t *testing.T) {
	ctx := context.Background()
	// A static key may equal a field name Pocket ID sends in every client
	// answer (at least 16 characters, as Pocket ID requires).
	const fieldKey = "allowedUserGroups"
	c, _ := textServerWithKey(t, fieldKey, textClientJSON(""))
	got, err := c.GetClient(ctx, "app")
	require.NoError(t, err)
	assert.Equal(t, "app", got.Name)
	_, err = c.UpdateClient(ctx, "app", &client.OIDCClientCreateRequest{Name: "app", CallbackURLs: []string{"https://app.example.com/cb"}})
	require.NoError(t, err)
	_, err = c.ListClients(ctx)
	require.Error(t, err, "the body is not a page; only the decoder refuses it")
	assert.NotErrorIs(t, err, client.ErrKeyInResponse)

	created := `{"id":"` + "cccccccc-cccc-4ccc-8ccc-cccccccccccc" + `","name":"app","callbackURLs":[],"isPublic":false,"pkceEnabled":true,"isGroupRestricted":false,"allowedUserGroups":[]}`
	c, _ = textServerWithKey(t, fieldKey, created)
	client1, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "app"})
	require.NoError(t, err, "a generated-ID create keeps its answer")
	assert.Equal(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", client1.ID)

	// A field the provider does not decode is not looked at, whatever its
	// name or value.
	c, _ = textServer(t, textUserJSON(`,"`+textKey+`":"`+textKey+`"`))
	user, err := c.GetUser(ctx, textUser)
	require.NoError(t, err)
	assert.Equal(t, "u", user.Username)
}

// A static key can be all digits. Numbers inside Pocket ID's ranges never
// hold 16 digits, so answers with them are used; the key inside a text field
// is refused like any other.
func TestClient_NumericKey(t *testing.T) {
	ctx := context.Background()
	const numericKey = "4815162342108151"
	c, _ := textServerWithKey(t, numericKey, textClientJSON(""))
	got, err := c.GetClient(ctx, "app")
	require.NoError(t, err)
	assert.Equal(t, int64(60), got.AccessTokenDurationMinutes)

	c, _ = textServerWithKey(t, numericKey, textPage(`{"id":"`+textUser+`","token":"t","expiresAt":"2030-01-02T03:04:05Z","usageLimit":100,"usageCount":3,"userGroups":[],"createdAt":"2026-10-02T10:00:00Z"}`))
	tokens, err := c.ListSignupTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, 100, tokens[0].UsageLimit)

	c, _ = textServerWithKey(t, numericKey, textUserJSON(`,"username":"user`+numericKey+`"`))
	_, err = c.GetUser(ctx, textUser)
	require.ErrorIs(t, err, client.ErrKeyInResponse)
	assert.NotContains(t, err.Error(), numericKey)
}

// Secret values (a SCIM, signup or one-time token, a client secret, the SMTP
// and LDAP passwords of the application configuration) go only to sensitive
// state and are never shown, so they are not refused for containing the key.
func TestClient_SecretValuesAreNotChecked(t *testing.T) {
	ctx := context.Background()
	c, _ := textServer(t, `{"id":"`+textSCIM+`","endpoint":"https://scim.example.com","token":"`+textKey+`"}`)
	provider, err := c.GetClientScimServiceProvider(ctx, "app")
	require.NoError(t, err)
	assert.Equal(t, textKey, provider.Token)

	c, _ = textServer(t, `[{"key":"smtpPassword","value":"`+textKey+`"},{"key":"ldapBindPassword","value":"`+textKey+`"},{"key":"appName","value":"Fixture"}]`)
	cfg, err := c.GetApplicationConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, textKey, cfg.SmtpPassword)
	assert.Equal(t, textKey, cfg.LdapBindPassword)

	c, _ = textServer(t, textPage(`{"id":"`+textUser+`","token":"`+textKey+`","expiresAt":"2030-01-02T03:04:05Z","usageLimit":1,"userGroups":[]}`))
	tokens, err := c.ListSignupTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, textKey, tokens[0].Token)

	c, _ = textServer(t, `{"token":"`+textKey+`"}`)
	token, err := c.CreateOneTimeAccessToken(ctx, textUser, &client.OneTimeAccessTokenRequest{TTL: "1h"})
	require.NoError(t, err)
	assert.Equal(t, textKey, token.Token)

	c, _ = textServer(t, `{"id":"`+textSecret+`","prefix":"`+textKey[:4]+`","createdAt":"2026-10-02T10:00:00Z","isActive":true,"secret":"`+textKey+`"}`)
	secret, err := c.CreateClientSecret(ctx, "app", nil)
	require.NoError(t, err)
	assert.Equal(t, textKey, secret.Value)
}

// A create whose answer names the new object with a usable ID but carries the
// key elsewhere returns only that ID (a signup token also its value, which
// may be valid) with an error wrapping ErrResultUnread and ErrKeyInResponse,
// so the caller can recover the object that exists.
func TestClient_CreateKeepsTheIdentityOfAnUnusableAnswer(t *testing.T) {
	ctx := context.Background()
	check := func(t *testing.T, err error) {
		t.Helper()
		require.ErrorIs(t, err, client.ErrResultUnread)
		require.ErrorIs(t, err, client.ErrKeyInResponse)
		assert.NotContains(t, err.Error(), textKey)
	}
	t.Run("user", func(t *testing.T) {
		c, _ := textServer(t, textUserJSON(`,"displayName":"`+textKey+`","userGroups":[{"id":"`+textGroup+`","name":"g"}]`))
		user, err := c.CreateUser(ctx, &client.UserCreateRequest{Username: "u"})
		check(t, err)
		require.NotNil(t, user)
		assert.Equal(t, client.User{ID: textUser, GroupsUnknown: true}, *user)
	})
	t.Run("user group", func(t *testing.T) {
		c, _ := textServer(t, `{"id":"`+textGroup+`","name":"`+textKey+`","friendlyName":"g"}`)
		group, err := c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: "g", FriendlyName: "g"})
		check(t, err)
		require.NotNil(t, group)
		assert.Equal(t, client.UserGroup{ID: textGroup}, *group)
	})
	t.Run("client", func(t *testing.T) {
		c, _ := textServer(t, `{"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","name":"`+textKey+`","callbackURLs":[],"allowedUserGroups":[]}`)
		created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "app"})
		check(t, err)
		require.NotNil(t, created)
		assert.Equal(t, client.OIDCClient{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}, *created)
	})
	t.Run("signup token", func(t *testing.T) {
		c, _ := textServer(t, `{"id":"`+textUser+`","token":"live-token","expiresAt":"`+textKey+`","usageLimit":1,"userGroups":[]}`)
		token, err := c.CreateSignupToken(ctx, &client.SignupTokenCreateRequest{UsageLimit: 1})
		check(t, err)
		require.NotNil(t, token)
		assert.Equal(t, client.SignupToken{ID: textUser, Token: "live-token"}, *token)
	})
	t.Run("SCIM service provider: nothing", func(t *testing.T) {
		c, _ := textServer(t, `{"id":"`+textSCIM+`","endpoint":"https://scim.example.com/`+textKey+`","oidcClient":{"id":""}}`)
		provider, err := c.CreateScimServiceProvider(ctx, &client.ScimServiceProviderCreateRequest{Endpoint: "https://scim.example.com", OidcClientID: "app"})
		check(t, err)
		assert.Nil(t, provider)
	})
}

// A request whose body carries the key outside a secret value of its type is
// not sent, also in a custom claim named like a secret setting; one that
// carries it only as a secret value is.
func TestClient_RequestsCarryingTheKeyAreNotSent(t *testing.T) {
	ctx := context.Background()
	expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	for name, call := range map[string]func(c *client.Client) error{
		"a username": func(c *client.Client) error {
			_, err := c.UpdateUser(ctx, textUser, &client.UserCreateRequest{Username: "u-" + textKey})
			return err
		},
		"a display name on create": func(c *client.Client) error {
			_, err := c.CreateUser(ctx, &client.UserCreateRequest{Username: "u", DisplayName: textKey})
			return err
		},
		"a group's friendly name": func(c *client.Client) error {
			_, err := c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: "g", FriendlyName: textKey})
			return err
		},
		"a custom claim": func(c *client.Client) error {
			_, err := c.UpdateUserCustomClaims(ctx, textUser, []client.CustomClaim{{Key: "team", Value: textKey}})
			return err
		},
		"a custom claim named smtpPassword": func(c *client.Client) error {
			_, err := c.UpdateGroupCustomClaims(ctx, textGroup, []client.CustomClaim{{Key: "smtpPassword", Value: textKey}})
			return err
		},
		"a client's callback URL": func(c *client.Client) error {
			_, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: "n", CallbackURLs: []string{"https://example.com/" + textKey}})
			return err
		},
		"a federated identity's subject": func(c *client.Client) error {
			_, err := c.UpdateClient(ctx, "app", &client.OIDCClientCreateRequest{Name: "n", Credentials: client.OIDCClientCredentials{
				FederatedIdentities: []client.OIDCClientFederatedIdentity{{Issuer: "https://idp.example.com", Subject: textKey}},
			}})
			return err
		},
		"a SCIM endpoint": func(c *client.Client) error {
			_, err := c.CreateScimServiceProvider(ctx, &client.ScimServiceProviderCreateRequest{Endpoint: "https://scim.example.com/" + textKey, OidcClientID: "app"})
			return err
		},
		"a one-time token's ttl": func(c *client.Client) error {
			_, err := c.CreateOneTimeAccessToken(ctx, textUser, &client.OneTimeAccessTokenRequest{TTL: textKey})
			return err
		},
		"an application setting": func(c *client.Client) error {
			_, err := c.UpdateApplicationConfig(ctx, &client.ApplicationConfig{AppName: textKey})
			return err
		},
		"an API permission's description": func(c *client.Client) error {
			description := textKey
			_, err := c.UpdateAPIPermissions(ctx, textSCIM, []client.APIPermissionInput{{Key: "read", Name: "Read", Description: &description}})
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

	// Secret values are sent: a SCIM token, the SMTP and LDAP passwords, a
	// client secret's value.
	var sent []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/version/current" {
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		sent = append(sent, string(body))
		switch r.URL.Path {
		case "/api/scim/service-provider/" + textSCIM:
			_, _ = fmt.Fprint(w, `{"id":"`+textSCIM+`","endpoint":"https://scim.example.com","oidcClient":{"id":""}}`)
		case "/api/application-configuration":
			_, _ = fmt.Fprint(w, `[{"key":"appName","value":"Fixture"}]`)
		default:
			_, _ = fmt.Fprint(w, `{"id":"`+textSecret+`","prefix":"`+textKey[:4]+`","createdAt":"2026-10-02T10:00:00Z","isActive":true,"secret":"`+textKey+`"}`)
		}
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, textKey, false, 5)
	require.NoError(t, err)
	_, err = c.UpdateScimServiceProvider(ctx, textSCIM, &client.ScimServiceProviderCreateRequest{Endpoint: "https://scim.example.com", Token: textKey, OidcClientID: "app"})
	require.NoError(t, err)
	_, err = c.UpdateApplicationConfig(ctx, &client.ApplicationConfig{AppName: "Fixture", SmtpPassword: textKey, LdapBindPassword: textKey})
	require.NoError(t, err)
	_, err = c.CreateClientSecret(ctx, "app", &client.ClientSecretOptions{Value: textKey, ExpiresAt: &expires})
	require.NoError(t, err)
	require.Len(t, sent, 3)
	var scim map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent[0]), &scim))
	assert.Equal(t, textKey, scim["token"])
	var settings map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent[1]), &settings))
	assert.Equal(t, textKey, settings["smtpPassword"])
	assert.Equal(t, textKey, settings["ldapBindPassword"])
	var secret map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent[2]), &secret))
	assert.Equal(t, textKey, secret["secret"])
}

// A secret's metadata gets one check wherever it comes from (a client's
// credentials.secrets, the secrets list, a creation answer): a prefix that is
// not empty or four printable ASCII bytes is refused, and so is a prefix or a
// time, in the form the provider stores or prints it, that contains the key.
// A static key can look like a timestamp.
func TestClient_SecretMetadataIsCheckedEverywhere(t *testing.T) {
	ctx := context.Background()
	const timeKey = "2030-01-02T03:04:05Z"
	secret := func(prefix, createdAt, expiresAt string) string {
		expires := "null"
		if expiresAt != "" {
			expires = `"` + expiresAt + `"`
		}
		return `{"id":"` + textSecret + `","prefix":"` + prefix + `","createdAt":"` + createdAt + `","expiresAt":` + expires + `,"isActive":true}`
	}
	embedded := func(s string) string { return textClientJSON(`,"credentials":{"secrets":[` + s + `]}`) }
	for name, tc := range map[string]struct {
		key, body string
		call      func(c *client.Client) error
		want      []error
	}{
		"embedded prefix carrying the key": {textKey, embedded(secret(textKey, "2026-10-02T10:00:00Z", "")), func(c *client.Client) error {
			_, err := c.GetClient(ctx, "app")
			return err
		}, []error{client.ErrInvalidIdentifier}},
		"embedded creation time that is the key": {timeKey, embedded(secret("abcd", timeKey, "")), func(c *client.Client) error {
			_, err := c.GetClient(ctx, "app")
			return err
		}, []error{client.ErrKeyInResponse}},
		"embedded expiry that is the key, in a client list": {timeKey, textPage(embedded(secret("abcd", "2026-10-02T10:00:00Z", timeKey))), func(c *client.Client) error {
			_, err := c.ListClients(ctx)
			return err
		}, []error{client.ErrKeyInResponse}},
		"listed creation time that is the key": {timeKey, "[" + secret("abcd", timeKey, "") + "]", func(c *client.Client) error {
			_, err := c.ListClientSecrets(ctx, "app")
			return err
		}, []error{client.ErrMalformedSecretList, client.ErrKeyInResponse}},
		"listed expiry that is the key": {timeKey, "[" + secret("abcd", "2026-10-02T10:00:00Z", timeKey) + "]", func(c *client.Client) error {
			_, err := c.ListClientSecrets(ctx, "app")
			return err
		}, []error{client.ErrMalformedSecretList, client.ErrKeyInResponse}},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := textServerWithKey(t, tc.key, tc.body)
			err := tc.call(c)
			require.Error(t, err)
			for _, want := range tc.want {
				assert.ErrorIs(t, err, want)
			}
			assert.NotContains(t, err.Error(), tc.key)
		})
	}

	// A creation answer whose metadata carries the key keeps only the
	// secret's ID.
	c, _ := textServerWithKey(t, timeKey, `{"id":"`+textSecret+`","prefix":"abcd","createdAt":"`+timeKey+`","expiresAt":null,"isActive":true,"secret":"abcdefghijklmnopqrstuvwx"}`)
	created, err := c.CreateClientSecret(ctx, "app", nil)
	require.ErrorIs(t, err, client.ErrResultUnread)
	require.ErrorIs(t, err, client.ErrCreatedSecretMalformed)
	require.ErrorIs(t, err, client.ErrKeyInResponse)
	assert.NotContains(t, err.Error(), timeKey)
	require.NotNil(t, created)
	assert.Equal(t, client.ClientSecret{ClientSecretMetadata: client.ClientSecretMetadata{ID: textSecret}}, *created)

	// The same metadata without the key is used.
	c, _ = textServerWithKey(t, timeKey, embedded(secret("abcd", "2026-10-02T10:00:00Z", "2031-01-01T00:00:00Z")))
	got, err := c.GetClient(ctx, "app")
	require.NoError(t, err)
	require.Len(t, got.Credentials.Secrets, 1)
	assert.Equal(t, "abcd", got.Credentials.Secrets[0].Prefix)
}

// A public JWK that repeats a member (here "kid", once carrying the key in
// escaped form) is neither used from an answer nor sent: a parser that keeps
// the last value would see only the harmless one. The same holds without
// the key.
func TestClient_JWKsRepeatingAMemberAreRefused(t *testing.T) {
	ctx := context.Background()
	for name, first := range map[string]string{"carrying the escaped key": textKeyEscaped(), "harmless": "first"} {
		jwk := `{"kty":"EC","crv":"P-256","kid":"` + first + `","kid":"safe","x":"a","y":"b"}`
		t.Run("answer "+name, func(t *testing.T) {
			c, _ := textServer(t, textClientJSON(`,"credentials":{"federatedIdentities":[{"issuer":"https://idp.example.com","publicKeys":[`+jwk+`]}]}`))
			_, err := c.GetClient(ctx, "app")
			require.ErrorIs(t, err, client.ErrUndecodableResponse)
			assert.NotContains(t, err.Error(), textKey)
			_, err = c.UpdateClient(ctx, "app", &client.OIDCClientCreateRequest{Name: "app"})
			require.ErrorIs(t, err, client.ErrUndecodableResponse)
			assert.ErrorIs(t, err, client.ErrResultUnread)
		})
		t.Run("request "+name, func(t *testing.T) {
			c, requests := textServer(t, `{}`)
			_, err := c.UpdateClient(ctx, "app", &client.OIDCClientCreateRequest{Name: "app", Credentials: client.OIDCClientCredentials{
				FederatedIdentities: []client.OIDCClientFederatedIdentity{{Issuer: "https://idp.example.com", PublicKeys: []json.RawMessage{json.RawMessage(jwk)}}},
			}})
			require.ErrorIs(t, err, client.ErrInvalidIdentifier)
			assert.NotContains(t, err.Error(), textKey)
			assert.Zero(t, requests.Load(), "nothing is sent")
		})
	}
}

// A setting that holds a JSON document (the signup default claims, the CIMD
// allowlist) is checked inside too: a key written with JSON escapes in the
// document is refused in an answer and in a request.
func TestClient_JSONSettingsAreCheckedInside(t *testing.T) {
	ctx := context.Background()
	// The escapes of the key, escaped once more for the wire.
	inner := strings.ReplaceAll(textKeyEscaped(), `\`, `\\`)
	c, _ := textServer(t, `[{"key":"signupDefaultCustomClaims","value":"[{\"key\":\"team\",\"value\":\"`+inner+`\"}]"}]`)
	_, err := c.GetApplicationConfig(ctx)
	require.ErrorIs(t, err, client.ErrKeyInResponse)
	assert.NotContains(t, err.Error(), textKey)

	c, requests := textServer(t, `[]`)
	_, err = c.UpdateApplicationConfig(ctx, &client.ApplicationConfig{SignupDefaultCustomClaims: `[{"key":"team","value":"` + textKeyEscaped() + `"}]`})
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
	assert.Zero(t, requests.Load(), "nothing is sent")

	// An ordinary JSON setting passes.
	c, _ = textServer(t, `[{"key":"signupDefaultCustomClaims","value":"[{\"key\":\"team\",\"value\":\"it\"}]"}]`)
	cfg, err := c.GetApplicationConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, `[{"key":"team","value":"it"}]`, cfg.SignupDefaultCustomClaims)
}
