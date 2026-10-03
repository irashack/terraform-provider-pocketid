//go:build acc
// +build acc

package provider_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testAccAppConfigVariable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func testAccAppConfig(t *testing.T) map[string]string {
	t.Helper()
	var vars []testAccAppConfigVariable
	status, err := testAccAPI(http.MethodGet, "/api/application-configuration/all", nil, &vars)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	config := make(map[string]string, len(vars))
	for _, v := range vars {
		config[v.Key] = v.Value
	}
	return config
}

func testAccPutAppConfig(t *testing.T, config map[string]string) int {
	t.Helper()
	status, err := testAccAPI(http.MethodPut, "/api/application-configuration", config, nil)
	require.NoError(t, err)
	return status
}

// The plan-time rules of pocketid_application_config are Pocket ID's own:
// every value the provider refuses (other than session durations below one
// minute, refused deliberately) is refused by the server too, a boundary
// value it accepts is accepted by the server, and an empty value for a key
// with a non-empty default is stored as that default, which is why the
// provider refuses it. Values are sent through the API directly, one key
// changed at a time, and the original configuration is restored.
func TestAccAPI_applicationConfigRules(t *testing.T) {
	testAccPreCheck(t)
	original := testAccAppConfig(t)
	t.Cleanup(func() {
		if status := testAccPutAppConfig(t, original); status != http.StatusOK {
			t.Errorf("restoring the application configuration returned HTTP %d", status)
		}
	})
	with := func(key, value string) map[string]string {
		config := make(map[string]string, len(original))
		for k, v := range original {
			config[k] = v
		}
		config[key] = value
		return config
	}

	refused := []struct{ key, value string }{
		{"appName", ""},
		{"appName", strings.Repeat("é", 31)},
		{"sessionDuration", ""},
		{"sessionDuration", "1.5"},
		{"sessionDuration", "sixty"},
		{"homePageUrl", ""},
		{"emailsVerified", "True"},
		{"emailsVerified", "1"},
		{"allowUserSignups", "withtoken"},
		{"signupDefaultUserGroupIDs", "null"},
		{"signupDefaultUserGroupIDs", "[1]"},
		{"signupDefaultCustomClaims", `{"department":"it"}`},
		{"signupDefaultCustomClaims", `[{"key":"department"}]`},
		{"signupDefaultCustomClaims", `[{"key":"department","value":1}]`},
		{"cimdUrlAllowlist", `["javascript:alert(1)"]`},
		{"cimdUrlAllowlist", `["example.com/client.json"]`},
		{"cimdUrlAllowlist", "null"},
		// URLs that net/url parses but that do not compile as URL patterns.
		{"cimdUrlAllowlist", `["https://example(.com/"]`},
		{"cimdUrlAllowlist", `["https://example.com/(client"]`},
		{"cimdUrlAllowlist", `["https://example.com/{client"]`},
		{"smtpFrom", "Pocket ID <no-reply@example.com>"},
		{"smtpFrom", "no-reply"},
		{"smtpTls", "STARTTLS"},
		{"smtpTls", ""},
		{"webauthnUserVerification", "discouraged"},
		{"webauthnAuthenticatorAttachment", "roaming"},
		{"ldapSoftDeleteUsers", ""},
	}
	if testAccServerAtLeast(t, "2.17.0") {
		refused = append(refused, struct{ key, value string }{"autoCreateOidcClientSecret", ""})
	}
	for _, tc := range refused {
		assert.Equal(t, http.StatusBadRequest, testAccPutAppConfig(t, with(tc.key, tc.value)), "%s = %q", tc.key, tc.value)
	}
	assert.Equal(t, original, testAccAppConfig(t), "a refused update changed the configuration")

	accepted := []struct{ key, value string }{
		{"appName", strings.Repeat("é", 30)},
		{"sessionDuration", "+61"},
		{"signupDefaultCustomClaims", `[{"key":"department","value":"it"}]`},
		{"cimdUrlAllowlist", `["https://*.example.com/*", "*"]`},
		{"cimdUrlAllowlist", `["https://[::1]:*/client.json", "https://example.com/a:b/**"]`},
		{"smtpFrom", "no-reply@example.com"},
		{"smtpHost", ""},
	}
	for _, tc := range accepted {
		require.Equal(t, http.StatusOK, testAccPutAppConfig(t, with(tc.key, tc.value)), "%s = %q", tc.key, tc.value)
		assert.Equal(t, tc.value, testAccAppConfig(t)[tc.key], "%s is stored as sent", tc.key)
	}

	resets := map[string]string{
		"accentColor":                  "default",
		"signupDefaultUserGroupIDs":    "[]",
		"signupDefaultCustomClaims":    "[]",
		"cimdUrlAllowlist":             "[]",
		"ldapUserSearchFilter":         "(objectClass=person)",
		"ldapUserGroupSearchFilter":    "(objectClass=groupOfNames)",
		"ldapAttributeUserDisplayName": "cn",
		"ldapAttributeGroupMember":     "member",
	}
	for key, defaultValue := range resets {
		require.Equal(t, http.StatusOK, testAccPutAppConfig(t, with(key, "")), key)
		assert.Equal(t, defaultValue, testAccAppConfig(t)[key], "an empty %s is stored as its default", key)
	}

	// An omitted or empty password clears it: an update that does not send
	// the current password back loses it. This is why the provider sends the
	// server's current password when a write-only input's version did not
	// change.
	for _, key := range []string{"smtpPassword", "ldapBindPassword"} {
		require.Equal(t, http.StatusOK, testAccPutAppConfig(t, with(key, "tf-acc-synthetic-"+key)), key)
		require.Equal(t, "tf-acc-synthetic-"+key, testAccAppConfig(t)[key])
		omitted := with(key, "")
		delete(omitted, key)
		require.Equal(t, http.StatusOK, testAccPutAppConfig(t, omitted), key)
		assert.Equal(t, "", testAccAppConfig(t)[key], "an omitted %s is cleared", key)
		require.Equal(t, http.StatusOK, testAccPutAppConfig(t, with(key, "tf-acc-synthetic-"+key)), key)
		require.Equal(t, http.StatusOK, testAccPutAppConfig(t, with(key, "")), key)
		assert.Equal(t, "", testAccAppConfig(t)[key], "an empty %s is cleared", key)
	}

	// Pocket ID 2.17.0 added autoCreateOidcClientSecret; older servers do
	// not report it.
	_, reported := original["autoCreateOidcClientSecret"]
	assert.Equal(t, testAccServerAtLeast(t, "2.17.0"), reported)
}
