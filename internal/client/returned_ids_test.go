package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// The API key these tests authenticate with is itself UUID-shaped, as a
// static key may be: a response that reflects it as an object ID passes every
// form check, so only the key check refuses it.
const (
	returnedIDsKey   = "5a5a5a5a-1111-4111-8111-111111111111"
	returnedIDsUser  = "aaaaaaaa-0000-4000-8000-0000000000a1"
	returnedIDsOther = "aaaaaaaa-0000-4000-8000-0000000000a2"
	returnedIDsGroup = "bbbbbbbb-0000-4000-8000-0000000000b1"
	returnedIDsSCIM  = "cccccccc-0000-4000-8000-0000000000c1"
	returnedIDsAPI   = "dddddddd-0000-4000-8000-0000000000d1"
)

// returnedIDsServer answers every request with body (and the version
// request with 2.17.0) and records the paths it was asked for.
func returnedIDsServer(t *testing.T, body string) (*client.Client, *[]string) {
	t.Helper()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/version/current" {
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
			return
		}
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, returnedIDsKey, false, 5)
	require.NoError(t, err)
	return c, &paths
}

func userJSON(id string, groupIDs ...string) string {
	groups := make([]map[string]string, 0, len(groupIDs))
	for _, g := range groupIDs {
		groups = append(groups, map[string]string{"id": g, "name": "g", "friendlyName": "g"})
	}
	out, _ := json.Marshal(map[string]any{"id": id, "username": "u", "email": "u@example.com", "userGroups": groups})
	return string(out)
}

func pageJSON(items ...string) string {
	return `{"data":[` + strings.Join(items, ",") + `],"pagination":{"totalPages":1,"totalItems":` + fmt.Sprint(len(items)) + `,"currentPage":1,"itemsPerPage":100}}`
}

