package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// This boundary regression exercises the real GET/merge/PUT/response path.
// Native fixture tests additionally run the full pinned upstream validator.
func TestApplicationConfigSMTPPreservesSettings(t *testing.T) {
	for _, omitted := range []types.String{types.StringNull(), types.StringUnknown()} {
		t.Run(omitted.String(), func(t *testing.T) {
			current := &client.ApplicationConfig{}
			value := reflect.ValueOf(current).Elem()
			for i := 0; i < value.NumField(); i++ {
				existing := "existing-" + value.Type().Field(i).Name
				switch value.Field(i).Kind() {
				case reflect.Pointer:
					value.Field(i).Set(reflect.ValueOf(&existing))
				case reflect.String:
					value.Field(i).SetString(existing)
				}
			}
			current.WebauthnUserVerification = "required"
			current.WebauthnAllowSyncedPasskeys = "false"
			current.WebauthnAuthenticatorAttachment = "cross-platform"
			current.CIMDURLAllowlist = `["https://trusted.example.invalid/client.json"]`
			// Not the server default ("true"): it must round-trip, not reset.
			autoCreate := "false"
			current.AutoCreateOIDCClientSecret = &autoCreate
			body, err := json.Marshal(current)
			require.NoError(t, err)
			var expected map[string]string
			require.NoError(t, json.Unmarshal(body, &expected))
			vars := func(config map[string]string) []client.AppConfigVariable {
				result := make([]client.AppConfigVariable, 0, len(config))
				for key, value := range config {
					result = append(result, client.AppConfigVariable{Key: key, Value: value})
				}
				return result
			}
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					require.Equal(t, "/api/application-configuration/all", r.URL.Path)
					require.NoError(t, json.NewEncoder(w).Encode(vars(expected)))
					return
				}
				require.Equal(t, http.MethodPut, r.Method)
				require.Equal(t, "/api/application-configuration", r.URL.Path)
				var payload map[string]string
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				// v2.14 AppConfigUpdateDto requires all three WebAuthn strings.
				for _, key := range []string{"webauthnUserVerification", "webauthnAllowSyncedPasskeys", "webauthnAuthenticatorAttachment"} {
					if payload[key] == "" {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
				}
				// v2.17 adds autoCreateOidcClientSecret, required and boolean.
				if value := payload["autoCreateOidcClientSecret"]; value != "true" && value != "false" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				puts++
				want := make(map[string]string, len(expected))
				for key, value := range expected {
					want[key] = value
				}
				want["smtpHost"] = "smtp.example.invalid"
				require.Equal(t, want, payload, "SMTP update must preserve every unrelated field")
				require.NoError(t, json.NewEncoder(w).Encode(vars(payload)))
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
			require.NoError(t, err)
			r := &applicationConfigResource{client: c}
			plan := &applicationConfigModel{}
			pv := reflect.ValueOf(plan).Elem()
			for i := 0; i < pv.NumField(); i++ {
				pv.Field(i).Set(reflect.ValueOf(omitted))
			}
			plan.SmtpHost = types.StringValue("smtp.example.invalid")
			var diags diag.Diagnostics
			r.applyConfig(context.Background(), plan, plan, nil, &diags)
			require.False(t, diags.HasError(), "%v", diags)
			require.Equal(t, 1, puts)
			want := *current
			want.SmtpHost = "smtp.example.invalid"
			var wantModel applicationConfigModel
			applicationConfigToModel(&want, &wantModel)
			// Secrets that were not configured stay out of state; they are
			// still sent back unchanged (checked above).
			wantModel.SmtpPassword = types.StringNull()
			wantModel.LdapBindPassword = types.StringNull()
			require.Equal(t, wantModel, *plan)
		})
	}
}

