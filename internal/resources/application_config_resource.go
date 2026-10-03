package resources

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"golang.org/x/mod/semver"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// applicationConfigID is the fixed identifier used for the singleton
// application configuration resource.
const applicationConfigID = "application-configuration"

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &applicationConfigResource{}
	_ resource.ResourceWithConfigure   = &applicationConfigResource{}
	_ resource.ResourceWithImportState = &applicationConfigResource{}
	_ resource.ResourceWithModifyPlan  = &applicationConfigResource{}
)

func init() { register(NewApplicationConfigResource) }

// NewApplicationConfigResource is a helper function to simplify the provider implementation.
func NewApplicationConfigResource() resource.Resource {
	return &applicationConfigResource{}
}

// applicationConfigResource is the resource implementation.
type applicationConfigResource struct {
	client *client.Client
}

// applicationConfigModel maps the application configuration schema data. It is
// shared in shape with the data source model.
type applicationConfigModel struct {
	ID types.String `tfsdk:"id"`

	// General
	AppName                   types.String `tfsdk:"app_name"`
	SessionDuration           types.String `tfsdk:"session_duration"`
	HomePageURL               types.String `tfsdk:"home_page_url"`
	EmailsVerified            types.String `tfsdk:"emails_verified"`
	DisableAnimations         types.String `tfsdk:"disable_animations"`
	AllowOwnAccountEdit       types.String `tfsdk:"allow_own_account_edit"`
	AllowUserSignups          types.String `tfsdk:"allow_user_signups"`
	SignupDefaultUserGroupIDs types.String `tfsdk:"signup_default_user_group_ids"`
	SignupDefaultCustomClaims types.String `tfsdk:"signup_default_custom_claims"`
	AccentColor               types.String `tfsdk:"accent_color"`
	RequireUserEmail          types.String `tfsdk:"require_user_email"`

	WebauthnUserVerification        types.String `tfsdk:"webauthn_user_verification"`
	WebauthnAllowSyncedPasskeys     types.String `tfsdk:"webauthn_allow_synced_passkeys"`
	WebauthnAuthenticatorAttachment types.String `tfsdk:"webauthn_authenticator_attachment"`
	CIMDURLAllowlist                types.String `tfsdk:"cimd_url_allowlist"`

	// Email / SMTP
	SmtpHost     types.String `tfsdk:"smtp_host"`
	SmtpPort     types.String `tfsdk:"smtp_port"`
	SmtpFrom     types.String `tfsdk:"smtp_from"`
	SmtpUser     types.String `tfsdk:"smtp_user"`
	SmtpPassword types.String `tfsdk:"smtp_password"`
	// Write-only alternative to SmtpPassword; always null in plan and state.
	SmtpPasswordWO        types.String `tfsdk:"smtp_password_wo"`
	SmtpPasswordWOVersion types.String `tfsdk:"smtp_password_wo_version"`
	SmtpTls               types.String `tfsdk:"smtp_tls"`
	SmtpSkipCertVerify    types.String `tfsdk:"smtp_skip_cert_verify"`

	EmailOneTimeAccessAsAdminEnabled           types.String `tfsdk:"email_one_time_access_as_admin_enabled"`
	EmailOneTimeAccessAsUnauthenticatedEnabled types.String `tfsdk:"email_one_time_access_as_unauthenticated_enabled"`
	EmailLoginNotificationEnabled              types.String `tfsdk:"email_login_notification_enabled"`
	EmailApiKeyExpirationEnabled               types.String `tfsdk:"email_api_key_expiration_enabled"`
	EmailVerificationEnabled                   types.String `tfsdk:"email_verification_enabled"`

	// LDAP
	LdapEnabled      types.String `tfsdk:"ldap_enabled"`
	LdapUrl          types.String `tfsdk:"ldap_url"`
	LdapBindDn       types.String `tfsdk:"ldap_bind_dn"`
	LdapBindPassword types.String `tfsdk:"ldap_bind_password"`
	// Write-only alternative to LdapBindPassword; always null in plan and state.
	LdapBindPasswordWO                 types.String `tfsdk:"ldap_bind_password_wo"`
	LdapBindPasswordWOVersion          types.String `tfsdk:"ldap_bind_password_wo_version"`
	LdapBase                           types.String `tfsdk:"ldap_base"`
	LdapUserSearchFilter               types.String `tfsdk:"ldap_user_search_filter"`
	LdapUserGroupSearchFilter          types.String `tfsdk:"ldap_user_group_search_filter"`
	LdapSkipCertVerify                 types.String `tfsdk:"ldap_skip_cert_verify"`
	LdapAttributeUserUniqueIdentifier  types.String `tfsdk:"ldap_attribute_user_unique_identifier"`
	LdapAttributeUserUsername          types.String `tfsdk:"ldap_attribute_user_username"`
	LdapAttributeUserEmail             types.String `tfsdk:"ldap_attribute_user_email"`
	LdapAttributeUserFirstName         types.String `tfsdk:"ldap_attribute_user_first_name"`
	LdapAttributeUserLastName          types.String `tfsdk:"ldap_attribute_user_last_name"`
	LdapAttributeUserDisplayName       types.String `tfsdk:"ldap_attribute_user_display_name"`
	LdapAttributeUserProfilePicture    types.String `tfsdk:"ldap_attribute_user_profile_picture"`
	LdapAttributeGroupMember           types.String `tfsdk:"ldap_attribute_group_member"`
	LdapAttributeGroupUniqueIdentifier types.String `tfsdk:"ldap_attribute_group_unique_identifier"`
	LdapAttributeGroupName             types.String `tfsdk:"ldap_attribute_group_name"`
	LdapAdminGroupName                 types.String `tfsdk:"ldap_admin_group_name"`
	LdapSoftDeleteUsers                types.String `tfsdk:"ldap_soft_delete_users"`

	// OIDC (Pocket ID 2.17.0+)
	AutoCreateOIDCClientSecret types.String `tfsdk:"auto_create_oidc_client_secret"`
}

