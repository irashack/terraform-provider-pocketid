package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// The settings table, the resource model and the client struct describe the
// same keys: every setting has a model field and a client field, and every
// model field other than id and the write-only inputs has a setting.
func TestAppConfigSettingsMatchModelAndClient(t *testing.T) {
	modelTags := map[string]bool{}
	modelType := reflect.TypeOf(applicationConfigModel{})
	for i := 0; i < modelType.NumField(); i++ {
		modelTags[modelType.Field(i).Tag.Get("tfsdk")] = true
	}
	clientKeys := map[string]bool{}
	clientType := reflect.TypeOf(client.ApplicationConfig{})
	for i := 0; i < clientType.NumField(); i++ {
		key, _, _ := strings.Cut(clientType.Field(i).Tag.Get("json"), ",")
		if key != "-" {
			clientKeys[key] = true
		}
	}

	seenAttributes, seenKeys := map[string]bool{}, map[string]bool{}
	for _, setting := range appConfigSettings {
		assert.False(t, seenAttributes[setting.attribute], "duplicate attribute %s", setting.attribute)
		assert.False(t, seenKeys[setting.key], "duplicate key %s", setting.key)
		seenAttributes[setting.attribute], seenKeys[setting.key] = true, true
		assert.True(t, modelTags[setting.attribute], "no model field for %s", setting.attribute)
		assert.True(t, clientKeys[setting.key], "no client field for %s", setting.key)
		if setting.required {
			assert.NotEmpty(t, setting.defaultValue, "%s is required, so Pocket ID has a non-empty default", setting.attribute)
		}
	}
	for tag := range modelTags {
		if tag == "id" || strings.HasSuffix(tag, "_wo") || strings.HasSuffix(tag, "_wo_version") {
			continue
		}
		assert.True(t, seenAttributes[tag], "model field %s has no setting", tag)
	}
	// Pocket ID 2.17.0's AppConfigUpdateDto has 48 keys.
	assert.Len(t, appConfigSettings, 48)
}

