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

const (
	apiTestAPIID        = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	apiTestPermissionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	// apiTestNotFoundBody is Pocket ID's structured not-found error for an API
	// (apperror.NotFound("API"), v2.14.0 to v2.17.0).
	apiTestNotFoundBody = `{"error":"API not found","code":"not_found","details":{"resource":"API"},"request_id":"r"}`
	// apiTestClientNotFoundBody is the same error for an OIDC client.
	apiTestClientNotFoundBody = `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"},"request_id":"r"}`
)

func apiTestServer(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 5)
	require.NoError(t, err)
	return c
}

func TestClient_CreateAPI(t *testing.T) {
	c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/apis", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"name":"Inventory","resource":"https://inventory.example"}`, string(body))
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":%q,"name":"Inventory","resource":"https://inventory.example","createdAt":"2026-01-01T00:00:00Z","permissions":[],"allowCimdClients":false}`, apiTestAPIID)
	})
	api, err := c.CreateAPI(context.Background(), &client.APICreateRequest{Name: "Inventory", Resource: "https://inventory.example"})
	require.NoError(t, err)
	assert.Equal(t, apiTestAPIID, api.ID)
	assert.Equal(t, "https://inventory.example", api.Resource)
	assert.Empty(t, api.Permissions)
}

// A create response without a usable ID is an error that says the API may
// exist: nothing may build a path from it.
func TestClient_CreateAPI_UnusableID(t *testing.T) {
	for name, body := range map[string]string{
		"missing":  `{"name":"Inventory"}`,
		"not uuid": `{"id":"../other","name":"Inventory"}`,
		"garbage":  `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprint(w, body)
			})
			_, err := c.CreateAPI(context.Background(), &client.APICreateRequest{Name: "Inventory", Resource: "https://inventory.example"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "the API may exist")
			assert.NotContains(t, err.Error(), "../other")
		})
	}
}

// A server that returns the API key as the new API's ID is refused, without
// the key reaching the error.
func TestClient_CreateAPI_IDReflectingKey(t *testing.T) {
	const key = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":%q,"name":"Inventory"}`, key)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, key, false, 5)
	require.NoError(t, err)
	_, err = c.CreateAPI(context.Background(), &client.APICreateRequest{Name: "Inventory", Resource: "urn:x"})
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)
	assert.Contains(t, err.Error(), "contains the API key")
	assert.NotContains(t, err.Error(), key)
}

func TestClient_GetAPI_NotFound(t *testing.T) {
	c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/apis/"+apiTestAPIID, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, apiTestNotFoundBody)
	})
	_, err := c.GetAPI(context.Background(), apiTestAPIID)
	require.Error(t, err)
	assert.True(t, client.IsNotFound(err, client.ResourceAPI))
	assert.False(t, client.IsNotFound(err, client.ResourceOIDCClient))
}

// IDs are checked before they reach a path; nothing is sent for a bad one.
func TestClient_APIs_RefuseBadIdentifiers(t *testing.T) {
	var calls atomic.Int32
	c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	ctx := context.Background()
	_, err := c.GetAPI(ctx, "../users")
	assert.ErrorIs(t, err, client.ErrInvalidIdentifier)
	_, err = c.UpdateAPIPermissions(ctx, "not-a-uuid", nil)
	assert.ErrorIs(t, err, client.ErrInvalidIdentifier)
	_, err = c.SetAPIClientAccess(ctx, apiTestAPIID, "https://cimd.example/client", client.APIClientGrant{UserDelegatedAccess: true})
	assert.ErrorIs(t, err, client.ErrInvalidIdentifier)
	err = c.RemoveAPIClientAccess(ctx, "x", "client")
	assert.ErrorIs(t, err, client.ErrInvalidIdentifier)
	_, err = c.FindClientAPIGrant(ctx, "client", "x")
	assert.ErrorIs(t, err, client.ErrInvalidIdentifier)
	assert.Zero(t, calls.Load())
}