// applicationConfigToModel maps a client.ApplicationConfig onto the framework
// model, preserving the singleton ID.
func applicationConfigToModel(cfg *client.ApplicationConfig, m *applicationConfigModel) {
	m.ID = types.StringValue(applicationConfigID)

	m.AppName = types.StringValue(cfg.AppName)
	m.SessionDuration = types.StringValue(cfg.SessionDuration)
	m.HomePageURL = types.StringValue(cfg.HomePageURL)
	m.EmailsVerified = types.StringValue(cfg.EmailsVerified)
	m.DisableAnimations = types.StringValue(cfg.DisableAnimations)
	m.AllowOwnAccountEdit = types.StringValue(cfg.AllowOwnAccountEdit)
	m.AllowUserSignups = types.StringValue(cfg.AllowUserSignups)
	m.SignupDefaultUserGroupIDs = types.StringValue(cfg.SignupDefaultUserGroupIDs)
	m.SignupDefaultCustomClaims = types.StringValue(cfg.SignupDefaultCustomClaims)
	m.AccentColor = types.StringValue(cfg.AccentColor)
	m.RequireUserEmail = types.StringValue(cfg.RequireUserEmail)

	m.WebauthnUserVerification = types.StringValue(cfg.WebauthnUserVerification)
	m.WebauthnAllowSyncedPasskeys = types.StringValue(cfg.WebauthnAllowSyncedPasskeys)
	m.WebauthnAuthenticatorAttachment = types.StringValue(cfg.WebauthnAuthenticatorAttachment)
	m.CIMDURLAllowlist = types.StringValue(cfg.CIMDURLAllowlist)

	m.SmtpHost = types.StringValue(cfg.SmtpHost)
	m.SmtpPort = types.StringValue(cfg.SmtpPort)
	m.SmtpFrom = types.StringValue(cfg.SmtpFrom)
	m.SmtpUser = types.StringValue(cfg.SmtpUser)
	m.SmtpPassword = types.StringValue(cfg.SmtpPassword)
	m.SmtpTls = types.StringValue(cfg.SmtpTls)
	m.SmtpSkipCertVerify = types.StringValue(cfg.SmtpSkipCertVerify)

	m.EmailOneTimeAccessAsAdminEnabled = types.StringValue(cfg.EmailOneTimeAccessAsAdminEnabled)
	m.EmailOneTimeAccessAsUnauthenticatedEnabled = types.StringValue(cfg.EmailOneTimeAccessAsUnauthenticatedEnabled)
	m.EmailLoginNotificationEnabled = types.StringValue(cfg.EmailLoginNotificationEnabled)
	m.EmailApiKeyExpirationEnabled = types.StringValue(cfg.EmailApiKeyExpirationEnabled)
	m.EmailVerificationEnabled = types.StringValue(cfg.EmailVerificationEnabled)

	m.LdapEnabled = types.StringValue(cfg.LdapEnabled)
	m.LdapUrl = types.StringValue(cfg.LdapUrl)
	m.LdapBindDn = types.StringValue(cfg.LdapBindDn)
	m.LdapBindPassword = types.StringValue(cfg.LdapBindPassword)
	m.LdapBase = types.StringValue(cfg.LdapBase)
	m.LdapUserSearchFilter = types.StringValue(cfg.LdapUserSearchFilter)
	m.LdapUserGroupSearchFilter = types.StringValue(cfg.LdapUserGroupSearchFilter)
	m.LdapSkipCertVerify = types.StringValue(cfg.LdapSkipCertVerify)
	m.LdapAttributeUserUniqueIdentifier = types.StringValue(cfg.LdapAttributeUserUniqueIdentifier)
	m.LdapAttributeUserUsername = types.StringValue(cfg.LdapAttributeUserUsername)
	m.LdapAttributeUserEmail = types.StringValue(cfg.LdapAttributeUserEmail)
	m.LdapAttributeUserFirstName = types.StringValue(cfg.LdapAttributeUserFirstName)
	m.LdapAttributeUserLastName = types.StringValue(cfg.LdapAttributeUserLastName)
	m.LdapAttributeUserDisplayName = types.StringValue(cfg.LdapAttributeUserDisplayName)
	m.LdapAttributeUserProfilePicture = types.StringValue(cfg.LdapAttributeUserProfilePicture)
	m.LdapAttributeGroupMember = types.StringValue(cfg.LdapAttributeGroupMember)
	m.LdapAttributeGroupUniqueIdentifier = types.StringValue(cfg.LdapAttributeGroupUniqueIdentifier)
	m.LdapAttributeGroupName = types.StringValue(cfg.LdapAttributeGroupName)
	m.LdapAdminGroupName = types.StringValue(cfg.LdapAdminGroupName)
	m.LdapSoftDeleteUsers = types.StringValue(cfg.LdapSoftDeleteUsers)

	// Null when the server does not have the setting (before 2.17.0).
	m.AutoCreateOIDCClientSecret = types.StringPointerValue(cfg.AutoCreateOIDCClientSecret)
}

