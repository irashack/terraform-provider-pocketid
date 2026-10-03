package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// syntheticKey is a UUID-shaped static API key: it passes every ID form
	// check on its own.
	syntheticKey = "7d3f9a12-4c8e-4b6a-9f21-0e5d8c7b6a43"
	userA        = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
	userB        = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
)

func newCheckingClient(t *testing.T, token string) *Client {
	t.Helper()
	c, err := NewClient("https://pocket-id.example.com", token, false, 30)
	require.NoError(t, err)
	return c
}

// assertRefused checks that err is a refusal that names the kind and says
// why, and quotes nothing of the value refused.
func assertRefused(t *testing.T, err error, returned, why string) {
	t.Helper()
	require.ErrorIs(t, err, ErrInvalidIdentifier)
	assert.Contains(t, err.Error(), why)
	if len(returned) >= 8 { // shorter values can occur in the fixed text by chance
		assert.NotContains(t, err.Error(), returned)
	}
	assert.NotContains(t, err.Error(), syntheticKey)
}

// A read addressed by ID (GET /users/{id}) must answer with that object: a
// response carrying another object's ID is refused, so it can never replace
// the ID in state. What counts as the same ID depends on the kind. A UUID is
// compared as a UUID: on PostgreSQL, where users, groups and SCIM providers
// live in native UUID columns, a request for an upper-case UUID finds the
// object and the answer spells its ID in lower case. An OIDC client ID is
// text, so a case variant names another client (or none).
func TestCheckReturnedID_WrongIDOnARead(t *testing.T) {
	c := newCheckingClient(t, "test-token")
	upperA := strings.ToUpper(userA)
	for _, tc := range []struct{ addressed, returned string }{
		{userA, userA},
		{upperA, userA}, // PostgreSQL: upper-case request, canonical answer
		{userA, upperA},
		{upperA, upperA},
	} {
		assert.NoError(t, c.checkReturnedID("user", tc.addressed, tc.returned), "%q answered with %q", tc.addressed, tc.returned)
		assert.NoError(t, c.checkReturnedID("SCIM service provider", tc.addressed, tc.returned))
	}

	for _, returned := range []string{userB, strings.ToUpper(userB), "", userA + " ", " " + userA, "{" + userA + "}", "other"} {
		err := c.checkReturnedID("user", upperA, returned)
		assertRefused(t, err, returned, "the user ID in the response is not the one requested")
	}
	// Not a UUID on the request side either: nothing to compare as a UUID.
	assertRefused(t, c.checkReturnedID("user group", "not-a-uuid", "NOT-A-UUID"), "NOT-A-UUID", "is not the one requested")

	// OIDC client IDs stay byte for byte, even when they look like UUIDs.
	for _, tc := range []struct{ addressed, returned string }{
		{"my-app", "MY-APP"},
		{upperA, userA},
		{userA, upperA},
	} {
		err := c.checkReturnedID(kindOIDCClient, tc.addressed, tc.returned)
		assertRefused(t, err, tc.returned, "the OIDC client ID in the response is not the one requested")
	}
	require.NoError(t, c.checkReturnedID(kindOIDCClient, upperA, upperA))
}

// The key check comes before the comparison, whatever the case: an
// addressed ID that is the key in upper case does not let its lower-case
// echo through.
func TestCheckReturnedID_KeyBeforeCaselessComparison(t *testing.T) {
	c := newCheckingClient(t, syntheticKey)
	err := c.checkReturnedID("user", strings.ToUpper(syntheticKey), syntheticKey)
	assertRefused(t, err, syntheticKey, "contains the API key this provider sent")
}

