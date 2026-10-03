package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Synthetic values; they only ever reach the fake server.
const (
	appConfigServerSMTPPassword = "synthetic-server-smtp"
	appConfigServerLDAPPassword = "synthetic-server-ldap"
	appConfigWOSMTPPassword     = "synthetic-wo-smtp"
	appConfigWOLDAPPassword     = "synthetic-wo-ldap"
)

func appConfigSecretsServerConfig() map[string]string {
	return map[string]string{"appName": "Fixture", "smtpPassword": appConfigServerSMTPPassword, "ldapBindPassword": appConfigServerLDAPPassword}
}

func appConfigTestState(t *testing.T, s schema.Schema, m *applicationConfigModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}
	require.False(t, state.Set(context.Background(), m).HasError())
	return state
}

func appConfigStateModel(t *testing.T, state tfsdk.State) applicationConfigModel {
	t.Helper()
	var m applicationConfigModel
	require.False(t, state.Get(context.Background(), &m).HasError())
	return m
}

// knownAppConfigModel is a model as Read would leave it, with every setting
// known.
func knownAppConfigModel() *applicationConfigModel {
	m := nullAppConfigModel()
	applicationConfigToModel(&client.ApplicationConfig{AppName: "Fixture"}, m)
	m.SmtpPassword, m.LdapBindPassword = types.StringNull(), types.StringNull()
	return m
}

func TestAppConfigSecretSchema(t *testing.T) {
	s := appConfigTestSchema(t)
	for _, secret := range appConfigSecrets {
		writeOnly := s.Attributes[secret.writeOnlyAttribute()].(schema.StringAttribute)
		assert.True(t, writeOnly.WriteOnly && writeOnly.Sensitive && writeOnly.Optional && !writeOnly.Computed, secret.attribute)
		assert.Contains(t, writeOnly.Description, "Terraform 1.11 or later, or OpenTofu 1.11 or later")
		version := s.Attributes[secret.versionAttribute()].(schema.StringAttribute)
		assert.True(t, version.Optional && !version.Computed && !version.WriteOnly, secret.attribute)
	}
}

// Read stores a secret only while it is tracked through the plain attribute:
// never in write-only mode, and never when state has no value for it (after
// an import, or when it was never configured).
func TestApplicationConfigReadSecrets(t *testing.T) {
	s := appConfigTestSchema(t)
	c, _ := appConfigFakeServer(t, appConfigSecretsServerConfig(), nil)
	r := &applicationConfigResource{client: c}
	for name, tc := range map[string]struct {
		prior    *applicationConfigModel
		wantSMTP types.String
	}{
		"plain attribute tracks the server's value": {func() *applicationConfigModel {
			m := knownAppConfigModel()
			m.SmtpPassword = types.StringValue("stale")
			return m
		}(), types.StringValue(appConfigServerSMTPPassword)},
		"never configured or imported stays null": {knownAppConfigModel(), types.StringNull()},
		"write-only mode stays null": {func() *applicationConfigModel {
			m := knownAppConfigModel()
			m.SmtpPassword = types.StringValue("left over")
			m.SmtpPasswordWOVersion = types.StringValue("1")
			return m
		}(), types.StringNull()},
	} {
		t.Run(name, func(t *testing.T) {
			resp := resource.ReadResponse{State: appConfigTestState(t, s, tc.prior)}
			r.Read(context.Background(), resource.ReadRequest{State: appConfigTestState(t, s, tc.prior)}, &resp)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			got := appConfigStateModel(t, resp.State)
			assert.Equal(t, tc.wantSMTP, got.SmtpPassword)
			assert.True(t, got.LdapBindPassword.IsNull(), "an untracked secret stays null")
			assert.True(t, got.SmtpPasswordWO.IsNull())
			assert.Equal(t, tc.prior.SmtpPasswordWOVersion, got.SmtpPasswordWOVersion)
		})
	}
}