// mergedString returns the configured value if it is set (known and
// non-null), otherwise the current server-side value. This lets unset
// attributes inherit the existing configuration so that required server-side
// fields are never sent as empty values.
func mergedString(planned types.String, current string) string {
	if planned.IsNull() || planned.IsUnknown() {
		return current
	}
	return planned.ValueString()
}

// modelToApplicationConfig builds the client payload from the configuration,
// merging in the current server values for any attribute that is not set.
//
// The payload starts as a copy of the current server configuration because the
// update endpoint replaces the configuration in full: any field left at its
// zero value is reset server-side. Copying first means a field added to
// client.ApplicationConfig round-trips even before it is listed here.
func modelToApplicationConfig(plan *applicationConfigModel, current *client.ApplicationConfig) *client.ApplicationConfig {
	cfg := *current
	cfg.AppName = mergedString(plan.AppName, current.AppName)
	cfg.SessionDuration = mergedString(plan.SessionDuration, current.SessionDuration)
	cfg.HomePageURL = mergedString(plan.HomePageURL, current.HomePageURL)
	cfg.EmailsVerified = mergedString(plan.EmailsVerified, current.EmailsVerified)
	cfg.DisableAnimations = mergedString(plan.DisableAnimations, current.DisableAnimations)
	cfg.AllowOwnAccountEdit = mergedString(plan.AllowOwnAccountEdit, current.AllowOwnAccountEdit)
	cfg.AllowUserSignups = mergedString(plan.AllowUserSignups, current.AllowUserSignups)
	cfg.SignupDefaultUserGroupIDs = mergedString(plan.SignupDefaultUserGroupIDs, current.SignupDefaultUserGroupIDs)
	cfg.SignupDefaultCustomClaims = mergedString(plan.SignupDefaultCustomClaims, current.SignupDefaultCustomClaims)
	cfg.AccentColor = mergedString(plan.AccentColor, current.AccentColor)
	cfg.RequireUserEmail = mergedString(plan.RequireUserEmail, current.RequireUserEmail)

	cfg.WebauthnUserVerification = mergedString(plan.WebauthnUserVerification, current.WebauthnUserVerification)
	cfg.WebauthnAllowSyncedPasskeys = mergedString(plan.WebauthnAllowSyncedPasskeys, current.WebauthnAllowSyncedPasskeys)
	cfg.WebauthnAuthenticatorAttachment = mergedString(plan.WebauthnAuthenticatorAttachment, current.WebauthnAuthenticatorAttachment)
	cfg.CIMDURLAllowlist = mergedString(plan.CIMDURLAllowlist, current.CIMDURLAllowlist)

	cfg.SmtpHost = mergedString(plan.SmtpHost, current.SmtpHost)
	cfg.SmtpPort = mergedString(plan.SmtpPort, current.SmtpPort)
	cfg.SmtpFrom = mergedString(plan.SmtpFrom, current.SmtpFrom)
	cfg.SmtpUser = mergedString(plan.SmtpUser, current.SmtpUser)
	cfg.SmtpPassword = mergedString(plan.SmtpPassword, current.SmtpPassword)
	cfg.SmtpTls = mergedString(plan.SmtpTls, current.SmtpTls)
	cfg.SmtpSkipCertVerify = mergedString(plan.SmtpSkipCertVerify, current.SmtpSkipCertVerify)

	cfg.EmailOneTimeAccessAsAdminEnabled = mergedString(plan.EmailOneTimeAccessAsAdminEnabled, current.EmailOneTimeAccessAsAdminEnabled)
	cfg.EmailOneTimeAccessAsUnauthenticatedEnabled = mergedString(plan.EmailOneTimeAccessAsUnauthenticatedEnabled, current.EmailOneTimeAccessAsUnauthenticatedEnabled)
	cfg.EmailLoginNotificationEnabled = mergedString(plan.EmailLoginNotificationEnabled, current.EmailLoginNotificationEnabled)
	cfg.EmailApiKeyExpirationEnabled = mergedString(plan.EmailApiKeyExpirationEnabled, current.EmailApiKeyExpirationEnabled)
	cfg.EmailVerificationEnabled = mergedString(plan.EmailVerificationEnabled, current.EmailVerificationEnabled)

	cfg.LdapEnabled = mergedString(plan.LdapEnabled, current.LdapEnabled)
	cfg.LdapUrl = mergedString(plan.LdapUrl, current.LdapUrl)
	cfg.LdapBindDn = mergedString(plan.LdapBindDn, current.LdapBindDn)
	cfg.LdapBindPassword = mergedString(plan.LdapBindPassword, current.LdapBindPassword)
	cfg.LdapBase = mergedString(plan.LdapBase, current.LdapBase)
	cfg.LdapUserSearchFilter = mergedString(plan.LdapUserSearchFilter, current.LdapUserSearchFilter)
	cfg.LdapUserGroupSearchFilter = mergedString(plan.LdapUserGroupSearchFilter, current.LdapUserGroupSearchFilter)
	cfg.LdapSkipCertVerify = mergedString(plan.LdapSkipCertVerify, current.LdapSkipCertVerify)
	cfg.LdapAttributeUserUniqueIdentifier = mergedString(plan.LdapAttributeUserUniqueIdentifier, current.LdapAttributeUserUniqueIdentifier)
	cfg.LdapAttributeUserUsername = mergedString(plan.LdapAttributeUserUsername, current.LdapAttributeUserUsername)
	cfg.LdapAttributeUserEmail = mergedString(plan.LdapAttributeUserEmail, current.LdapAttributeUserEmail)
	cfg.LdapAttributeUserFirstName = mergedString(plan.LdapAttributeUserFirstName, current.LdapAttributeUserFirstName)
	cfg.LdapAttributeUserLastName = mergedString(plan.LdapAttributeUserLastName, current.LdapAttributeUserLastName)
	cfg.LdapAttributeUserDisplayName = mergedString(plan.LdapAttributeUserDisplayName, current.LdapAttributeUserDisplayName)
	cfg.LdapAttributeUserProfilePicture = mergedString(plan.LdapAttributeUserProfilePicture, current.LdapAttributeUserProfilePicture)
	cfg.LdapAttributeGroupMember = mergedString(plan.LdapAttributeGroupMember, current.LdapAttributeGroupMember)
	cfg.LdapAttributeGroupUniqueIdentifier = mergedString(plan.LdapAttributeGroupUniqueIdentifier, current.LdapAttributeGroupUniqueIdentifier)
	cfg.LdapAttributeGroupName = mergedString(plan.LdapAttributeGroupName, current.LdapAttributeGroupName)
	cfg.LdapAdminGroupName = mergedString(plan.LdapAdminGroupName, current.LdapAdminGroupName)
	cfg.LdapSoftDeleteUsers = mergedString(plan.LdapSoftDeleteUsers, current.LdapSoftDeleteUsers)

	// applyConfig refuses a planned value the server has no key for, so a
	// value is only ever sent to a server that reported the key.
	if !plan.AutoCreateOIDCClientSecret.IsNull() && !plan.AutoCreateOIDCClientSecret.IsUnknown() {
		value := plan.AutoCreateOIDCClientSecret.ValueString()
		cfg.AutoCreateOIDCClientSecret = &value
	}

	return &cfg
}