// A nested object must belong to the parent the call named: a SCIM provider
// read through its client must report that client, and a nested object the
// call did not name (a user's groups) must at least have its kind's form.
func TestCheckReturnedID_NestedParentMismatch(t *testing.T) {
	c := newCheckingClient(t, "test-token")

	// GET /oidc/clients/my-app/scim-service-provider answering for another client.
	require.NoError(t, c.checkReturnedID(kindOIDCClient, "my-app", "my-app"))
	for _, returned := range []string{"other-app", userA, "", "my-app/"} {
		err := c.checkReturnedID(kindOIDCClient, "my-app", returned)
		assertRefused(t, err, returned, "the OIDC client ID in the response is not the one requested")
	}

	// The provider's own ID, not addressed by that call, and a user's groups:
	// UUIDs, nothing else.
	require.NoError(t, c.checkReturnedID("SCIM service provider", "", userA))
	for _, returned := range []string{"my-app", "", "a/b", "..", userA + "/x", "https://scim.example.com/x"} {
		err := c.checkReturnedID("user group", "", returned)
		assertRefused(t, err, returned, "the user group ID in the response is not a valid user group ID")
	}
}

// An update addressed by an ID that is the key (state that took the key in
// before this check existed) must not have the key confirmed by a response
// that echoes it: equality with the addressed ID is not enough. Containment
// is a substring test, so a valid-looking ID that embeds the key is refused
// too. The key is compared as the server received it, whatever whitespace
// surrounded it in the configuration.
func TestCheckReturnedID_KeyOnAnUpdate(t *testing.T) {
	for _, token := range []string{syntheticKey, " \t" + syntheticKey + "\t "} {
		c := newCheckingClient(t, token)
		require.NoError(t, c.checkReturnedID("SCIM service provider", userA, userA))

		cases := []struct{ kind, addressed, returned string }{
			{"SCIM service provider", syntheticKey, syntheticKey}, // PUT /scim/service-provider/{key}
			{"user", userA, syntheticKey},
			{"user group", "", syntheticKey},
			{kindOIDCClient, "", "app-" + syntheticKey}, // a valid client ID that embeds the key
			{kindOIDCClient, "", "https://client.example.com/" + syntheticKey},
		}
		for _, tc := range cases {
			err := c.checkReturnedID(tc.kind, tc.addressed, tc.returned)
			assertRefused(t, err, tc.returned, "the "+tc.kind+" ID in the response contains the API key this provider sent")
		}
	}
}

// The form an ID the call did not name must take is the server's: a UUID for
// every kind but OIDC clients, whose IDs follow Pocket ID's own client-ID
// pattern (validateClientIDRegex, so "." and ".." and any length) or the
// CIMD rules of its OIDC library (ParseCIMDURL: spaces and Unicode in the
// path, no length limit). Those are wider than what this client puts in a
// request path, which still refuses ".." and every CIMD URL.
func TestCheckReturnedID_Forms(t *testing.T) {
	c := newCheckingClient(t, "test-token")
	clientIDs := map[string]bool{
		userA:                    true,
		"my-app":                 true,
		"a.b_c-d":                true,
		"ab":                     true,
		"a":                      true,
		".":                      true,
		"..":                     true,
		"..my..app..":            true,
		strings.Repeat("a", 129): true,
		"":                       false,
		"a/b":                    false,
		"a b":                    false,
		"app\u00e9":              false,
		"https://client.example.com/oauth/metadata.json":                 true,
		"https://client.example.com:8443/m":                              true,
		"HTTPS://client.example.com/m":                                   true,
		"https://client.example.com/a b/client metadata.json":            true,
		"https://client.example.com/\u00e9t\u00e9/m\u00e9tadonn\u00e9es": true,
		"https://client.example.com/a/.../m":                             true,
		"https://client.example.com/" + strings.Repeat("a", 4096):        true,
		"https://client.example.com":                                     false, // no path
		"http://client.example.com/m":                                    false,
		"https://user@client.example.com/m":                              false,
		"https://client.example.com/m?x=1":                               false,
		"https://client.example.com/m?":                                  false,
		"https://client.example.com/m#f":                                 false,
		"https://client.example.com/m#":                                  false,
		"https://client.example.com/a/../m":                              false,
		"https://client.example.com/./m":                                 false,
		"https://client.example.com/a/%2E%2E/m":                          false, // decoded, a ".." segment
		"https://client.example.com/a\tb":                                false, // a control character
		"https://client.example.com/a\nb":                                false,
		"https://cli ent.example.com/m":                                  false,
		"https:///m":                                                     false,
	}
	for id, ok := range clientIDs {
		err := c.checkReturnedID(kindOIDCClient, "", id)
		if ok {
			assert.NoError(t, err, "%q", id)
			continue
		}
		assertRefused(t, err, id, "is not a valid OIDC client ID")
	}

	// What the server may hold is not what this client sends: a request path
	// still refuses a relative segment and every CIMD URL.
	for _, id := range []string{"..", ".", "a", "https://client.example.com/a b/m"} {
		require.NoError(t, c.checkReturnedID(kindOIDCClient, "", id))
		assert.ErrorIs(t, c.ValidateIdentifier(kindOIDCClient, id), ErrInvalidIdentifier, "%q", id)
		_, err := clientIDSegment(id)
		assert.ErrorIs(t, err, ErrInvalidIdentifier, "%q", id)
	}

	for _, kind := range []string{"user", "user group", "client secret", "SCIM service provider", "API", "API key", "signup token", "passkey"} {
		assert.NoError(t, c.checkReturnedID(kind, "", userA), kind)
		for _, id := range []string{"my-app", "..", "https://client.example.com/m", "", strings.ToUpper(userA) + "x"} {
			assertRefused(t, c.checkReturnedID(kind, "", id), id, "is not a valid "+kind+" ID")
		}
	}
}

