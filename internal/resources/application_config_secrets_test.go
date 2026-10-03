package resources

import (
	"context"
	"testing"

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
	serverSMTPPassword = "synthetic-server-smtp"
	serverLDAPPassword = "synthetic-server-ldap"
	woSMTPPassword     = "synthetic-wo-smtp"
	woLDAPPassword     = "synthetic-wo-ldap"
)

func secretsServerConfig() map[string]string {
	return map[string]string{"appName": "Fixture", "smtpPassword": serverSMTPPassword, "ldapBindPassword": serverLDAPPassword}
}

func appConfigTestState(t *testing.T, s schema.Schema, m *applicationConfigModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}
	require.False(t, state.Set(context.Background(), m).HasError())
	return state
}

func stateModel(t *testing.T, state tfsdk.State) applicationConfigModel {
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
	c, _ := appConfigFakeServer(t, secretsServerConfig(), nil)
	r := &applicationConfigResource{client: c}
	for name, tc := range map[string]struct {
		prior    *applicationConfigModel
		wantSMTP types.String
	}{
		"plain attribute tracks the server's value": {func() *applicationConfigModel {
			m := knownAppConfigModel()
			m.SmtpPassword = types.StringValue("stale")
			return m
		}(), types.StringValue(serverSMTPPassword)},
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
			got := stateModel(t, resp.State)
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
	config := appConfigTestRaw(t, s, map[string]string{"smtp_password_wo": woSMTPPassword, "smtp_password_wo_version": "1"})
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

type secretsApply struct {
	config map[string]string // configuration values (write-only ones included)
	plan   *applicationConfigModel
	prior  *applicationConfigModel // nil: create
}

func runSecretsApply(t *testing.T, tc secretsApply) (sent map[string]string, state applicationConfigModel) {
	t.Helper()
	s := appConfigTestSchema(t)
	c, lastPut := appConfigFakeServer(t, secretsServerConfig(), nil)
	r := &applicationConfigResource{client: c}
	config := tfsdk.Config{Schema: s, Raw: appConfigTestRaw(t, s, tc.config)}
	plan := tfsdk.Plan(appConfigTestState(t, s, tc.plan))
	newState := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}
	if tc.prior == nil {
		resp := resource.CreateResponse{State: newState}
		r.Create(context.Background(), resource.CreateRequest{Config: config, Plan: plan}, &resp)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		return *lastPut, stateModel(t, resp.State)
	}
	resp := resource.UpdateResponse{State: newState}
	r.Update(context.Background(), resource.UpdateRequest{Config: config, Plan: plan, State: appConfigTestState(t, s, tc.prior)}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	return *lastPut, stateModel(t, resp.State)
}

func writeOnlyPlan(smtpVersion, ldapVersion string) *applicationConfigModel {
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
			"smtp_password_wo": woSMTPPassword, "smtp_password_wo_version": smtpVersion,
			"ldap_bind_password_wo": woLDAPPassword, "ldap_bind_password_wo_version": ldapVersion,
		}
	}

	t.Run("create sends the write-only values", func(t *testing.T) {
		plan := writeOnlyPlan("1", "1")
		plan.ID = types.StringUnknown()
		sent, state := runSecretsApply(t, secretsApply{config: woConfig("1", "1"), plan: plan})
		assert.Equal(t, woSMTPPassword, sent["smtpPassword"])
		assert.Equal(t, woLDAPPassword, sent["ldapBindPassword"])
		assert.True(t, state.SmtpPassword.IsNull())
		assert.True(t, state.LdapBindPassword.IsNull())
		assert.True(t, state.SmtpPasswordWO.IsNull(), "a write-only value never reaches state")
		assert.Equal(t, types.StringValue("1"), state.SmtpPasswordWOVersion)
	})

	t.Run("an update without a version change keeps the server's passwords", func(t *testing.T) {
		config := woConfig("1", "1")
		config["app_name"] = "Renamed"
		plan := writeOnlyPlan("1", "1")
		plan.AppName = types.StringValue("Renamed")
		sent, state := runSecretsApply(t, secretsApply{config: config, plan: plan, prior: writeOnlyPlan("1", "1")})
		assert.Equal(t, serverSMTPPassword, sent["smtpPassword"])
		assert.Equal(t, serverLDAPPassword, sent["ldapBindPassword"])
		assert.Equal(t, "Renamed", sent["appName"])
		assert.True(t, state.SmtpPassword.IsNull())
	})

	t.Run("a version change sends that value only", func(t *testing.T) {
		sent, _ := runSecretsApply(t, secretsApply{config: woConfig("2", "1"), plan: writeOnlyPlan("2", "1"), prior: writeOnlyPlan("1", "1")})
		assert.Equal(t, woSMTPPassword, sent["smtpPassword"])
		assert.Equal(t, serverLDAPPassword, sent["ldapBindPassword"])
	})

	t.Run("moving from the plain attribute sends the write-only value and drops the plain one", func(t *testing.T) {
		prior := writeOnlyPlan("", "")
		prior.SmtpPassword = types.StringValue(serverSMTPPassword)
		config := map[string]string{"smtp_password_wo": woSMTPPassword, "smtp_password_wo_version": "1"}
		sent, state := runSecretsApply(t, secretsApply{config: config, plan: writeOnlyPlan("1", ""), prior: prior})
		assert.Equal(t, woSMTPPassword, sent["smtpPassword"])
		assert.Equal(t, serverLDAPPassword, sent["ldapBindPassword"], "an unconfigured secret is sent back unchanged")
		assert.True(t, state.SmtpPassword.IsNull())
	})

	t.Run("the plain attribute is sent and kept in state", func(t *testing.T) {
		plan := writeOnlyPlan("", "")
		plan.SmtpPassword = types.StringValue("synthetic-plain")
		sent, state := runSecretsApply(t, secretsApply{config: map[string]string{"smtp_password": "synthetic-plain"}, plan: plan, prior: writeOnlyPlan("", "")})
		assert.Equal(t, "synthetic-plain", sent["smtpPassword"])
		assert.Equal(t, types.StringValue("synthetic-plain"), state.SmtpPassword)
		assert.True(t, state.LdapBindPassword.IsNull())
	})
}