// Empty lists go out as [] rather than null, and the description is always
// sent (null clears it).
func TestClient_APIWritesSendEmptyLists(t *testing.T) {
	var bodies []string
	c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(body))
		if strings.HasSuffix(r.URL.Path, "/clients/app") {
			_, _ = fmt.Fprint(w, `{"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"permissions":null}`, apiTestAPIID)
	})
	ctx := context.Background()
	_, err := c.UpdateAPIPermissions(ctx, apiTestAPIID, nil)
	require.NoError(t, err)
	_, err = c.UpdateAPIPermissions(ctx, apiTestAPIID, []client.APIPermissionInput{{Key: "read", Name: "Read"}})
	require.NoError(t, err)
	_, err = c.UpdateAPICIMDAccess(ctx, apiTestAPIID, false, nil)
	require.NoError(t, err)
	_, err = c.SetAPIClientAccess(ctx, apiTestAPIID, "app", client.APIClientGrant{UserDelegatedAccess: true})
	require.NoError(t, err)
	require.Len(t, bodies, 4)
	assert.Equal(t, "PUT /api/apis/"+apiTestAPIID+`/permissions {"permissions":[]}`, bodies[0])
	assert.Equal(t, "PUT /api/apis/"+apiTestAPIID+`/permissions {"permissions":[{"key":"read","name":"Read","description":null}]}`, bodies[1])
	assert.Equal(t, "PUT /api/apis/"+apiTestAPIID+`/cimd-access {"enabled":false,"permissionIds":[]}`, bodies[2])
	assert.Equal(t, "PUT /api/apis/"+apiTestAPIID+`/clients/app {"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}`, bodies[3])
}

// A grant write is a mutation: it is sent once, even when the server answers
// with an error a read would be retried on.
func TestClient_SetAPIClientAccess_NotRetried(t *testing.T) {
	var calls atomic.Int32
	c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := c.SetAPIClientAccess(context.Background(), apiTestAPIID, "app", client.APIClientGrant{UserDelegatedAccess: true})
	require.Error(t, err)
	assert.False(t, client.IsDefiniteRejection(err))
	assert.Equal(t, int32(1), calls.Load())
}

// The stored grant is returned as the server reports it; an undecodable
// answer to an accepted write is ErrResultUnread.
func TestClient_SetAPIClientAccess_Result(t *testing.T) {
	c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var sent client.APIClientGrant
		require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
		assert.Equal(t, []string{apiTestPermissionID, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}, sent.ClientPermissionIDs)
		// The server dropped the second ID.
		_, _ = fmt.Fprintf(w, `{"userDelegatedAccess":false,"clientAccess":true,"userDelegatedPermissionIds":[],"clientPermissionIds":[%q]}`, apiTestPermissionID)
	})
	applied, err := c.SetAPIClientAccess(context.Background(), apiTestAPIID, "app", client.APIClientGrant{
		ClientAccess: true, ClientPermissionIDs: []string{apiTestPermissionID, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{apiTestPermissionID}, applied.ClientPermissionIDs)

	unread := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, `[`) })
	_, err = unread.SetAPIClientAccess(context.Background(), apiTestAPIID, "app", client.APIClientGrant{UserDelegatedAccess: true})
	assert.ErrorIs(t, err, client.ErrResultUnread)
}