// One case per area file: each read refuses a response whose ID is another
// object's or carries the API key (top-level or nested), and each update
// does the same and reports the refusal as an unread result, because the
// change was made. No error repeats the key.
func TestClient_ReturnedIDsAreChecked(t *testing.T) {
	ctx := context.Background()
	type call func(c *client.Client) error
	cases := []struct {
		name     string
		body     string
		call     call
		mutation bool
		sentinel error // ErrInvalidIdentifier unless set
	}{
		// oidc_clients.go
		{"GetClient: another client", `{"id":"other","allowedUserGroups":[]}`, func(c *client.Client) error { _, err := c.GetClient(ctx, "app"); return err }, false, nil},
		{"GetClient: another spelling of a client ID", `{"id":"App","allowedUserGroups":[]}`, func(c *client.Client) error { _, err := c.GetClient(ctx, "app"); return err }, false, nil},
		{"GetClient: reflected group ID", `{"id":"app","allowedUserGroups":[{"id":"` + returnedIDsKey + `"}]}`, func(c *client.Client) error { _, err := c.GetClient(ctx, "app"); return err }, false, nil},
		{"GetClient: reflected secret ID", `{"id":"app","allowedUserGroups":[],"credentials":{"secrets":[{"id":"` + returnedIDsKey + `"}]}}`, func(c *client.Client) error { _, err := c.GetClient(ctx, "app"); return err }, false, nil},
		{"UpdateClient: another client", `{"id":"other","allowedUserGroups":[]}`, func(c *client.Client) error {
			_, err := c.UpdateClient(ctx, "app", &client.OIDCClientCreateRequest{Name: "n"})
			return err
		}, true, nil},
		{"UpdateClientAllowedUserGroups: read-back of another client", `{"id":"other","allowedUserGroups":[]}`, func(c *client.Client) error {
			_, err := c.UpdateClientAllowedUserGroups(ctx, "app", nil)
			return err
		}, true, nil},
		{"ListClients: reflected group ID", pageJSON(`{"id":"app","allowedUserGroups":[{"id":"` + returnedIDsKey + `"}]}`), func(c *client.Client) error { _, err := c.ListClients(ctx); return err }, false, nil},
		// oidc_client_secrets.go
		{"ListClientSecrets: reflected secret ID", `[{"id":"` + returnedIDsKey + `","prefix":""}]`, func(c *client.Client) error { _, err := c.ListClientSecrets(ctx, "app"); return err }, false, client.ErrMalformedSecretList},
		{"CreateClientSecret: reflected secret ID", `{"id":"` + returnedIDsKey + `","prefix":"abcd","secret":"abcdefghijklmnopqrstuvwx"}`, func(c *client.Client) error { _, err := c.CreateClientSecret(ctx, "app", nil); return err }, false, nil},
		// users.go
		{"GetUser: another user", userJSON(returnedIDsOther), func(c *client.Client) error { _, err := c.GetUser(ctx, returnedIDsUser); return err }, false, nil},
		{"GetUser: reflected group ID", userJSON(returnedIDsUser, returnedIDsKey), func(c *client.Client) error { _, err := c.GetUser(ctx, returnedIDsUser); return err }, false, nil},
		{"UpdateUser: another user", userJSON(returnedIDsOther), func(c *client.Client) error {
			_, err := c.UpdateUser(ctx, returnedIDsUser, &client.UserCreateRequest{Username: "u"})
			return err
		}, true, nil},
		{"UpdateUserGroups: reflected group ID", userJSON(returnedIDsUser, returnedIDsKey), func(c *client.Client) error {
			_, err := c.UpdateUserGroups(ctx, returnedIDsUser, []string{returnedIDsGroup})
			return err
		}, true, nil},
		{"ListAllUsers: reflected user ID", pageJSON(userJSON(returnedIDsKey)), func(c *client.Client) error { _, err := c.ListAllUsers(ctx, ""); return err }, false, nil},
		{"ListUsersPage: reflected group ID", pageJSON(userJSON(returnedIDsUser, returnedIDsKey)), func(c *client.Client) error { _, err := c.ListUsersPage(ctx, 1, 20, ""); return err }, false, nil},
		// groups.go
		{"GetUserGroup: another group", `{"id":"` + returnedIDsOther + `","name":"g"}`, func(c *client.Client) error { _, err := c.GetUserGroup(ctx, returnedIDsGroup); return err }, false, nil},
		{"GetUserGroup: reflected member ID", `{"id":"` + returnedIDsGroup + `","users":[` + userJSON(returnedIDsKey) + `]}`, func(c *client.Client) error { _, err := c.GetUserGroup(ctx, returnedIDsGroup); return err }, false, nil},
		{"UpdateUserGroup: another group", `{"id":"` + returnedIDsOther + `","name":"g"}`, func(c *client.Client) error {
			_, err := c.UpdateUserGroup(ctx, returnedIDsGroup, &client.UserGroupCreateRequest{Name: "g"})
			return err
		}, true, nil},
		{"ListUserGroups: reflected group ID", pageJSON(`{"id":"` + returnedIDsKey + `","name":"g"}`), func(c *client.Client) error { _, err := c.ListUserGroups(ctx); return err }, false, nil},
		// group_search.go
		{"SearchUserGroups: reflected group ID", pageJSON(`{"id":"` + returnedIDsKey + `","name":"g"}`), func(c *client.Client) error { _, err := c.SearchUserGroups(ctx, "g"); return err }, false, nil},
		// scim.go
		{"GetClientScimServiceProvider: reflected provider ID", `{"id":"` + returnedIDsKey + `","endpoint":"https://scim.example.com"}`, func(c *client.Client) error {
			_, err := c.GetClientScimServiceProvider(ctx, "app")
			return err
		}, false, nil},
		{"GetClientScimServiceProvider: another client's provider", `{"id":"` + returnedIDsSCIM + `","oidcClient":{"id":"other"}}`, func(c *client.Client) error {
			_, err := c.GetClientScimServiceProvider(ctx, "app")
			return err
		}, false, nil},
		{"UpdateScimServiceProvider: another provider", `{"id":"` + returnedIDsOther + `","oidcClient":{"id":""}}`, func(c *client.Client) error {
			_, err := c.UpdateScimServiceProvider(ctx, returnedIDsSCIM, &client.ScimServiceProviderCreateRequest{OidcClientID: "app"})
			return err
		}, true, nil},
		// signup_tokens.go
		{"ListSignupTokens: reflected token ID", pageJSON(`{"id":"` + returnedIDsKey + `","token":"t"}`), func(c *client.Client) error { _, err := c.ListSignupTokens(ctx); return err }, false, nil},
		{"CreateSignupToken: reflected group ID", `{"id":"` + returnedIDsUser + `","token":"t","userGroups":[{"id":"` + returnedIDsKey + `"}]}`, func(c *client.Client) error {
			created, err := c.CreateSignupToken(ctx, &client.SignupTokenCreateRequest{UsageLimit: 1})
			if created != nil && len(created.UserGroups) > 0 {
				return errors.New("an unchecked group was returned")
			}
			return err
		}, true, nil},
		// api_keys.go
		{"ListAPIKeys: reflected key ID", pageJSON(`{"id":"` + returnedIDsKey + `","name":"k"}`), func(c *client.Client) error { _, err := c.ListAPIKeys(ctx); return err }, false, nil},
		// apis.go
		{"GetAPI: another API", `{"id":"` + returnedIDsOther + `","name":"a","resource":"https://api.example.com","permissions":[]}`, func(c *client.Client) error { _, err := c.GetAPI(ctx, returnedIDsAPI); return err }, false, nil},
		{"UpdateAPI: reflected permission ID", `{"id":"` + returnedIDsAPI + `","name":"a","resource":"https://api.example.com","permissions":[{"id":"` + returnedIDsKey + `","key":"k","name":"n"}]}`, func(c *client.Client) error {
			_, err := c.UpdateAPI(ctx, returnedIDsAPI, &client.APIUpdateRequest{Name: "a"})
			return err
		}, true, nil},
		// app_config.go
		{"GetApplicationConfig: reflected default group ID", `[{"key":"signupDefaultUserGroupIDs","value":"[\"` + returnedIDsKey + `\"]"}]`, func(c *client.Client) error { _, err := c.GetApplicationConfig(ctx); return err }, false, nil},
		{"UpdateApplicationConfig: default group ID that is not a UUID", `[{"key":"signupDefaultUserGroupIDs","value":"[\"not-a-group\"]"}]`, func(c *client.Client) error {
			_, err := c.UpdateApplicationConfig(ctx, &client.ApplicationConfig{})
			return err
		}, true, nil},
		// current_user.go
		{"GetCurrentUser: reflected group ID", userJSON(returnedIDsUser, returnedIDsKey), func(c *client.Client) error { _, err := c.GetCurrentUser(ctx); return err }, false, nil},
		// group_relations.go
		{"GetUserGroupDetail: another group", `{"id":"` + returnedIDsOther + `","customClaims":[],"users":[],"allowedOidcClients":[]}`, func(c *client.Client) error {
			_, err := c.GetUserGroupDetail(ctx, returnedIDsGroup)
			return err
		}, false, nil},
		{"GetUserGroupDetail: reflected client ID", `{"id":"` + returnedIDsGroup + `","customClaims":[],"users":[],"allowedOidcClients":[{"id":"x` + returnedIDsKey + `"}]}`, func(c *client.Client) error {
			_, err := c.GetUserGroupDetail(ctx, returnedIDsGroup)
			return err
		}, false, nil},
		{"GroupMemberIDs: reflected group ID", pageJSON(userJSON(returnedIDsUser, returnedIDsKey)), func(c *client.Client) error { _, err := c.GroupMemberIDs(ctx); return err }, false, nil},
		// group_members.go
		{"SetGroupMembers: another group", `{"id":"` + returnedIDsOther + `","users":[]}`, func(c *client.Client) error {
			_, err := c.SetGroupMembers(ctx, returnedIDsGroup, nil)
			return err
		}, true, nil},
		{"SetGroupMembers: reflected member ID", `{"id":"` + returnedIDsGroup + `","users":[{"id":"` + returnedIDsKey + `"}]}`, func(c *client.Client) error {
			_, err := c.SetGroupMembers(ctx, returnedIDsGroup, nil)
			return err
		}, true, nil},
		// passkeys.go
		{"ListUserPasskeys: reflected passkey ID", `[{"id":"` + returnedIDsKey + `","name":"k"}]`, func(c *client.Client) error { _, err := c.ListUserPasskeys(ctx, returnedIDsUser); return err }, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := returnedIDsServer(t, tc.body)
			err := tc.call(c)
			require.Error(t, err)
			want := tc.sentinel
			if want == nil {
				want = client.ErrInvalidIdentifier
			}
			assert.ErrorIs(t, err, want)
			if tc.mutation {
				assert.ErrorIs(t, err, client.ErrResultUnread, "the change was made; only its result is refused")
			}
			assert.NotContains(t, err.Error(), returnedIDsKey)
		})
	}
}