// With the write-only input configured, the plain attribute is planned null
// even when state holds a value (moving from smtp_password to
// smtp_password_wo removes the password from state).
func TestApplicationConfigModifyPlanWriteOnly(t *testing.T) {
	s := appConfigTestSchema(t)
	r := &applicationConfigResource{}
	config := appConfigTestRaw(t, s, map[string]string{"smtp_password_wo": appConfigWOSMTPPassword, "smtp_password_wo_version": "1"})
	planned := knownAppConfigModel()
	planned.SmtpPassword = types.StringValue("from state")
	planned.LdapBindPassword = types.StringValue("ldap from state")
	planned.SmtpPasswordWOVersion = types.StringValue("1")
	plan := tfsdk.Plan(appConfigTestState(t, s, planned))
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Config: tfsdk.Config{Schema: s, Raw: config}, Plan: plan, State: appConfigTestState(t, s, planned)}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var got applicationConfigModel
	require.False(t, resp.Plan.Get(context.Background(), &got).HasError())
	assert.True(t, got.SmtpPassword.IsNull())
	assert.Equal(t, types.StringValue("ldap from state"), got.LdapBindPassword, "the other secret is untouched")
}

type appConfigSecretsCase struct {
	config map[string]string // configuration values (write-only ones included)
	plan   *applicationConfigModel
	prior  *applicationConfigModel // nil: create
}

// runAppConfigApply runs Create (no prior) or Update against c and returns
// the diagnostics and the new state.
func runAppConfigApply(t *testing.T, c *client.Client, tc appConfigSecretsCase) (diag.Diagnostics, tfsdk.State) {
	t.Helper()
	s := appConfigTestSchema(t)
	r := &applicationConfigResource{client: c}
	config := tfsdk.Config{Schema: s, Raw: appConfigTestRaw(t, s, tc.config)}
	plan := tfsdk.Plan(appConfigTestState(t, s, tc.plan))
	newState := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}
	if tc.prior == nil {
		resp := resource.CreateResponse{State: newState}
		r.Create(context.Background(), resource.CreateRequest{Config: config, Plan: plan}, &resp)
		return resp.Diagnostics, resp.State
	}
	resp := resource.UpdateResponse{State: newState}
	r.Update(context.Background(), resource.UpdateRequest{Config: config, Plan: plan, State: appConfigTestState(t, s, tc.prior)}, &resp)
	return resp.Diagnostics, resp.State
}

func runAppConfigUpdate(t *testing.T, c *client.Client, tc appConfigSecretsCase) diag.Diagnostics {
	t.Helper()
	diags, _ := runAppConfigApply(t, c, tc)
	return diags
}

func runAppConfigSecretsApply(t *testing.T, tc appConfigSecretsCase) (sent map[string]string, state applicationConfigModel) {
	t.Helper()
	c, lastPut := appConfigFakeServer(t, appConfigSecretsServerConfig(), nil)
	diags, newState := runAppConfigApply(t, c, tc)
	require.False(t, diags.HasError(), "%v", diags)
	return *lastPut, appConfigStateModel(t, newState)
}

func appConfigWriteOnlyPlan(smtpVersion, ldapVersion string) *applicationConfigModel {
	m := knownAppConfigModel()
	m.ID = types.StringValue(applicationConfigID)
	if smtpVersion != "" {
		m.SmtpPasswordWOVersion = types.StringValue(smtpVersion)
	}
	if ldapVersion != "" {
		m.LdapBindPasswordWOVersion = types.StringValue(ldapVersion)
	}
	return m
}

