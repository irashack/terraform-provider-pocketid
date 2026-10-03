package client

import (
	"strings"
	"testing"

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
// response carrying another object's ID, or the same ID in another case, is
// refused, so it can never replace the ID in state.
func TestCheckReturnedID_WrongIDOnARead(t *testing.T) {
	c := newCheckingClient(t, "test-token")
	require.NoError(t, c.checkReturnedID("user", userA, userA))

	for _, returned := range []string{userB, strings.ToUpper(userA), "", userA + " ", "other"} {
		err := c.checkReturnedID("user", userA, returned)
		assertRefused(t, err, returned, "the user ID in the response is not the one requested")
	}
	err := c.checkReturnedID(kindOIDCClient, "my-app", "MY-APP")
	assertRefused(t, err, "MY-APP", "the OIDC client ID in the response is not the one requested")
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

// The form an ID the call did not name must take: a UUID for every kind but
// OIDC clients, whose IDs follow ValidateClientID or are a CIMD client's URL
// (the rules of ParseCIMDURL in Pocket ID's OIDC library).
func TestCheckReturnedID_Forms(t *testing.T) {
	c := newCheckingClient(t, "test-token")
	clientIDs := map[string]bool{
		userA:                    true,
		"my-app":                 true,
		"a.b_c-d":                true,
		"ab":                     true,
		"a":                      false,
		"..":                     false,
		"":                       false,
		"a/b":                    false,
		"a b":                    false,
		strings.Repeat("a", 129): false,
		"https://client.example.com/oauth/metadata.json":                           true,
		"https://client.example.com:8443/m":                                        true,
		"HTTPS://client.example.com/m":                                             true,
		"https://client.example.com":                                               false, // no path
		"http://client.example.com/m":                                              false,
		"https://user@client.example.com/m":                                        false,
		"https://client.example.com/m?x=1":                                         false,
		"https://client.example.com/m?":                                            false,
		"https://client.example.com/m#f":                                           false,
		"https://client.example.com/a/../m":                                        false,
		"https://client.example.com/./m":                                           false,
		"https://client.example.com/a b":                                           false,
		"https://client.example.com/é":                                             false,
		"https:///m":                                                               false,
		"https://client.example.com/" + strings.Repeat("a", maxCIMDClientIDLength): false,
	}
	for id, ok := range clientIDs {
		err := c.checkReturnedID(kindOIDCClient, "", id)
		if ok {
			assert.NoError(t, err, "%q", id)
			continue
		}
		assertRefused(t, err, id, "is not a valid OIDC client ID")
	}

	for _, kind := range []string{"user", "user group", "client secret", "SCIM service provider", "API", "API key", "signup token", "passkey"} {
		assert.NoError(t, c.checkReturnedID(kind, "", userA), kind)
		for _, id := range []string{"my-app", "https://client.example.com/m", "", strings.ToUpper(userA) + "x"} {
			assertRefused(t, c.checkReturnedID(kind, "", id), id, "is not a valid "+kind+" ID")
		}
	}
}
