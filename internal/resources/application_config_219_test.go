package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Pocket ID 2.19.0 replaced emailLoginNotificationEnabled and
// emailVerificationEnabled with emailLoginNotificationMode and
// emailVerification. Each attribute works only on the releases that have its
// key; a configured one on any other release is refused at plan time.
func TestApplicationConfigModifyPlanEmailSettingsByVersion(t *testing.T) {
	s := appConfigTestSchema(t)
	for _, tc := range []struct {
		version    string
		attribute  string
		value      string
		refusedMsg string
	}{
		{"2.18.0", "email_login_notification_mode", "always", "requires Pocket ID 2.19.0 or later; the server runs 2.18.0"},
		{"2.18.0", "email_verification", "optional", "requires Pocket ID 2.19.0 or later; the server runs 2.18.0"},
		{"2.19.0", "email_login_notification_mode", "always", ""},
		{"2.19.1", "email_verification", "optional", ""},
		{"2.14.0", "email_login_notification_enabled", "true", ""},
		{"2.18.0", "email_verification_enabled", "true", ""},
		{"2.19.0", "email_login_notification_enabled", "true", "was replaced in Pocket ID 2.19.0 and the server runs 2.19.0, which no longer has it"},
		{"2.19.1", "email_verification_enabled", "true", "was replaced in Pocket ID 2.19.0 and the server runs 2.19.1, which no longer has it"},
	} {
		requests := 0
		r := &applicationConfigResource{client: appConfigVersionServer(t, tc.version, &requests)}
		raw := appConfigTestRaw(t, s, map[string]string{tc.attribute: tc.value})
		resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: raw}}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Config: tfsdk.Config{Schema: s, Raw: raw},
			Plan:   tfsdk.Plan{Schema: s, Raw: raw},
			State:  tfsdk.State{Schema: s, Raw: tftypes.NewValue(raw.Type(), nil)},
		}, &resp)
		name := tc.version + " " + tc.attribute
		if tc.refusedMsg == "" {
			assert.False(t, resp.Diagnostics.HasError(), "%s: %v", name, resp.Diagnostics)
			continue
		}
		require.True(t, resp.Diagnostics.HasError(), name)
		detail := resp.Diagnostics.Errors()[0].Detail()
		assert.Contains(t, detail, tc.refusedMsg, name)
		if tc.attribute == "email_login_notification_enabled" {
			assert.Contains(t, detail, "use email_login_notification_mode instead", name)
		}
		if tc.attribute == "email_verification_enabled" {
			assert.Contains(t, detail, "use email_verification instead", name)
		}
	}
}

// The old attributes are deprecated; the new ones are not.
func TestApplicationConfigEmailSettingsDeprecation(t *testing.T) {
	s := appConfigTestSchema(t)
	for attribute, replacement := range map[string]string{
		"email_login_notification_enabled": "email_login_notification_mode",
		"email_verification_enabled":       "email_verification",
	} {
		deprecation := s.Attributes[attribute].GetDeprecationMessage()
		assert.Contains(t, deprecation, replacement, attribute)
		assert.Empty(t, s.Attributes[replacement].GetDeprecationMessage(), replacement)
	}
}

// The payload carries only the keys the server reported, whatever is merged
// in, and a configured value overrides the server's.
func TestApplicationConfigEmailSettingsMerge(t *testing.T) {
	str := func(v string) *string { return &v }

	t.Run("2.19.0 or later keeps the server's modes and sends no old key", func(t *testing.T) {
		current := &client.ApplicationConfig{EmailLoginNotificationMode: str("always"), EmailVerification: str("optional")}
		body := modelToApplicationConfig(appConfigNullModel(), current).Values()
		assert.Equal(t, "always", body["emailLoginNotificationMode"])
		assert.Equal(t, "optional", body["emailVerification"])
		assert.NotContains(t, body, "emailLoginNotificationEnabled")
		assert.NotContains(t, body, "emailVerificationEnabled")
	})

	t.Run("2.19.0 or later: a configured mode wins", func(t *testing.T) {
		current := &client.ApplicationConfig{EmailLoginNotificationMode: str("always"), EmailVerification: str("optional")}
		plan := appConfigNullModel()
		plan.EmailLoginNotificationMode = types.StringValue("ipAndUserAgent")
		plan.EmailVerification = types.StringValue("disabled")
		body := modelToApplicationConfig(plan, current).Values()
		assert.Equal(t, "ipAndUserAgent", body["emailLoginNotificationMode"])
		assert.Equal(t, "disabled", body["emailVerification"])
		assert.Equal(t, "always", *current.EmailLoginNotificationMode, "the current configuration is not modified")
	})

	t.Run("2.19.0 or later: an old value is never sent even if one reached the plan", func(t *testing.T) {
		current := &client.ApplicationConfig{EmailLoginNotificationMode: str("always"), EmailVerification: str("optional"),
			EmailLoginNotificationEnabled: str("true"), EmailVerificationEnabled: str("true")}
		body := modelToApplicationConfig(appConfigNullModel(), current).Values()
		assert.NotContains(t, body, "emailLoginNotificationEnabled")
		assert.NotContains(t, body, "emailVerificationEnabled")
	})

	t.Run("before 2.19.0 keeps the booleans and sends no new key", func(t *testing.T) {
		current := &client.ApplicationConfig{EmailLoginNotificationEnabled: str("true"), EmailVerificationEnabled: str("false")}
		plan := appConfigNullModel()
		plan.EmailVerificationEnabled = types.StringValue("true")
		body := modelToApplicationConfig(plan, current).Values()
		assert.Equal(t, "true", body["emailLoginNotificationEnabled"])
		assert.Equal(t, "true", body["emailVerificationEnabled"])
		assert.NotContains(t, body, "emailLoginNotificationMode")
		assert.NotContains(t, body, "emailVerification")
	})
}