// A UUID addressed in upper case comes back from PostgreSQL in lower case:
// the same object, accepted. A later write for it addresses the server's
// spelling.
func TestClient_ReturnedUUIDsCompareWithoutCase(t *testing.T) {
	ctx := context.Background()
	upper := strings.ToUpper(returnedIDsUser)

	c, _ := returnedIDsServer(t, userJSON(returnedIDsUser, returnedIDsGroup))
	user, err := c.GetUser(ctx, upper)
	require.NoError(t, err)
	assert.Equal(t, returnedIDsUser, user.ID, "the server's spelling is returned")

	c, _ = returnedIDsServer(t, `{"id":"`+returnedIDsGroup+`","name":"g"}`)
	_, err = c.GetUserGroup(ctx, strings.ToUpper(returnedIDsGroup))
	require.NoError(t, err)

	// AddUserToGroup reads the user, then writes its groups: the write goes
	// to the ID the read returned, and a group already held in another case
	// is already a membership.
	c, paths := returnedIDsServer(t, userJSON(returnedIDsUser, returnedIDsOther))
	require.Error(t, c.AddUserToGroup(ctx, upper, returnedIDsGroup), "the fake keeps the old groups, so the new one is missing")
	assert.Contains(t, *paths, "PUT /api/users/"+returnedIDsUser+"/user-groups")
	assert.NotContains(t, *paths, "PUT /api/users/"+upper+"/user-groups")

	c, paths = returnedIDsServer(t, userJSON(returnedIDsUser, returnedIDsGroup))
	require.NoError(t, c.AddUserToGroup(ctx, returnedIDsUser, strings.ToUpper(returnedIDsGroup)))
	assert.Equal(t, []string{"GET /api/users/" + returnedIDsUser}, *paths, "already a member: nothing is written")

	// A removal that compared case-sensitively would find nothing to remove
	// and report success while the membership stays.
	c, paths = returnedIDsServer(t, userJSON(returnedIDsUser, returnedIDsGroup))
	err = c.RemoveUserFromGroup(ctx, returnedIDsUser, strings.ToUpper(returnedIDsGroup))
	var mismatch *client.UserGroupsMismatchError
	require.ErrorAs(t, err, &mismatch, "the fake keeps the group, so the removal is reported as not applied")
	assert.Contains(t, *paths, "PUT /api/users/"+returnedIDsUser+"/user-groups")

	held, err := c.UserHasGroupMembership(ctx, returnedIDsUser, strings.ToUpper(returnedIDsGroup))
	require.NoError(t, err)
	assert.True(t, held)

	// An OIDC client ID is text: another spelling is another client.
	c, _ = returnedIDsServer(t, `{"id":"app","allowedUserGroups":[]}`)
	_, err = c.GetClient(ctx, "App")
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
}

// Revocation is confirmed only when the list no longer holds the secret in
// any spelling.
func TestClient_RevokeClientSecretComparesTheListedIDAsAUUID(t *testing.T) {
	const secret = "eeeeeeee-0000-4000-8000-0000000000e1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = fmt.Fprint(w, `[{"id":"`+secret+`","prefix":"abcd"}]`)
		}
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, returnedIDsKey, false, 5)
	require.NoError(t, err)
	err = c.RevokeClientSecret(context.Background(), "app", strings.ToUpper(secret))
	require.Error(t, err, "the secret is still listed")
	assert.Contains(t, err.Error(), "still listed")
}
