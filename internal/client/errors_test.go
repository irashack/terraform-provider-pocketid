package client_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Only Pocket ID's structured not-found error for an OIDC client confirms the
// client is gone, identical from v2.14.0 to v2.17.0.
func TestIsOIDCClientNotFound(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   bool
	}{
		"structured client not found": {404, `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"},"request_id":"r"}`, true},
		"bare 404":                    {404, ``, false},
		"proxy page":                  {404, `<html><body>Not Found</body></html>`, false},
		"missing route":               {404, `{"error":"API endpoint not found"}`, false},
		"another resource":            {404, `{"error":"Client secret not found","code":"not_found","details":{"resource":"Client secret"}}`, false},
		"no resource detail":          {404, `{"error":"not found","code":"not_found"}`, false},
		"user not found":              {404, `{"error":"User not found","code":"user_not_found"}`, false},
		"wrong status":                {400, `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			_, err = c.GetClient(context.Background(), "c1")
			require.Error(t, err)
			assert.Equal(t, tc.want, client.IsOIDCClientNotFound(err))
			assert.False(t, client.IsUserNotFound(err) && tc.want, "the two signals never coincide")
		})
	}
}

// notFoundBody is what middleware.ErrorHandlerMiddleware writes for
// apperror.NotFound(name) (code "not_found", details.resource) or for a kind
// with a dedicated code (no details). The serialized shape (dto.ErrorDto) and
// the apperror package are identical in v2.14.0, v2.15.0, v2.16.0 and v2.17.0;
// only the request_id generator differs (google/uuid before 2.17), which is a
// UUID string either way.
func notFoundBody(r client.Resource, requestID string) string {
	if r.Code == "not_found" {
		return fmt.Sprintf(`{"error":%q,"code":"not_found","details":{"resource":%q},"request_id":%q}`, r.Name+" not found", r.Name, requestID)
	}
	return fmt.Sprintf(`{"error":"Not found","code":%q,"request_id":%q}`, r.Code, requestID)
}

func errorFor(t *testing.T, status int, body string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	_, err = c.GetClient(context.Background(), "c1")
	require.Error(t, err)
	return err
}

func TestIsNotFound(t *testing.T) {
	kinds := map[string]client.Resource{
		"user":                  client.ResourceUser,
		"user group":            client.ResourceUserGroup,
		"OIDC client":           client.ResourceOIDCClient,
		"client secret":         client.ResourceClientSecret,
		"SCIM service provider": client.ResourceSCIMServiceProvider,
		"API":                   client.ResourceAPI,
		"passkey":               client.ResourcePasskey,
		"API key":               client.ResourceAPIKey,
		"image":                 client.ResourceImage,
	}
	versions := map[string]string{
		"2.14.0": "5c3f1d2e-8b4a-4c6d-9e0f-1a2b3c4d5e6f",
		"2.15.0": "6d4e2f3a-9c5b-4d7e-8f1a-2b3c4d5e6f70",
		"2.16.0": "7e5f3a4b-0d6c-4e8f-9a2b-3c4d5e6f7081",
		"2.17.0": "8f6a4b5c-1e7d-4f9a-8b3c-4d5e6f708192",
	}
	for version, requestID := range versions {
		for name, kind := range kinds {
			t.Run(version+"/"+name, func(t *testing.T) {
				err := errorFor(t, http.StatusNotFound, notFoundBody(kind, requestID))
				assert.True(t, client.IsNotFound(err, kind), "its own not-found error confirms absence")
				for otherName, other := range kinds {
					if otherName != name {
						assert.False(t, client.IsNotFound(err, other), "a %s error must not confirm a missing %s", name, otherName)
					}
				}
			})
		}
	}
}

func TestIsNotFoundRejectsEverythingElse(t *testing.T) {
	structuredClient := `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"},"request_id":"r"}`
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"missing route":             {404, `{"error":"API endpoint not found"}`},
		"empty 404":                 {404, ``},
		"proxy page":                {404, `<html><body>404 Not Found</body></html>`},
		"plain text":                {404, `404 page not found`},
		"JSON without code":         {404, `{"error":"OIDC client not found"}`},
		"not_found without details": {404, `{"error":"not found","code":"not_found"}`},
		"resource not a string":     {404, `{"code":"not_found","details":{"resource":["OIDC client"]}}`},
		"resource case differs":     {404, `{"code":"not_found","details":{"resource":"oidc client"}}`},
		"structured but 400":        {400, structuredClient},
		"structured but 410":        {410, structuredClient},
		"structured but 500":        {500, structuredClient},
		"code not Pocket ID's form": {404, `{"code":"NOT_FOUND","details":{"resource":"OIDC client"}}`},
		"validation error":          {400, `{"error":"Request validation failed","code":"validation_failed","details":{"fields":[{"field":"name","code":"required","message":"is required"}]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			err := errorFor(t, tc.status, tc.body)
			assert.False(t, client.IsNotFound(err, client.ResourceOIDCClient))
			assert.False(t, client.IsNotFound(err, client.ResourceUser))
		})
	}
	assert.False(t, client.IsNotFound(nil, client.ResourceOIDCClient))
	assert.False(t, client.IsNotFound(fmt.Errorf("wrapped: %w", context.Canceled), client.ResourceOIDCClient))
	assert.False(t, client.IsNotFound(errorFor(t, 404, structuredClient), client.Resource{}), "the zero kind matches nothing")
	assert.False(t, client.IsNotFound(errorFor(t, 404, structuredClient), client.Resource{Code: "not_found"}), "a not_found kind needs its name")
	assert.True(t, client.IsNotFound(fmt.Errorf("context: %w", errorFor(t, 404, structuredClient)), client.ResourceOIDCClient), "wrapping keeps the answer")
}

// The structured fields are exposed for comparison; the error text stays
// status-only.
func TestHTTPErrorCarriesCodeAndResource(t *testing.T) {
	err := errorFor(t, 409, `{"error":"Name is already in use","code":"already_in_use","details":{"property":"name"},"request_id":"r"}`)
	var status *client.HTTPError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, "already_in_use", status.Code)
	assert.Empty(t, status.Resource)
	assert.Equal(t, "HTTP 409: Conflict", err.Error())

	err = errorFor(t, 404, `{"error":"User group not found","code":"not_found","details":{"resource":"User group"}}`)
	require.ErrorAs(t, err, &status)
	assert.Equal(t, "not_found", status.Code)
	assert.Equal(t, "User group", status.Resource)
	assert.False(t, status.MissingEndpoint)

	err = errorFor(t, 404, `{"error":"API endpoint not found"}`)
	require.ErrorAs(t, err, &status)
	assert.True(t, status.MissingEndpoint)
	assert.Empty(t, status.Code)

	// A code or resource that does not look like Pocket ID's is dropped, so
	// nothing a server reflects into them is kept.
	err = errorFor(t, 404, `{"code":"not_found","details":{"resource":"OIDC client\" secret-value-0123456789"}}`)
	require.ErrorAs(t, err, &status)
	assert.Empty(t, status.Resource)
	err = errorFor(t, 404, `{"code":"secret-value-0123456789","details":{"resource":"OIDC client"}}`)
	require.ErrorAs(t, err, &status)
	assert.Empty(t, status.Code)
	assert.Empty(t, status.Resource)
}