func TestApplicationConfigApplyEmailSettings(t *testing.T) {
	apply := func(t *testing.T, version string, overrides map[string]string, configure func(*applicationConfigModel)) (diag.Diagnostics, map[string]string, *applicationConfigModel) {
		t.Helper()
		puts := 0
		c, sent := appConfigFakeServerFor(t, version, overrides, func(map[string]string) { puts++ })
		r := &applicationConfigResource{client: c}
		config, plan := appConfigNullModel(), appConfigNullModel()
		configure(config)
		configure(plan)
		plan.ID = types.StringUnknown()
		var diags diag.Diagnostics
		r.applyConfig(context.Background(), config, plan, nil, &diags)
		if diags.HasError() {
			assert.Zero(t, puts, "nothing is sent when the update is refused")
		}
		return diags, *sent, plan
	}

	t.Run("2.19.1 sends the new keys with the server's values and never the old", func(t *testing.T) {
		diags, sent, plan := apply(t, "2.19.1", map[string]string{"emailLoginNotificationMode": "always"}, func(m *applicationConfigModel) {
			m.AppName = types.StringValue("Renamed")
		})
		require.False(t, diags.HasError(), "%v", diags)
		assert.Equal(t, "always", sent["emailLoginNotificationMode"])
		assert.Equal(t, "disabled", sent["emailVerification"])
		assert.NotContains(t, sent, "emailLoginNotificationEnabled")
		assert.NotContains(t, sent, "emailVerificationEnabled")
		assert.True(t, plan.EmailLoginNotificationEnabled.IsNull() && plan.EmailVerificationEnabled.IsNull(), "the old attributes are null on 2.19.1")
		assert.Equal(t, "always", plan.EmailLoginNotificationMode.ValueString())
	})

	t.Run("2.19.1 sends a configured mode", func(t *testing.T) {
		diags, sent, plan := apply(t, "2.19.1", nil, func(m *applicationConfigModel) {
			m.EmailLoginNotificationMode = types.StringValue("browserRecognition")
			m.EmailVerification = types.StringValue("optional")
		})
		require.False(t, diags.HasError(), "%v", diags)
		assert.Equal(t, "browserRecognition", sent["emailLoginNotificationMode"])
		assert.Equal(t, "optional", sent["emailVerification"])
		assert.Equal(t, "optional", plan.EmailVerification.ValueString())
	})

	t.Run("2.19.1 refuses an old attribute and sends nothing", func(t *testing.T) {
		diags, _, _ := apply(t, "2.19.1", nil, func(m *applicationConfigModel) {
			m.EmailVerificationEnabled = types.StringValue("true")
		})
		require.True(t, diags.HasError())
		detail := diags.Errors()[0].Detail()
		assert.Contains(t, detail, "email_verification_enabled was replaced in Pocket ID 2.19.0")
		assert.Contains(t, detail, "use email_verification instead")
		assert.Contains(t, detail, "Nothing was changed")
	})

	t.Run("2.18.0 refuses a new attribute and sends nothing", func(t *testing.T) {
		diags, _, _ := apply(t, "2.18.0", nil, func(m *applicationConfigModel) {
			m.EmailLoginNotificationMode = types.StringValue("always")
		})
		require.True(t, diags.HasError())
		assert.Contains(t, diags.Errors()[0].Detail(), "email_login_notification_mode requires Pocket ID 2.19.0 or later")
		assert.Contains(t, diags.Errors()[0].Detail(), "Nothing was changed")
	})

	t.Run("2.18.0 keeps working with the booleans and sends no new key", func(t *testing.T) {
		diags, sent, plan := apply(t, "2.18.0", nil, func(m *applicationConfigModel) {
			m.EmailLoginNotificationEnabled = types.StringValue("true")
		})
		require.False(t, diags.HasError(), "%v", diags)
		assert.Equal(t, "true", sent["emailLoginNotificationEnabled"])
		assert.Equal(t, "false", sent["emailVerificationEnabled"])
		assert.NotContains(t, sent, "emailLoginNotificationMode")
		assert.NotContains(t, sent, "emailVerification")
		assert.True(t, plan.EmailLoginNotificationMode.IsNull() && plan.EmailVerification.IsNull(), "the new attributes are null before 2.19.0")
	})
}