// A successful answer to the grant PUT that does not describe a grant (null,
// {}, a partial object, the wrong types) is not an empty grant: the write was
// accepted, so the result is unread, and the caller reads the grant back. The
// body is never quoted.
func TestClient_SetAPIClientAccess_IncompleteResponse(t *testing.T) {
	const all = `"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]`
	without := func(field string) string {
		fields := map[string]string{
			"userDelegatedAccess": `"userDelegatedAccess":true`, "clientAccess": `"clientAccess":false`,
			"userDelegatedPermissionIds": `"userDelegatedPermissionIds":[]`, "clientPermissionIds": `"clientPermissionIds":[]`,
		}
		delete(fields, field)
		var parts []string
		for _, key := range []string{"userDelegatedAccess", "clientAccess", "userDelegatedPermissionIds", "clientPermissionIds"} {
			if part, ok := fields[key]; ok {
				parts = append(parts, part)
			}
		}
		return "{" + strings.Join(parts, ",") + `,"note":"BODY-MARKER"}`
	}
	for name, body := range map[string]string{
		"null":                      `null`,
		"empty object":              `{"note":"BODY-MARKER"}`,
		"array":                     `[{"note":"BODY-MARKER"}]`,
		"string":                    `"BODY-MARKER"`,
		"number":                    `7`,
		"empty body":                ``,
		"missing user access":       without("userDelegatedAccess"),
		"missing client access":     without("clientAccess"),
		"missing user permissions":  without("userDelegatedPermissionIds"),
		"missing client permission": without("clientPermissionIds"),
		"null flag":                 `{"userDelegatedAccess":null,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}`,
		"string flag":               `{"userDelegatedAccess":"true","clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}`,
		"list is an object":         `{"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":{},"clientPermissionIds":[]}`,
		"list holds a number":       `{"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[7],"clientPermissionIds":[]}`,
		"list holds a null":         `{"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[null],"clientPermissionIds":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) })
			applied, err := c.SetAPIClientAccess(context.Background(), apiTestAPIID, "app", client.APIClientGrant{UserDelegatedAccess: true})
			require.Error(t, err)
			assert.Nil(t, applied)
			assert.ErrorIs(t, err, client.ErrResultUnread)
			assert.False(t, client.IsDefiniteRejection(err))
			assert.NotContains(t, err.Error(), "BODY-MARKER")
		})
	}

	// The same fields complete are a grant, whatever else the object carries,
	// and a list the server leaves null means no permissions.
	for name, tc := range map[string]struct {
		body  string
		empty bool
	}{
		"complete":             {`{` + all + `}`, false},
		"extra fields":         {`{` + all + `,"later":{"x":1}}`, false},
		"empty grant":          {`{"userDelegatedAccess":false,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}`, true},
		"null permission list": {`{"userDelegatedAccess":false,"clientAccess":false,"userDelegatedPermissionIds":null,"clientPermissionIds":null}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, tc.body) })
			applied, err := c.SetAPIClientAccess(context.Background(), apiTestAPIID, "app", client.APIClientGrant{UserDelegatedAccess: true})
			require.NoError(t, err)
			assert.Equal(t, tc.empty, applied.IsEmpty())
			assert.NotNil(t, applied.UserDelegatedPermissionIDs)
			assert.NotNil(t, applied.ClientPermissionIDs)
		})
	}
}