// appConfigModelValue returns the model's value for a setting's attribute.
func appConfigModelValue(m *applicationConfigModel, attribute string) types.String {
	value := reflect.ValueOf(m).Elem()
	for i := 0; i < value.NumField(); i++ {
		if value.Type().Field(i).Tag.Get("tfsdk") == attribute {
			if s, ok := value.Field(i).Interface().(types.String); ok {
				return s
			}
		}
	}
	return types.StringNull()
}

// unsupportedAppConfigSettings names the version-dependent settings that plan
// sets (known, not null) although the server did not report their keys.
func unsupportedAppConfigSettings(plan *applicationConfigModel, current *client.ApplicationConfig) []appConfigSetting {
	var unsupported []appConfigSetting
	for _, setting := range appConfigSettings {
		if setting.minVersion == "" {
			continue
		}
		value := appConfigModelValue(plan, setting.attribute)
		if value.IsNull() || value.IsUnknown() || appConfigKeyReported(current, setting.key) {
			continue
		}
		unsupported = append(unsupported, setting)
	}
	return unsupported
}

// Metadata returns the resource type name.
func (r *applicationConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_application_config"
}

// Schema defines the schema for the resource.
func (r *applicationConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Description: "Fixed identifier of the application configuration singleton.",
			Computed:    true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
	}
	for _, setting := range appConfigSettings {
		description := setting.description()
		validators := []validator.String{appConfigValueValidator{setting: setting}}
		for _, secret := range appConfigSecrets {
			if secret.attribute == setting.attribute {
				description += secret.plainDescription()
				validators = append(validators, secret.plainValidator())
			}
		}
		attributes[setting.attribute] = schema.StringAttribute{
			Description: description,
			Optional:    true,
			Computed:    true,
			Sensitive:   setting.sensitive,
			Validators:  validators,
			// Unset means "keep the server's value": an update of other
			// settings plans it unchanged instead of unknown.
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
	}
	for _, secret := range appConfigSecrets {
		for name, attribute := range secret.schemaAttributes() {
			attributes[name] = attribute
		}
	}
	resp.Schema = schema.Schema{
		Description:         "Manages the global application configuration of a Pocket-ID instance.",
		MarkdownDescription: "Manages the global application configuration of a Pocket-ID instance. This is a singleton resource: only one should exist per instance. Any attribute left unset inherits the current server-side value, and removing the resource from configuration leaves the live configuration untouched.",
		Attributes:          attributes,
	}
}

