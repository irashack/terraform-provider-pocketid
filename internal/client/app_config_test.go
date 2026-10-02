package client_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func appConfigVariables() []client.AppConfigVariable {
	return []client.AppConfigVariable{
		{Key: "appName", Type: "string", Value: "My App"},
		{Key: "sessionDuration", Type: "number", Value: "60"},
		{Key: "homePageUrl", Type: "string", Value: "https://example.com"},
		{Key: "smtpPassword", Type: "string", Value: "s3cret"},
		{Key: "ldapEnabled", Type: "boolean", Value: "true"},
		{Key: "ldapBindPassword", Type: "string", Value: "ldapsecret"},
	}
}

func TestGetApplicationConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/application-configuration/all", r.URL.Path)
		assert.Equal(t, "test-token", r.Header.Get("X-API-KEY"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(appConfigVariables())
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	cfg, err := c.GetApplicationConfig(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "My App", cfg.AppName)
	assert.Equal(t, "60", cfg.SessionDuration)
	assert.Equal(t, "https://example.com", cfg.HomePageURL)
	assert.Equal(t, "s3cret", cfg.SmtpPassword)
	assert.Equal(t, "true", cfg.LdapEnabled)
	assert.Equal(t, "ldapsecret", cfg.LdapBindPassword)
	// Keys not present in the response stay at their zero value.
	assert.Equal(t, "", cfg.AccentColor)
}

func TestUpdateApplicationConfig(t *testing.T) {
	var receivedBody map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/api/application-configuration", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &receivedBody))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]client.AppConfigVariable{
			{Key: "appName", Type: "string", Value: "Updated App"},
			{Key: "smtpHost", Type: "string", Value: "smtp.example.com"},
		})
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	updated, err := c.UpdateApplicationConfig(context.Background(), &client.ApplicationConfig{
		AppName:  "Updated App",
		SmtpHost: "smtp.example.com",
	})
	require.NoError(t, err)

	// The PUT body should carry the JSON keys expected by Pocket-ID.
	assert.Equal(t, "Updated App", receivedBody["appName"])
	assert.Equal(t, "smtp.example.com", receivedBody["smtpHost"])

	// Response is parsed back from the key/value array.
	assert.Equal(t, "Updated App", updated.AppName)
	assert.Equal(t, "smtp.example.com", updated.SmtpHost)
}

// autoCreateOidcClientSecret (Pocket ID 2.17.0+) is read so that an update can
// send it back unchanged. It stays nil, and so absent from the update, when the
// server does not report it.
func TestApplicationConfigAutoCreateOIDCClientSecret(t *testing.T) {
	reported := func(value string) []client.AppConfigVariable {
		return append(appConfigVariables(), client.AppConfigVariable{Key: "autoCreateOidcClientSecret", Type: "bool", Value: value})
	}
	falseValue, trueValue := "false", "true"
	for name, tc := range map[string]struct {
		reported []client.AppConfigVariable
		want     *string
	}{
		"2.17 reports false": {reported("false"), &falseValue},
		"2.17 reports true":  {reported("true"), &trueValue},
		"older server":       {appConfigVariables(), nil},
	} {
		t.Run(name, func(t *testing.T) {
			var sent map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPut {
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
				}
				_ = json.NewEncoder(w).Encode(tc.reported)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			cfg, err := c.GetApplicationConfig(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.AutoCreateOIDCClientSecret)

			_, err = c.UpdateApplicationConfig(context.Background(), cfg)
			require.NoError(t, err)
			if tc.want == nil {
				assert.NotContains(t, sent, "autoCreateOidcClientSecret")
			} else {
				assert.Equal(t, *tc.want, sent["autoCreateOidcClientSecret"])
			}
		})
	}
}