func TestAppConfigSettingRules(t *testing.T) {
	settings := map[string]appConfigSetting{}
	for _, setting := range appConfigSettings {
		settings[setting.attribute] = setting
	}
	cases := []struct {
		attribute, value string
		ok               bool
	}{
		{"app_name", "", false},
		{"app_name", "x", true},
		{"app_name", strings.Repeat("é", 30), true},
		{"app_name", strings.Repeat("é", 31), false},

		{"session_duration", "", false},
		{"session_duration", "60", true},
		{"session_duration", "+60", true},
		{"session_duration", "0", false},
		{"session_duration", "-5", false},
		{"session_duration", "1.5", false},
		{"session_duration", "sixty", false},
		{"session_duration", "153722867", true},
		{"session_duration", "153722868", false},

		{"home_page_url", "", false},
		{"home_page_url", "/settings/account", true},

		{"emails_verified", "true", true},
		{"emails_verified", "false", true},
		{"emails_verified", "True", false},
		{"emails_verified", "1", false},
		{"emails_verified", "", false},

		{"allow_user_signups", "withToken", true},
		{"allow_user_signups", "withtoken", false},

		{"signup_default_user_group_ids", "[]", true},
		{"signup_default_user_group_ids", `["0b9e0b1c-0000-4000-8000-000000000000"]`, true},
		{"signup_default_user_group_ids", "null", false},
		{"signup_default_user_group_ids", "[1]", false},
		{"signup_default_user_group_ids", "{}", false},
		{"signup_default_user_group_ids", "", false},

		{"signup_default_custom_claims", "[]", true},
		{"signup_default_custom_claims", `[{"key":"department","value":"it"}]`, true},
		{"signup_default_custom_claims", `{"department":"it"}`, false},
		{"signup_default_custom_claims", `[{"key":"department"}]`, false},
		{"signup_default_custom_claims", `[{"key":"department","value":1}]`, false},
		{"signup_default_custom_claims", `[null]`, false},
		{"signup_default_custom_claims", "", false},

		{"accent_color", "", false},
		{"accent_color", "default", true},
		{"accent_color", "#3b82f6", true},

		{"cimd_url_allowlist", "[]", true},
		{"cimd_url_allowlist", `["https://*.example.com/*"]`, true},
		{"cimd_url_allowlist", `["*"]`, true},
		{"cimd_url_allowlist", `["https://example.com:*/client.json"]`, true},
		{"cimd_url_allowlist", `["javascript:alert(1)"]`, false},
		{"cimd_url_allowlist", `["example.com/client.json"]`, false},
		{"cimd_url_allowlist", "null", false},
		{"cimd_url_allowlist", "", false},

		{"smtp_host", "", true},
		{"smtp_from", "", true},
		{"smtp_from", "no-reply@example.com", true},
		{"smtp_from", "Pocket ID <no-reply@example.com>", false},
		{"smtp_from", "no-reply", false},
		{"smtp_tls", "starttls", true},
		{"smtp_tls", "STARTTLS", false},
		{"smtp_tls", "", false},
		{"smtp_password", "", true},

		{"webauthn_user_verification", "preferred", true},
		{"webauthn_user_verification", "discouraged", false},
		{"webauthn_authenticator_attachment", "cross-platform", true},
		{"webauthn_authenticator_attachment", "roaming", false},

		{"ldap_url", "", true},
		{"ldap_user_search_filter", "", false},
		{"ldap_user_group_search_filter", "", false},
		{"ldap_attribute_user_display_name", "", false},
		{"ldap_attribute_group_member", "", false},
		{"ldap_attribute_group_name", "", true},
		{"ldap_soft_delete_users", "false", true},

		{"auto_create_oidc_client_secret", "false", true},
		{"auto_create_oidc_client_secret", "", false},
	}
	for _, tc := range cases {
		setting, ok := settings[tc.attribute]
		require.True(t, ok, tc.attribute)
		problem := setting.problem(tc.value)
		if tc.ok {
			assert.Empty(t, problem, "%s = %q", tc.attribute, tc.value)
		} else {
			assert.NotEmpty(t, problem, "%s = %q", tc.attribute, tc.value)
		}
	}

	// The message for a key whose empty value resets names the default.
	assert.Contains(t, settings["accent_color"].problem(""), `"default"`)
	assert.Contains(t, settings["signup_default_custom_claims"].problem(""), `"[]"`)
}

func appConfigTestSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	(&applicationConfigResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

// appConfigTestRaw builds a configuration or plan value with every attribute
// null except values.
func appConfigTestRaw(t *testing.T, s schema.Schema, values map[string]string) tftypes.Value {
	t.Helper()
	objectType, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	require.True(t, ok)
	attributes := map[string]tftypes.Value{}
	for name, attributeType := range objectType.AttributeTypes {
		attributes[name] = tftypes.NewValue(attributeType, nil)
	}
	for name, value := range values {
		attributes[name] = tftypes.NewValue(tftypes.String, value)
	}
	return tftypes.NewValue(objectType, attributes)
}

// Every setting attribute carries its validator, and an invalid configured
// value fails validation with the attribute's path.
func TestAppConfigSchemaValidators(t *testing.T) {
	s := appConfigTestSchema(t)
	for _, setting := range appConfigSettings {
		attribute, ok := s.Attributes[setting.attribute].(schema.StringAttribute)
		require.True(t, ok, setting.attribute)
		assert.True(t, attribute.Optional && attribute.Computed, setting.attribute)
		assert.Equal(t, setting.sensitive, attribute.Sensitive, setting.attribute)
		require.Len(t, attribute.Validators, 1, setting.attribute)
		if setting.defaultValue != "" {
			assert.Contains(t, attribute.Description, setting.defaultValue, setting.attribute)
		}
	}

	var resp validator.StringResponse
	s.Attributes["allow_user_signups"].(schema.StringAttribute).Validators[0].ValidateString(context.Background(), validator.StringRequest{
		Path:        path.Root("allow_user_signups"),
		ConfigValue: types.StringValue("everyone"),
	}, &resp)
	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), `"disabled", "withToken" or "open"`)

	resp = validator.StringResponse{}
	s.Attributes["allow_user_signups"].(schema.StringAttribute).Validators[0].ValidateString(context.Background(), validator.StringRequest{
		Path:        path.Root("allow_user_signups"),
		ConfigValue: types.StringUnknown(),
	}, &resp)
	assert.False(t, resp.Diagnostics.HasError(), "an unknown value is checked once it is known")
}