// Configure adds the provider configured client to the resource.
func (r *applicationConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = c
}

// ModifyPlan plans a secret's plain attribute as null while its write-only
// input is used, and refuses, at plan time, a version-dependent setting that
// is configured for a server older than the setting. Without a configured
// client (provider settings not yet known) that check runs before the update.
func (r *applicationConfigResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	for _, secret := range appConfigSecrets {
		resp.Diagnostics.Append(secret.planWriteOnlyMode(ctx, req.Config, &resp.Plan)...)
	}
	if resp.Diagnostics.HasError() || r.client == nil {
		return
	}
	version, versionRead := "", false
	for _, setting := range appConfigSettings {
		if setting.minVersion == "" {
			continue
		}
		var configured types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(setting.attribute), &configured)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if configured.IsNull() || configured.IsUnknown() {
			continue
		}
		if !versionRead {
			var err error
			version, err = r.client.GetCurrentVersion(ctx)
			if err != nil {
				resp.Diagnostics.AddAttributeError(path.Root(setting.attribute), "Could not check the Pocket ID version",
					fmt.Sprintf("%s requires Pocket ID %s or later, and the server's version could not be read: %s", setting.attribute, setting.minVersion, err))
				return
			}
			versionRead = true
		}
		// A server without the version endpoint (before 2.3.0) is older
		// than every version a setting needs.
		if version != "" && semver.Compare("v"+version, "v"+setting.minVersion) >= 0 {
			continue
		}
		running := version
		if running == "" {
			running = "a release before 2.3.0"
		}
		resp.Diagnostics.AddAttributeError(path.Root(setting.attribute), "Setting not supported by this Pocket ID",
			fmt.Sprintf("%s requires Pocket ID %s or later; the server runs %s, which does not have this setting. Remove %s from the configuration.", setting.attribute, setting.minVersion, running, setting.attribute))
	}
}