func TestEmailVerificationRequiredProblems(t *testing.T) {
	str := func(v string) *string { return &v }
	assert.Empty(t, emailVerificationRequiredProblems(str("true"), str("false"), str("smtp.example.invalid"), str("id@example.invalid")))
	assert.Empty(t, emailVerificationRequiredProblems(nil, nil, nil, nil), "values that are not known are not checked")
	assert.Equal(t, []string{`require_user_email must be "true"`}, emailVerificationRequiredProblems(str("false"), str("false"), str("h"), str("f")))
	assert.Equal(t, []string{`emails_verified must be "false"`}, emailVerificationRequiredProblems(str("true"), str("true"), str("h"), str("f")))
	assert.Equal(t, []string{"smtp_host must be set", "smtp_from must be set"}, emailVerificationRequiredProblems(str("true"), str("false"), str(""), str("")))
}

// email_verification "required" is refused before any change when the merged
// configuration could not hold it, and accepted when it can.
func TestApplicationConfigApplyEmailVerificationRequired(t *testing.T) {
	run := func(t *testing.T, overrides map[string]string) (diag.Diagnostics, int) {
		t.Helper()
		puts := 0
		c, _ := appConfigFakeServerFor(t, "2.19.1", overrides, func(map[string]string) { puts++ })
		r := &applicationConfigResource{client: c}
		config, plan := appConfigNullModel(), appConfigNullModel()
		config.EmailVerification = types.StringValue("required")
		plan.EmailVerification = types.StringValue("required")
		plan.ID = types.StringUnknown()
		var diags diag.Diagnostics
		r.applyConfig(context.Background(), config, plan, nil, &diags)
		return diags, puts
	}

	diags, puts := run(t, nil)
	require.True(t, diags.HasError(), "the default configuration has no SMTP host or sender")
	assert.Contains(t, diags.Errors()[0].Detail(), "smtp_host must be set, smtp_from must be set")
	assert.Zero(t, puts)

	diags, puts = run(t, map[string]string{"emailsVerified": "true", "smtpHost": "smtp.example.invalid", "smtpFrom": "id@example.invalid"})
	require.True(t, diags.HasError())
	assert.Contains(t, diags.Errors()[0].Detail(), `emails_verified must be "false"`)
	assert.Zero(t, puts)

	diags, puts = run(t, map[string]string{"smtpHost": "smtp.example.invalid", "smtpFrom": "id@example.invalid"})
	require.False(t, diags.HasError(), "%v", diags)
	assert.Equal(t, 1, puts)
}

func TestApplicationConfigModifyPlanEmailVerificationRequired(t *testing.T) {
	s := appConfigTestSchema(t)
	for _, tc := range []struct {
		configured map[string]string
		refused    string
	}{
		{map[string]string{"email_verification": "required"}, ""},
		{map[string]string{"email_verification": "required", "emails_verified": "true"}, `emails_verified must be "false"`},
		{map[string]string{"email_verification": "required", "require_user_email": "false", "smtp_host": ""}, "smtp_host must be set"},
		{map[string]string{"email_verification": "optional", "emails_verified": "true"}, ""},
	} {
		raw := appConfigTestRaw(t, s, tc.configured)
		resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: raw}}
		(&applicationConfigResource{}).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Config: tfsdk.Config{Schema: s, Raw: raw},
			Plan:   tfsdk.Plan{Schema: s, Raw: raw},
			State:  tfsdk.State{Schema: s, Raw: tftypes.NewValue(raw.Type(), nil)},
		}, &resp)
		if tc.refused == "" {
			assert.False(t, resp.Diagnostics.HasError(), "%v: %v", tc.configured, resp.Diagnostics)
			continue
		}
		require.True(t, resp.Diagnostics.HasError(), "%v", tc.configured)
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), tc.refused)
	}
}

// State written by 3.1.x holds the booleans and no mode. Against a 2.19.0
// server a refresh drops the booleans to null and fills the modes with the
// server's migrated values, so a configuration that uses the modes at those
// values has nothing to change.
func TestApplicationConfigReadUpgradesFrom31xState(t *testing.T) {
	s := appConfigTestSchema(t)
	old := appConfigTestRaw(t, s, map[string]string{
		"id":                               applicationConfigID,
		"app_name":                         "Fixture",
		"email_login_notification_enabled": "true",
		"email_verification_enabled":       "false",
	})
	c, _ := appConfigFakeServerFor(t, "2.19.1", map[string]string{
		"appName": "Fixture", "emailLoginNotificationMode": "browserRecognition", "emailVerification": "disabled",
	}, nil)
	r := &applicationConfigResource{client: c}
	resp := resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: old}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: old}}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	var state applicationConfigModel
	require.False(t, resp.State.Get(context.Background(), &state).HasError())
	assert.True(t, state.EmailLoginNotificationEnabled.IsNull())
	assert.True(t, state.EmailVerificationEnabled.IsNull())
	assert.Equal(t, "browserRecognition", state.EmailLoginNotificationMode.ValueString())
	assert.Equal(t, "disabled", state.EmailVerification.ValueString())
	assert.Equal(t, "Fixture", state.AppName.ValueString())
}
