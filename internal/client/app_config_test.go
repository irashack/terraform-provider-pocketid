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

// A key the provider has no field for (a setting a newer Pocket ID adds) is
// kept from the read and sent back unchanged by the update.
func TestApplicationConfigUnknownKeysSurviveUpdate(t *testing.T) {
	reported := append(appConfigVariables(),
		client.AppConfigVariable{Key: "settingFromTheFuture", Type: "string", Value: "kept"},
		client.AppConfigVariable{Key: "anotherNewSetting", Type: "bool", Value: "false"},
	)
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
		}
		_ = json.NewEncoder(w).Encode(reported)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	cfg, err := c.GetApplicationConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"settingFromTheFuture": "kept", "anotherNewSetting": "false"}, cfg.Additional)

	cfg.AppName = "Changed"
	updated, err := c.UpdateApplicationConfig(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "kept", sent["settingFromTheFuture"])
	assert.Equal(t, "false", sent["anotherNewSetting"])
	assert.Equal(t, "Changed", sent["appName"])
	assert.Equal(t, "s3cret", sent["smtpPassword"], "known keys are still sent")
	assert.Equal(t, cfg.Additional, updated.Additional)

	// A named field wins over an Additional entry of the same key.
	cfg.Additional = map[string]string{"appName": "stale", "settingFromTheFuture": "kept"}
	_, err = c.UpdateApplicationConfig(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "Changed", sent["appName"])
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

// A listed setting whose value is missing, null or not a string is not read
// as "": the response is refused, a missing or null value as incomplete, a
// value of another type as undecodable. A key left out is not reported; one
// reported as "" is.
func TestApplicationConfigIncompleteResponses(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	ctx := context.Background()

	for name, tc := range map[string]struct {
		entry string
		want  error
	}{
		"value missing": {`{"key":"smtpPassword","type":"string"}`, client.ErrIncompleteApplicationConfig},
		"value null":    {`{"key":"smtpPassword","value":null}`, client.ErrIncompleteApplicationConfig},
		"value number":  {`{"key":"smtpPassword","value":5}`, client.ErrUndecodableResponse},
		"value object":  {`{"key":"ldapBindPassword","value":{}}`, client.ErrUndecodableResponse},
	} {
		body = `[{"key":"appName","value":"Fixture"},` + tc.entry + `]`
		_, err := c.GetApplicationConfig(ctx)
		assert.ErrorIs(t, err, tc.want, name)
		assert.NotErrorIs(t, err, client.ErrResultUnread, "%s: a read changed nothing", name)
		_, err = c.UpdateApplicationConfig(ctx, &client.ApplicationConfig{AppName: "Fixture"})
		assert.ErrorIs(t, err, tc.want, name)
		assert.ErrorIs(t, err, client.ErrResultUnread, "%s: the update was made", name)
	}

	body = `[{"key":"appName","value":"Fixture"},{"key":"ldapBindPassword","value":""}]`
	cfg, err := c.GetApplicationConfig(ctx)
	require.NoError(t, err)
	assert.True(t, cfg.Reported("ldapBindPassword"), "an explicit empty value is reported")
	assert.Equal(t, "", cfg.LdapBindPassword)
	assert.False(t, cfg.Reported("smtpPassword"), "a key left out is not")
	assert.False(t, (&client.ApplicationConfig{}).Reported("appName"))
}