// applyConfig reads the server's current configuration, sends it back with
// the configured values over it, and writes the result into plan.
//
// Only attributes set in config are sent as planned. Every other key is sent
// with the value the server holds right now, so a setting changed outside
// Terraform after the plan was made is kept, not reverted by a change the plan
// did not show. Such a setting's planned value (from state) stays in state
// until the next refresh, as Terraform requires; the next refresh records the
// server's value, without a planned change because it is not configured.
//
// writeOnly holds the write-only secret values to send, by attribute (see
// appConfigWriteOnlyValues); every other secret is sent as configured through
// its plain attribute, or else as the server holds it.
func (r *applicationConfigResource) applyConfig(ctx context.Context, config, plan *applicationConfigModel, writeOnly map[string]string, diags *diag.Diagnostics) {
	current, err := r.client.GetApplicationConfig(ctx)
	if err != nil {
		diags.AddError(
			"Error reading application configuration",
			"Could not read current application configuration: "+err.Error(),
		)
		return
	}

	for _, setting := range unsupportedAppConfigSettings(config, current) {
		diags.AddAttributeError(
			path.Root(setting.attribute),
			"Setting not supported by this Pocket ID",
			fmt.Sprintf("%s requires Pocket ID %s or later, and this server does not have the setting. Nothing was changed. Remove %s from the configuration.", setting.attribute, setting.minVersion, setting.attribute),
		)
	}
	if diags.HasError() {
		return
	}

	payload := modelToApplicationConfig(config, current)
	for _, secret := range appConfigSecrets {
		if value, ok := writeOnly[secret.attribute]; ok {
			*secret.server(payload) = value
		}
	}

	tflog.Debug(ctx, "Updating application configuration")

	updated, err := r.client.UpdateApplicationConfig(ctx, payload)
	if err != nil {
		diags.AddError(
			"Error updating application configuration",
			"Could not update application configuration: "+err.Error(),
		)
		return
	}

	if unstored := unstoredAppConfigSettings(current, payload, updated); len(unstored) > 0 {
		diags.AddError(
			"Pocket ID stored different application settings",
			"Pocket ID accepted the update but did not store the value the provider sent for: "+strings.Join(unstored, ", ")+
				". Its configuration has changed; refresh to see what it holds, and check those values.",
		)
		return
	}

	var stored applicationConfigModel
	applicationConfigToModel(updated, &stored)
	planned := make([]types.String, len(appConfigSecrets))
	for i, secret := range appConfigSecrets {
		planned[i] = *secret.plain(plan)
	}
	fillUnplannedFromServer(plan, &stored)
	for i, secret := range appConfigSecrets {
		// A planned secret is what was sent and stored (or, unconfigured,
		// the value from state). One planned unknown was never configured
		// or tracked: it stays out of state rather than taking the
		// server's. One planned null uses the write-only input.
		if planned[i].IsUnknown() {
			planned[i] = types.StringNull()
		}
		*secret.plain(plan) = planned[i]
	}
}