// An answer to the grant list that is not a list, or whose entries do not say
// what the client may do, is a failed read, not a client with no grant.
func TestClient_ListClientAPIGrants_IncompleteResponse(t *testing.T) {
	grantFields := `"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]`
	api := fmt.Sprintf(`"api":{"id":%q,"permissions":[]}`, apiTestAPIID)
	for name, body := range map[string]string{
		"null":                      `null`,
		"empty object":              `{"note":"BODY-MARKER"}`,
		"string":                    `"BODY-MARKER"`,
		"empty body":                ``,
		"empty entry":               `[{}]`,
		"null entry":                `[null]`,
		"entry without grant":       `[{` + api + `,"cimdGrantedAccess":false,"cimdGrantedPermissionIds":[]}]`,
		"entry without api":         `[{` + grantFields + `}]`,
		"api without id":            `[{"api":{"permissions":[]},` + grantFields + `}]`,
		"api is null":               `[{"api":null,` + grantFields + `}]`,
		"missing user access":       `[{` + api + `,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}]`,
		"missing client access":     `[{` + api + `,"userDelegatedAccess":true,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}]`,
		"missing user permissions":  `[{` + api + `,"userDelegatedAccess":true,"clientAccess":false,"clientPermissionIds":[]}]`,
		"missing client permission": `[{` + api + `,"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[]}]`,
		"flag of the wrong type":    `[{` + api + `,"userDelegatedAccess":"yes","clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}]`,
		"one incomplete entry":      `[{` + api + `,` + grantFields + `},{"api":{"id":"` + apiTestPermissionID + `"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) })
			grants, err := c.ListClientAPIGrants(context.Background(), "app")
			require.Error(t, err)
			assert.Nil(t, grants)
			assert.ErrorIs(t, err, client.ErrIncompleteGrantResponse)
			assert.NotContains(t, err.Error(), "BODY-MARKER")
			// The lookup fails too: it must not report a client without a grant.
			grant, err := c.FindClientAPIGrant(context.Background(), "app", apiTestAPIID)
			assert.Error(t, err)
			assert.Nil(t, grant)
		})
	}

	// Complete entries, with null permission lists and without the CIMD fields.
	c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `[{%s,"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":null,"clientPermissionIds":null}]`, api)
	})
	grant, err := c.FindClientAPIGrant(context.Background(), "app", apiTestAPIID)
	require.NoError(t, err)
	require.NotNil(t, grant)
	assert.True(t, grant.UserDelegatedAccess)
	assert.Empty(t, grant.UserDelegatedPermissionIDs)
}

func TestClient_RemoveAPIClientAccess_ConfirmedAbsence(t *testing.T) {
	for name, tc := range map[string]struct {
		body             string
		apiGone, appGone bool
	}{
		"api gone":    {apiTestNotFoundBody, true, false},
		"client gone": {apiTestClientNotFoundBody, false, true},
		"bare 404":    {``, false, false},
		"route 404":   {`{"error":"API endpoint not found"}`, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "DELETE", r.Method)
				assert.Equal(t, "/api/apis/"+apiTestAPIID+"/clients/app", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, tc.body)
			})
			err := c.RemoveAPIClientAccess(context.Background(), apiTestAPIID, "app")
			require.Error(t, err)
			assert.Equal(t, tc.apiGone, client.IsNotFound(err, client.ResourceAPI))
			assert.Equal(t, tc.appGone, client.IsNotFound(err, client.ResourceOIDCClient))
		})
	}
}

// FindClientAPIGrant picks the API's entry from the client's list, ignoring
// other APIs and an entry the client reaches only through CIMD access.
func TestClient_FindClientAPIGrant(t *testing.T) {
	const other = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	list := fmt.Sprintf(`[
		{"api":{"id":%q,"permissions":[]},"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[],"cimdGrantedAccess":false,"cimdGrantedPermissionIds":[]},
		{"api":{"id":%q,"permissions":[{"id":%q,"key":"read","name":"Read","allowedForCimdClients":false}]},"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[%q],"clientPermissionIds":[],"cimdGrantedAccess":false,"cimdGrantedPermissionIds":[]}
	]`, other, apiTestAPIID, apiTestPermissionID, apiTestPermissionID)
	cimdOnly := fmt.Sprintf(`[{"api":{"id":%q,"permissions":[]},"userDelegatedAccess":false,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[],"cimdGrantedAccess":true,"cimdGrantedPermissionIds":[]}]`, apiTestAPIID)

	for name, tc := range map[string]struct {
		body  string
		found bool
	}{
		"granted":   {list, true},
		"none":      {`[]`, false},
		"cimd only": {cimdOnly, false},
	} {
		t.Run(name, func(t *testing.T) {
			c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/api-access/app/apis", r.URL.Path)
				_, _ = fmt.Fprint(w, tc.body)
			})
			grant, err := c.FindClientAPIGrant(context.Background(), "app", apiTestAPIID)
			require.NoError(t, err)
			if !tc.found {
				assert.Nil(t, grant)
				return
			}
			require.NotNil(t, grant)
			assert.Equal(t, apiTestAPIID, grant.API.ID)
			assert.Equal(t, []string{apiTestPermissionID}, grant.UserDelegatedPermissionIDs)
			assert.Equal(t, "read", grant.API.Permissions[0].Key)
		})
	}

	gone := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, apiTestClientNotFoundBody)
	})
	_, err := gone.FindClientAPIGrant(context.Background(), "app", apiTestAPIID)
	assert.True(t, client.IsNotFound(err, client.ResourceOIDCClient))
}

// ListAPIs reads every page.
func TestClient_ListAPIs_AllPages(t *testing.T) {
	c := apiTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/apis", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("pagination[limit]"))
		page := r.URL.Query().Get("pagination[page]")
		id := "11111111-1111-4111-8111-11111111111" + page
		_, _ = fmt.Fprintf(w, `{"data":[{"id":%q,"name":"n%s","resource":"urn:x:%s","permissions":[]}],"pagination":{"totalPages":2,"totalItems":2,"currentPage":%s,"itemsPerPage":100}}`, id, page, page, page)
	})
	apis, err := c.ListAPIs(context.Background())
	require.NoError(t, err)
	require.Len(t, apis, 2)
	assert.Equal(t, "urn:x:1", apis[0].Resource)
	assert.Equal(t, "urn:x:2", apis[1].Resource)
}

func TestAPIResourceProblem(t *testing.T) {
	for _, ok := range []string{
		"https://api.example.com",
		"https://api.example.com/v1",
		"urn:example:inventory",
		"http://localhost:8080/api",
		"https://api.example.com/v1?tenant=a",
		strings.Repeat("a", 340) + ":xxxxxxxxx",
	} {
		assert.Empty(t, client.APIResourceProblem(ok), ok)
	}
	for value, want := range map[string]string{
		"":                                   "empty",
		"api.example.com":                    "absolute URI",
		"/relative":                          "absolute URI",
		"relative/path":                      "absolute URI",
		"https://api.example.com/":           "must not end with a slash",
		"https://api.example.com/v1//":       "must not end with a slash",
		"https://api.example.com/#frag":      "fragment",
		"https://api.example.com/a b":        "whitespace",
		"https://api.example.com/\tx":        "whitespace",
		"javascript:alert(1)":                "javascript",
		"DATA:text/plain,hi":                 "javascript: or data:",
		strings.Repeat("a", 345) + ":xxxxxx": "at most 350",
		"https://exa mple.com":               "whitespace",
		"https://example.com/%zz":            "absolute URI",
	} {
		assert.Contains(t, client.APIResourceProblem(value), want, value)
	}
}

func TestAPIPermissionKeyProblem(t *testing.T) {
	for _, ok := range []string{"read", "inventory:read", "a", "Inventory.Write", "x!#$%&'()*+,-./:;<=>?@[]^_`{|}~", strings.Repeat("k", 128), "openid2", "emails"} {
		assert.Empty(t, client.APIPermissionKeyProblem(ok), ok)
	}
	for key, want := range map[string]string{
		"":                       "empty",
		strings.Repeat("k", 129): "at most 128",
		"read write":             "valid in an OAuth scope",
		`say"hi"`:                "valid in an OAuth scope",
		`back\slash`:             "valid in an OAuth scope",
		"lesen-ä":                "valid in an OAuth scope",
		"tab\tkey":               "valid in an OAuth scope",
		"openid":                 "reserved",
		"OpenID":                 "reserved",
		"offline_access":         "reserved",
		"Email_Verified":         "reserved",
		"groups":                 "reserved",
		"profile":                "reserved",
		"email":                  "reserved",
	} {
		assert.Contains(t, client.APIPermissionKeyProblem(key), want, key)
	}
}

func TestAPIClientGrant_IsEmpty(t *testing.T) {
	assert.True(t, client.APIClientGrant{}.IsEmpty())
	assert.True(t, client.APIClientGrant{UserDelegatedPermissionIDs: []string{}, ClientPermissionIDs: []string{}}.IsEmpty())
	assert.False(t, client.APIClientGrant{ClientAccess: true}.IsEmpty())
	assert.False(t, client.APIClientGrant{UserDelegatedPermissionIDs: []string{apiTestPermissionID}}.IsEmpty())
}