func TestApplicationConfigExplicitValues(t *testing.T) {
	current := &client.ApplicationConfig{WebauthnUserVerification: "preferred", WebauthnAllowSyncedPasskeys: "true", WebauthnAuthenticatorAttachment: "any", CIMDURLAllowlist: `["https://old.example.invalid"]`, SmtpHost: "smtp.old.example.invalid"}
	plan := &applicationConfigModel{WebauthnUserVerification: types.StringValue("required"), WebauthnAllowSyncedPasskeys: types.StringValue("false"), WebauthnAuthenticatorAttachment: types.StringValue("platform"), CIMDURLAllowlist: types.StringValue("[]"), SmtpHost: types.StringValue("")}
	cfg := modelToApplicationConfig(plan, current)
	require.Equal(t, "required", cfg.WebauthnUserVerification)
	require.Equal(t, "false", cfg.WebauthnAllowSyncedPasskeys)
	require.Equal(t, "platform", cfg.WebauthnAuthenticatorAttachment)
	require.Equal(t, "[]", cfg.CIMDURLAllowlist)
	// smtp_host's default is empty, so "" is a valid explicit value.
	require.Empty(t, cfg.SmtpHost, "explicit empty must remain distinct from omitted")
}

// A server that reports, and requires, a setting this provider has never heard
// of (as 2.17.0 did with autoCreateOidcClientSecret) still accepts the
// provider's update, and the setting keeps its value. The fake server's
// key universe is its own, not derived from the provider's model.
func TestApplicationConfigUpdateKeepsUnknownServerKeys(t *testing.T) {
	const unknownKey, unknownValue = "aSettingNoProviderKnows", "custom-value"
	reported := map[string]string{
		"appName":                         "Fixture",
		"webauthnUserVerification":        "preferred",
		"webauthnAllowSyncedPasskeys":     "true",
		"webauthnAuthenticatorAttachment": "any",
		unknownKey:                        unknownValue,
	}
	vars := func(config map[string]string) []client.AppConfigVariable {
		result := make([]client.AppConfigVariable, 0, len(config))
		for key, value := range config {
			result = append(result, client.AppConfigVariable{Key: key, Value: value})
		}
		return result
	}
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			require.NoError(t, json.NewEncoder(w).Encode(vars(reported)))
			return
		}
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		// Like a newer Pocket ID's binding:"required": a missing or empty
		// value is refused with HTTP 400.
		if payload[unknownKey] == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"aSettingNoProviderKnows is required","code":"validation_error"}`))
			return
		}
		puts++
		require.Equal(t, unknownValue, payload[unknownKey], "the unknown setting must be sent back unchanged")
		require.NoError(t, json.NewEncoder(w).Encode(vars(payload)))
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
	plan.AppName = types.StringValue("Renamed")
	var diags diag.Diagnostics
	r.applyConfig(context.Background(), plan, plan, nil, &diags)
	require.False(t, diags.HasError(), "%v", diags)
	require.Equal(t, 1, puts)
	require.Equal(t, "Renamed", plan.AppName.ValueString())
}

// A server before 2.17.0 does not report autoCreateOidcClientSecret, and the
// update must not invent it.
func TestApplicationConfigOmitsUnreportedSettings(t *testing.T) {
	reported := []client.AppConfigVariable{
		{Key: "appName", Value: "Fixture"},
		{Key: "webauthnUserVerification", Value: "preferred"},
		{Key: "webauthnAllowSyncedPasskeys", Value: "true"},
		{Key: "webauthnAuthenticatorAttachment", Value: "any"},
	}
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			require.NoError(t, json.NewEncoder(w).Encode(reported))
			return
		}
		puts++
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.NotContains(t, payload, "autoCreateOidcClientSecret")
		require.Equal(t, "smtp.example.invalid", payload["smtpHost"])
		stored := make([]client.AppConfigVariable, 0, len(payload))
		for key, value := range payload {
			stored = append(stored, client.AppConfigVariable{Key: key, Value: value})
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
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
	plan.SmtpHost = types.StringValue("smtp.example.invalid")
	var diags diag.Diagnostics
	r.applyConfig(context.Background(), plan, plan, nil, &diags)
	require.False(t, diags.HasError(), "%v", diags)
	require.Equal(t, 1, puts)
}

// appConfigFakeServer serves GET /all from config and stores each PUT body,
// passed through store first, as the new configuration. It returns the
// client and a pointer to the last PUT body.
func appConfigFakeServer(t *testing.T, config map[string]string, store func(map[string]string)) (*client.Client, *map[string]string) {
	t.Helper()
	var lastPut map[string]string
	toVars := func(values map[string]string) []client.AppConfigVariable {
		vars := make([]client.AppConfigVariable, 0, len(values))
		for key, value := range values {
			vars = append(vars, client.AppConfigVariable{Key: key, Value: value})
		}
		return vars
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			require.NoError(t, json.NewEncoder(w).Encode(toVars(config)))
			return
		}
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		lastPut = make(map[string]string, len(payload))
		for key, value := range payload {
			lastPut[key] = value
		}
		if store != nil {
			store(payload)
		}
		config = payload
		require.NoError(t, json.NewEncoder(w).Encode(toVars(config)))
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	return c, &lastPut
}

func nullAppConfigModel() *applicationConfigModel {
	m := &applicationConfigModel{}
	value := reflect.ValueOf(m).Elem()
	for i := 0; i < value.NumField(); i++ {
		value.Field(i).Set(reflect.ValueOf(types.StringNull()))
	}
	return m
}

// An attribute that is not configured is planned at its value in state, so
// an update of another setting shows no change for it. If the server's value
// changed after that plan was made, the update sends the server's value (the
// plan did not show a change), and state keeps the planned value until the
// next refresh, as Terraform requires.
func TestApplicationConfigUnconfiguredKeepsServerValue(t *testing.T) {
	c, sent := appConfigFakeServer(t, map[string]string{"appName": "Fixture", "accentColor": "#changed-outside"}, nil)
	r := &applicationConfigResource{client: c}
	config := nullAppConfigModel()
	config.AppName = types.StringValue("Renamed")
	plan := nullAppConfigModel()
	plan.AppName = types.StringValue("Renamed")
	plan.AccentColor = types.StringValue("#from-state")
	plan.ID = types.StringUnknown()
	plan.SmtpHost = types.StringUnknown()

	var diags diag.Diagnostics
	r.applyConfig(context.Background(), config, plan, nil, &diags)
	require.False(t, diags.HasError(), "%v", diags)
	assert.Equal(t, "#changed-outside", (*sent)["accentColor"], "the server's value is sent, not the planned one")
	assert.Equal(t, "Renamed", (*sent)["appName"])
	assert.Equal(t, "#from-state", plan.AccentColor.ValueString(), "a known planned value is kept")
	assert.Equal(t, "", plan.SmtpHost.ValueString(), "an unknown planned value is the server's")
	assert.False(t, plan.SmtpHost.IsUnknown())
	assert.Equal(t, applicationConfigID, plan.ID.ValueString())
}

// When the server stores something other than what the provider sent for a
// setting the update changes, the apply fails and names the setting, never
// its value.
func TestApplicationConfigUnstoredValueFails(t *testing.T) {
	const secret = "synthetic-smtp-password"
	c, _ := appConfigFakeServer(t, map[string]string{"appName": "Fixture", "smtpPassword": "", "accentColor": "default"}, func(payload map[string]string) {
		payload["appName"] = "Coerced"
		payload["smtpPassword"] = ""
	})
	r := &applicationConfigResource{client: c}
	config := nullAppConfigModel()
	config.AppName = types.StringValue("Renamed")
	config.SmtpPassword = types.StringValue(secret)
	config.AccentColor = types.StringValue("default")
	plan := *config

	var diags diag.Diagnostics
	r.applyConfig(context.Background(), config, &plan, nil, &diags)
	require.True(t, diags.HasError())
	detail := diags.Errors()[0].Detail()
	assert.Contains(t, detail, "app_name, smtp_password.")
	assert.NotContains(t, detail, "accent_color", "an unchanged setting is not checked")
	assert.NotContains(t, detail, secret)
	assert.NotContains(t, detail, "Coerced")
}

func TestApplicationConfigSettingsPlanFromState(t *testing.T) {
	s := appConfigTestSchema(t)
	for _, setting := range appConfigSettings {
		attribute := s.Attributes[setting.attribute].(schema.StringAttribute)
		assert.Len(t, attribute.PlanModifiers, 1, setting.attribute)
	}
}