// keyRecorder is a server that records every request target it receives and
// answers each with an empty JSON object.
func keyRecorder(t *testing.T) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var targets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targets = append(targets, r.RequestURI)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), targets...)
	}
}

// An identifier from configuration, state or import that contains the API
// key (a UUID-shaped key, a client-ID-shaped key, one configured with
// surrounding whitespace, one that needs escaping in a path or query) is
// refused before a request is built: the server never sees a request that
// carries it, and neither the error nor the provider's log contains it.
func TestRequestsCarryingTheKeyAreNeverSent(t *testing.T) {
	const (
		clientShapedKey = "Zq3vR8kLm2Np7Xw4Ys9Tb6Hc1Jd5Fg0A"
		escapedKey      = "key+with/slash=and%sign-0123456"
	)
	ctx := context.Background()
	cases := []struct {
		name, token, key string
		call             func(c *Client, ctx context.Context) error
	}{
		{"UUID-shaped key as a user ID", syntheticKey, syntheticKey, func(c *Client, ctx context.Context) error {
			_, err := c.GetUser(ctx, syntheticKey)
			return err
		}},
		{"UUID-shaped key, configured padded", " \t" + syntheticKey + "\t ", syntheticKey, func(c *Client, ctx context.Context) error {
			return c.DeleteUserGroup(ctx, syntheticKey)
		}},
		{"UUID-shaped key as a secret ID", syntheticKey, syntheticKey, func(c *Client, ctx context.Context) error {
			return c.DeleteClientSecret(ctx, "my-app", syntheticKey)
		}},
		{"UUID-shaped key as a one-time token's user", syntheticKey, syntheticKey, func(c *Client, ctx context.Context) error {
			_, err := c.CreateOneTimeAccessToken(ctx, syntheticKey, &OneTimeAccessTokenRequest{TTL: "1h"})
			return err
		}},
		{"client-ID-shaped key as a client ID", clientShapedKey, clientShapedKey, func(c *Client, ctx context.Context) error {
			_, err := c.GetClient(ctx, clientShapedKey)
			return err
		}},
		{"key inside a client ID", clientShapedKey, clientShapedKey, func(c *Client, ctx context.Context) error {
			_, err := c.UpdateClient(ctx, "app-"+clientShapedKey, &OIDCClientCreateRequest{Name: "n"})
			return err
		}},
		{"key as a search term", clientShapedKey, clientShapedKey, func(c *Client, ctx context.Context) error {
			_, err := c.ListAllUsers(ctx, clientShapedKey)
			return err
		}},
		{"escaped key in a query", escapedKey, escapedKey, func(c *Client, ctx context.Context) error {
			_, err := c.ListUsersPage(ctx, 1, 10, "x "+escapedKey)
			return err
		}},
		{"escaped key in a path", escapedKey, escapedKey, func(c *Client, ctx context.Context) error {
			_, err := c.doRequest(ctx, http.MethodDelete, "/api/x/"+url.PathEscape(escapedKey), nil)
			return err
		}},
		{"escaped key in an image read", escapedKey, escapedKey, func(c *Client, ctx context.Context) error {
			_, _, err := c.getBinaryUncached(ctx, "/api/x", url.Values{"q": {escapedKey}}, 0)
			return err
		}},
		{"key in an upload's query", clientShapedKey, clientShapedKey, func(c *Client, ctx context.Context) error {
			_, err := c.upload(ctx, http.MethodPost, "/api/x?name="+clientShapedKey, MultipartFile{FieldName: "file", FileName: "a.png", Content: []byte{1}}, 0)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serverURL, targets := keyRecorder(t)
			var logs bytes.Buffer
			c, err := NewClient(serverURL, tc.token, false, 30)
			require.NoError(t, err)

			err = tc.call(c, tflogtest.RootLogger(ctx, &logs))
			require.ErrorIs(t, err, ErrInvalidIdentifier)
			assert.Contains(t, err.Error(), "contains the API key this provider sends")
			assert.NotContains(t, err.Error(), tc.key)
			assert.NotContains(t, err.Error(), url.PathEscape(tc.key))
			assert.NotContains(t, logs.String(), tc.key)
			assert.NotContains(t, logs.String(), url.PathEscape(tc.key))
			assert.Empty(t, targets(), "no request was sent")
		})
	}

	// The same calls with an identifier that does not carry the key go out.
	serverURL, targets := keyRecorder(t)
	c, err := NewClient(serverURL, syntheticKey, false, 30)
	require.NoError(t, err)
	_ = c.DeleteUserGroup(ctx, userA)
	_, _ = c.ListAllUsers(ctx, "alice")
	assert.Len(t, targets(), 2)
}

