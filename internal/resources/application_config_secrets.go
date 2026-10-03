package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// appConfigSecret is a secret setting with a write-only alternative input:
// <attribute>_wo (write-only, sensitive) and <attribute>_wo_version (a
// string the user changes to have the value sent again).
//
// While <attribute>_wo_version is set, the secret is in write-only mode: the
// plain attribute is null in plan and state, Read never stores what the
// server returns, and an update sends the server's current value back
// unchanged unless the version changed.
//
// Otherwise the plain attribute is tracked only when it already holds a
// value: configured through it, or written to state by an earlier provider
// version. A secret that was never configured (including after an import)
// stays null instead of copying the server's value into state.
type appConfigSecret struct {
	attribute, label string
	// plain, writeOnly and version select the three fields of the model.
	plain, writeOnly, version func(*applicationConfigModel) *types.String
	// server selects the field of the client configuration.
	server func(*client.ApplicationConfig) *string
}

var appConfigSecrets = []appConfigSecret{
	{
		attribute: "smtp_password", label: "SMTP password",
		plain:     func(m *applicationConfigModel) *types.String { return &m.SmtpPassword },
		writeOnly: func(m *applicationConfigModel) *types.String { return &m.SmtpPasswordWO },
		version:   func(m *applicationConfigModel) *types.String { return &m.SmtpPasswordWOVersion },
		server:    func(c *client.ApplicationConfig) *string { return &c.SmtpPassword },
	},
	{
		attribute: "ldap_bind_password", label: "LDAP bind password",
		plain:     func(m *applicationConfigModel) *types.String { return &m.LdapBindPassword },
		writeOnly: func(m *applicationConfigModel) *types.String { return &m.LdapBindPasswordWO },
		version:   func(m *applicationConfigModel) *types.String { return &m.LdapBindPasswordWOVersion },
		server:    func(c *client.ApplicationConfig) *string { return &c.LdapBindPassword },
	},
}

func (s appConfigSecret) writeOnlyAttribute() string { return s.attribute + "_wo" }
func (s appConfigSecret) versionAttribute() string   { return s.attribute + "_wo_version" }

// plainDescription is appended to the plain attribute's description.
func (s appConfigSecret) plainDescription() string {
	return fmt.Sprintf(" It is stored in state (marked sensitive) only while you set it with this attribute, or when state written by an earlier provider version already holds it; otherwise it is null, also after an import. To keep it out of plan and state, use `%s` instead. Conflicts with `%s`.", s.writeOnlyAttribute(), s.writeOnlyAttribute())
}

func (s appConfigSecret) schemaAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		s.writeOnlyAttribute(): schema.StringAttribute{
			Description: fmt.Sprintf("The %s as a write-only value: Terraform never stores it in plan or state, and %s is then null in state. "+
				"It is sent when the resource is created and whenever `%s` changes; any other update keeps the %s Pocket ID holds. "+
				"Requires `%s`. Needs Terraform 1.11 or later, or OpenTofu 1.11 or later. Conflicts with `%s`.",
				s.label, "`"+s.attribute+"`", s.versionAttribute(), s.label, s.versionAttribute(), s.attribute),
			Optional:  true,
			Sensitive: true,
			WriteOnly: true,
			Validators: []validator.String{
				stringvalidator.AlsoRequires(path.MatchRoot(s.versionAttribute())),
			},
		},
		s.versionAttribute(): schema.StringAttribute{
			Description: fmt.Sprintf("Any string. Change it to send `%s` to Pocket ID again. Requires `%s`.", s.writeOnlyAttribute(), s.writeOnlyAttribute()),
			Optional:    true,
			Validators: []validator.String{
				stringvalidator.AlsoRequires(path.MatchRoot(s.writeOnlyAttribute())),
			},
		},
	}
}

// plainValidator refuses the plain attribute next to the write-only inputs.
func (s appConfigSecret) plainValidator() validator.String {
	return stringvalidator.ConflictsWith(path.MatchRoot(s.writeOnlyAttribute()), path.MatchRoot(s.versionAttribute()))
}

// writeOnlyMode reports whether m (configuration, plan or state) uses the
// write-only input.
func (s appConfigSecret) writeOnlyMode(m *applicationConfigModel) bool {
	return !s.version(m).IsNull()
}

// planWriteOnlyMode makes the plain attribute null in a plan that uses the
// write-only input (it may hold the value from state).
func (s appConfigSecret) planWriteOnlyMode(ctx context.Context, config tfsdk.Config, plan *tfsdk.Plan) diag.Diagnostics {
	var version types.String
	diags := config.GetAttribute(ctx, path.Root(s.versionAttribute()), &version)
	if diags.HasError() || version.IsNull() {
		return diags
	}
	diags.Append(plan.SetAttribute(ctx, path.Root(s.attribute), types.StringNull())...)
	return diags
}

// valueToSend returns the write-only value to send, if one must be sent: on
// create, and on an update that changes the version. prior is nil on create.
func (s appConfigSecret) valueToSend(config, plan, prior *applicationConfigModel) (*string, error) {
	if !s.writeOnlyMode(config) {
		return nil, nil
	}
	if prior != nil && s.version(plan).Equal(*s.version(prior)) {
		return nil, nil
	}
	value := *s.writeOnly(config)
	if value.IsNull() || value.IsUnknown() {
		return nil, fmt.Errorf("%s must be set when %s is set or changed", s.writeOnlyAttribute(), s.versionAttribute())
	}
	v := value.ValueString()
	return &v, nil
}

// forState is the plain attribute's value in state after a read or an
// update. prior is the value in state (Read) or the planned value (apply);
// mode is the model whose version decides write-only mode.
func (s appConfigSecret) forState(prior types.String, mode *applicationConfigModel, server string) types.String {
	if s.writeOnlyMode(mode) || prior.IsNull() || prior.IsUnknown() {
		return types.StringNull()
	}
	return types.StringValue(server)
}

// appConfigWriteOnlyValues returns the write-only values an apply must send,
// keyed by attribute. prior is nil on create.
func appConfigWriteOnlyValues(config, plan, prior *applicationConfigModel) (map[string]string, error) {
	values := map[string]string{}
	for _, secret := range appConfigSecrets {
		value, err := secret.valueToSend(config, plan, prior)
		if err != nil {
			return nil, err
		}
		if value != nil {
			values[secret.attribute] = *value
		}
	}
	return values, nil
}