func TestApplicationConfigWriteOnlySecrets(t *testing.T) {
	woConfig := func(smtpVersion, ldapVersion string) map[string]string {
		return map[string]string{
			"smtp_password_wo": appConfigWOSMTPPassword, "smtp_password_wo_version": smtpVersion,
			"ldap_bind_password_wo": appConfigWOLDAPPassword, "ldap_bind_password_wo_version": ldapVersion,
		}
	}

	t.Run("create sends the write-only values", func(t *testing.T) {
		plan := appConfigWriteOnlyPlan("1", "1")
		plan.ID = types.StringUnknown()
		sent, state := runAppConfigSecretsApply(t, appConfigSecretsCase{config: woConfig("1", "1"), plan: plan})
		assert.Equal(t, appConfigWOSMTPPassword, sent["smtpPassword"])
		assert.Equal(t, appConfigWOLDAPPassword, sent["ldapBindPassword"])
		assert.True(t, state.SmtpPassword.IsNull())
		assert.True(t, state.LdapBindPassword.IsNull())
		assert.True(t, state.SmtpPasswordWO.IsNull(), "a write-only value never reaches state")
		assert.Equal(t, types.StringValue("1"), state.SmtpPasswordWOVersion)
	})

	t.Run("an update without a version change keeps the server's passwords", func(t *testing.T) {
		config := woConfig("1", "1")
		config["app_name"] = "Renamed"
		plan := appConfigWriteOnlyPlan("1", "1")
		plan.AppName = types.StringValue("Renamed")
		sent, state := runAppConfigSecretsApply(t, appConfigSecretsCase{config: config, plan: plan, prior: appConfigWriteOnlyPlan("1", "1")})
		assert.Equal(t, appConfigServerSMTPPassword, sent["smtpPassword"])
		assert.Equal(t, appConfigServerLDAPPassword, sent["ldapBindPassword"])
		assert.Equal(t, "Renamed", sent["appName"])
		assert.True(t, state.SmtpPassword.IsNull())
	})

	t.Run("a version change sends that value only", func(t *testing.T) {
		sent, _ := runAppConfigSecretsApply(t, appConfigSecretsCase{config: woConfig("2", "1"), plan: appConfigWriteOnlyPlan("2", "1"), prior: appConfigWriteOnlyPlan("1", "1")})
		assert.Equal(t, appConfigWOSMTPPassword, sent["smtpPassword"])
		assert.Equal(t, appConfigServerLDAPPassword, sent["ldapBindPassword"])
	})

	t.Run("moving from the plain attribute sends the write-only value and drops the plain one", func(t *testing.T) {
		prior := appConfigWriteOnlyPlan("", "")
		prior.SmtpPassword = types.StringValue(appConfigServerSMTPPassword)
		config := map[string]string{"smtp_password_wo": appConfigWOSMTPPassword, "smtp_password_wo_version": "1"}
		sent, state := runAppConfigSecretsApply(t, appConfigSecretsCase{config: config, plan: appConfigWriteOnlyPlan("1", ""), prior: prior})
		assert.Equal(t, appConfigWOSMTPPassword, sent["smtpPassword"])
		assert.Equal(t, appConfigServerLDAPPassword, sent["ldapBindPassword"], "an unconfigured secret is sent back unchanged")
		assert.True(t, state.SmtpPassword.IsNull())
	})

	t.Run("the plain attribute is sent and kept in state", func(t *testing.T) {
		plan := appConfigWriteOnlyPlan("", "")
		plan.SmtpPassword = types.StringValue("synthetic-plain")
		sent, state := runAppConfigSecretsApply(t, appConfigSecretsCase{config: map[string]string{"smtp_password": "synthetic-plain"}, plan: plan, prior: appConfigWriteOnlyPlan("", "")})
		assert.Equal(t, "synthetic-plain", sent["smtpPassword"])
		assert.Equal(t, types.StringValue("synthetic-plain"), state.SmtpPassword)
		assert.True(t, state.LdapBindPassword.IsNull())
	})
}

// appConfigRawServer answers GET /all with get(defaults) (raw JSON built from
// a complete configuration) and a PUT with put(payload), counting PUTs.
func appConfigRawServer(t *testing.T, get func(map[string]string) string, put func(map[string]string) string) (*client.Client, *int) {
	t.Helper()
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(get(appConfigServerDefaults(map[string]string{"smtpPassword": appConfigServerSMTPPassword, "ldapBindPassword": appConfigServerLDAPPassword}))))
			return
		}
		puts++
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		_, _ = w.Write([]byte(put(payload)))
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	return c, &puts
}