// ValidateIdentifier, for identifiers from configuration, state or import:
// the kind's form, and never the key.
func TestValidateIdentifier(t *testing.T) {
	c := newCheckingClient(t, " "+syntheticKey+"\t")
	require.NoError(t, c.ValidateIdentifier("user", userA))
	require.NoError(t, c.ValidateIdentifier(kindOIDCClient, "my-app"))

	for _, tc := range []struct{ kind, id, why string }{
		{"user", syntheticKey, "the user ID contains the API key this provider sends"},
		{kindOIDCClient, syntheticKey, "the OIDC client ID contains the API key"},
		{kindOIDCClient, "app-" + syntheticKey, "the OIDC client ID contains the API key"},
		{"user", "my-app", "user ID must be a UUID"},
		{kindOIDCClient, "https://client.example.com/m", "OIDC client ID must be 2 to 128 characters"},
		{kindOIDCClient, "..", "OIDC client ID must be 2 to 128 characters"},
	} {
		assertRefused(t, c.ValidateIdentifier(tc.kind, tc.id), tc.id, tc.why)
	}
}

// An endpoint the check cannot decode is refused too, rather than sent
// unchecked.
func TestCheckEndpointRefusesWhatItCannotDecode(t *testing.T) {
	c := newCheckingClient(t, syntheticKey)
	require.NoError(t, c.checkEndpoint("/api/users/"+userA+"?pagination%5Bpage%5D=1&search=a+b"))
	for _, endpoint := range []string{"/api/x/%zz", "/api/x?q=%zz", "/api/x?a=1;b=2"} {
		err := c.checkEndpoint(endpoint)
		require.ErrorIs(t, err, ErrInvalidIdentifier, endpoint)
		assert.Contains(t, err.Error(), "not well formed")
	}
}