// unstoredAppConfigSettings names the settings whose value the update changed
// but the server did not store as sent (by attribute, or by key for a setting
// this provider has no attribute for). No value is included: some are
// secrets.
func unstoredAppConfigSettings(current, sent, updated *client.ApplicationConfig) []string {
	before, request, after := current.Values(), sent.Values(), updated.Values()
	attributes := make(map[string]string, len(appConfigSettings))
	for _, setting := range appConfigSettings {
		attributes[setting.key] = setting.attribute
	}
	var unstored []string
	for key, value := range request {
		if value == before[key] || value == after[key] {
			continue
		}
		name, ok := attributes[key]
		if !ok {
			name = key
		}
		unstored = append(unstored, name)
	}
	sort.Strings(unstored)
	return unstored
}

// fillUnplannedFromServer sets every planned value that is unknown (or null)
// to the server's. A known planned value stays: for a configured attribute it
// is what was sent and stored, otherwise it is the value from state the plan
// showed.
func fillUnplannedFromServer(plan, stored *applicationConfigModel) {
	planValue := reflect.ValueOf(plan).Elem()
	storedValue := reflect.ValueOf(stored).Elem()
	for i := 0; i < planValue.NumField(); i++ {
		value, ok := planValue.Field(i).Interface().(types.String)
		if !ok || (!value.IsUnknown() && !value.IsNull()) {
			continue
		}
		planValue.Field(i).Set(storedValue.Field(i))
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *applicationConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config applicationConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	writeOnly, err := appConfigWriteOnlyValues(&config, &plan, nil)
	if err != nil {
		resp.Diagnostics.AddError("Missing write-only value", err.Error())
		return
	}
	r.applyConfig(ctx, &config, &plan, writeOnly, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *applicationConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state applicationConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading application configuration")

	cfg, err := r.client.GetApplicationConfig(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading application configuration",
			"Could not read application configuration: "+err.Error(),
		)
		return
	}

	prior := make([]types.String, len(appConfigSecrets))
	for i, secret := range appConfigSecrets {
		prior[i] = *secret.plain(&state)
	}
	applicationConfigToModel(cfg, &state)
	for i, secret := range appConfigSecrets {
		*secret.plain(&state) = secret.forState(prior[i], &state, *secret.server(cfg))
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *applicationConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, config, prior applicationConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}

	writeOnly, err := appConfigWriteOnlyValues(&config, &plan, &prior)
	if err != nil {
		resp.Diagnostics.AddError("Missing write-only value", err.Error())
		return
	}
	r.applyConfig(ctx, &config, &plan, writeOnly, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from state. The application configuration is a
// singleton that always exists, so the live configuration is left untouched.
func (r *applicationConfigResource) Delete(ctx context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	tflog.Info(ctx, "Removing application configuration from state; the live Pocket-ID configuration is left unchanged")
}

// ImportState imports the singleton application configuration into Terraform.
func (r *applicationConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