func versionServer(t *testing.T, version string, requests *int) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		require.Equal(t, "/api/version/current", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"currentVersion": version})
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	return c
}

// A setting a server version does not have is refused at plan time when it
// is configured; nothing is asked of the server when it is not configured.
func TestApplicationConfigModifyPlanRefusesNewerSettings(t *testing.T) {
	s := appConfigTestSchema(t)
	for _, tc := range []struct {
		version    string
		configured map[string]string
		refused    bool
		requests   int
	}{
		{"2.16.0", map[string]string{"auto_create_oidc_client_secret": "false"}, true, 1},
		{"2.17.0", map[string]string{"auto_create_oidc_client_secret": "false"}, false, 1},
		{"2.16.0", map[string]string{"app_name": "Fixture"}, false, 0},
	} {
		requests := 0
		r := &applicationConfigResource{client: versionServer(t, tc.version, &requests)}
		raw := appConfigTestRaw(t, s, tc.configured)
		resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: raw}}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Config: tfsdk.Config{Schema: s, Raw: raw},
			Plan:   tfsdk.Plan{Schema: s, Raw: raw},
			State:  tfsdk.State{Schema: s, Raw: tftypes.NewValue(raw.Type(), nil)},
		}, &resp)
		assert.Equal(t, tc.refused, resp.Diagnostics.HasError(), "%s %v: %v", tc.version, tc.configured, resp.Diagnostics)
		if tc.refused {
			assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "requires Pocket ID 2.17.0 or later; the server runs 2.16.0")
		}
		assert.Equal(t, tc.requests, requests)
	}
}

// Before any mutation, an update refuses a version-dependent setting the
// server did not report, even when the plan could not check the version.
func TestApplicationConfigApplyRefusesUnreportedSetting(t *testing.T) {
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			puts++
		}
		_ = json.NewEncoder(w).Encode([]client.AppConfigVariable{{Key: "appName", Value: "Fixture"}})
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	r := &applicationConfigResource{client: c}
	plan := &applicationConfigModel{}
	pv := reflect.ValueOf(plan).Elem()
	for i := 0; i < pv.NumField(); i++ {
		pv.Field(i).Set(reflect.ValueOf(types.StringNull()))
	}
	plan.AutoCreateOIDCClientSecret = types.StringValue("false")
	var diags diag.Diagnostics
	r.applyConfig(context.Background(), plan, &diags)
	require.True(t, diags.HasError())
	assert.Contains(t, diags.Errors()[0].Detail(), "requires Pocket ID 2.17.0 or later")
	assert.Contains(t, diags.Errors()[0].Detail(), "Nothing was changed")
	assert.Zero(t, puts)
}

// On 2.17.0 the setting is read into state and an explicit value is sent.
func TestApplicationConfigAutoCreateRoundTrip(t *testing.T) {
	trueValue := "true"
	current := &client.ApplicationConfig{AutoCreateOIDCClientSecret: &trueValue}
	var model applicationConfigModel
	applicationConfigToModel(current, &model)
	assert.Equal(t, types.StringValue("true"), model.AutoCreateOIDCClientSecret)
	applicationConfigToModel(&client.ApplicationConfig{}, &model)
	assert.True(t, model.AutoCreateOIDCClientSecret.IsNull(), "null when the server does not have the setting")

	plan := &applicationConfigModel{AutoCreateOIDCClientSecret: types.StringValue("false")}
	cfg := modelToApplicationConfig(plan, current)
	require.NotNil(t, cfg.AutoCreateOIDCClientSecret)
	assert.Equal(t, "false", *cfg.AutoCreateOIDCClientSecret)
	assert.Equal(t, "true", *current.AutoCreateOIDCClientSecret, "the current configuration is not modified")

	cfg = modelToApplicationConfig(&applicationConfigModel{AutoCreateOIDCClientSecret: types.StringNull()}, current)
	assert.Equal(t, "true", *cfg.AutoCreateOIDCClientSecret, "an unset value keeps the server's")
}