// appConfigVarsJSON encodes config as Pocket ID's key/value list, with key
// written as rawEntry instead when rawEntry is not empty ("-" leaves it out).
func appConfigVarsJSON(config map[string]string, key, rawEntry string) string {
	entries := make([]string, 0, len(config))
	for k, v := range config {
		if k == key && rawEntry != "" {
			if rawEntry != "-" {
				entries = append(entries, rawEntry)
			}
			continue
		}
		encoded, _ := json.Marshal(client.AppConfigVariable{Key: k, Value: v})
		entries = append(entries, string(encoded))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

// When the server's configuration does not give a password it would have to
// send back unchanged (left out, its value missing or null), the update is
// refused before anything is sent: sending "" would clear the password.
func TestApplicationConfigIncompleteReadSendsNothing(t *testing.T) {
	shapes := map[string]func(key string) string{
		"key left out":  func(string) string { return "-" },
		"value missing": func(key string) string { return `{"key":"` + key + `"}` },
		"value null":    func(key string) string { return `{"key":"` + key + `","value":null}` },
	}
	for _, key := range []string{"smtpPassword", "ldapBindPassword"} {
		for shapeName, shape := range shapes {
			for mode, tc := range map[string]appConfigSecretsCase{
				"not configured": {config: map[string]string{"app_name": "Renamed"}, plan: appConfigWriteOnlyPlan("", ""), prior: appConfigWriteOnlyPlan("", "")},
				"write-only, version unchanged": {
					config: map[string]string{"app_name": "Renamed", "smtp_password_wo": "synthetic-wo-smtp", "smtp_password_wo_version": "1", "ldap_bind_password_wo": "synthetic-wo-ldap", "ldap_bind_password_wo_version": "1"},
					plan:   appConfigWriteOnlyPlan("1", "1"), prior: appConfigWriteOnlyPlan("1", "1"),
				},
			} {
				t.Run(key+"/"+shapeName+"/"+mode, func(t *testing.T) {
					c, puts := appConfigRawServer(t,
						func(config map[string]string) string { return appConfigVarsJSON(config, key, shape(key)) },
						func(payload map[string]string) string { return appConfigVarsJSON(payload, "", "") })
					tc.plan.AppName = types.StringValue("Renamed")
					diags := runAppConfigUpdate(t, c, tc)
					require.True(t, diags.HasError(), "the update must be refused")
					assert.Zero(t, *puts, "nothing may be sent")
				})
			}
		}
	}
}

// A password that is configured, or a write-only value being sent, needs no
// value from the server; an explicit "" from the server is sent back as "".
func TestApplicationConfigIncompleteReadNotNeeded(t *testing.T) {
	var sent map[string]string
	echo := func(payload map[string]string) string { sent = payload; return appConfigVarsJSON(payload, "", "") }
	c, puts := appConfigRawServer(t, func(config map[string]string) string { return appConfigVarsJSON(config, "smtpPassword", "-") }, echo)
	tc := appConfigSecretsCase{config: map[string]string{"smtp_password_wo": "synthetic-wo-smtp", "smtp_password_wo_version": "2"}, plan: appConfigWriteOnlyPlan("2", ""), prior: appConfigWriteOnlyPlan("1", "")}
	require.False(t, runAppConfigUpdate(t, c, tc).HasError())
	assert.Equal(t, 1, *puts)
	assert.Equal(t, "synthetic-wo-smtp", sent["smtpPassword"])

	c, _ = appConfigRawServer(t, func(config map[string]string) string {
		config["ldapBindPassword"] = ""
		return appConfigVarsJSON(config, "", "")
	}, echo)
	plan := appConfigWriteOnlyPlan("", "")
	plan.AppName = types.StringValue("Renamed")
	require.False(t, runAppConfigUpdate(t, c, appConfigSecretsCase{config: map[string]string{"app_name": "Renamed"}, plan: plan, prior: appConfigWriteOnlyPlan("", "")}).HasError())
	assert.Equal(t, "", sent["ldapBindPassword"])
	assert.Equal(t, appConfigServerSMTPPassword, sent["smtpPassword"])
}

// A response that leaves out a setting the update sent is reported, not
// recorded as success.
func TestApplicationConfigIncompleteResponse(t *testing.T) {
	c, puts := appConfigRawServer(t,
		func(config map[string]string) string { return appConfigVarsJSON(config, "", "") },
		func(payload map[string]string) string { return appConfigVarsJSON(payload, "ldapBindPassword", "-") })
	plan := appConfigWriteOnlyPlan("", "")
	plan.AppName = types.StringValue("Renamed")
	diags := runAppConfigUpdate(t, c, appConfigSecretsCase{config: map[string]string{"app_name": "Renamed"}, plan: plan, prior: appConfigWriteOnlyPlan("", "")})
	require.True(t, diags.HasError())
	assert.Equal(t, 1, *puts)
	assert.Contains(t, diags.Errors()[0].Detail(), "did not include a value for: ldap_bind_password")
	assert.NotContains(t, diags.Errors()[0].Detail(), appConfigServerLDAPPassword)
}
