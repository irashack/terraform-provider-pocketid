package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dunglas/go-urlpattern"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// appConfigRule is the format Pocket ID requires of a setting's value (the
// binding tags of dto.AppConfigUpdateDto, identical in 2.14.0 to 2.17.0). It
// returns "" when value is acceptable, otherwise what is wrong with it. It is
// never called with "": emptiness is decided by the setting itself.
type appConfigRule func(value string) string

// appConfigSetting is one attribute of pocketid_application_config and the
// rules Pocket ID applies to the key behind it.
type appConfigSetting struct {
	// attribute is the Terraform attribute name; key the API's JSON key.
	attribute, key string
	// about says what the setting does, as a sentence.
	about string
	// format describes the accepted values for the documentation; empty for
	// free text.
	format string
	// rule checks a non-empty value; nil accepts any text.
	rule appConfigRule
	// defaultValue is Pocket ID's default for the key (getDefaultConfig in
	// appconfig/model.go). An empty string sent for a key that is not
	// required resets it to this value.
	defaultValue string
	// required: the update DTO has binding:"required", so Pocket ID refuses
	// an empty value with HTTP 400.
	required  bool
	sensitive bool
	// minVersion is the first Pocket ID release that has the key; empty for
	// every supported release.
	minVersion string
}

func appConfigOneOf(values ...string) appConfigRule {
	return func(value string) string {
		for _, allowed := range values {
			if value == allowed {
				return ""
			}
		}
		return "must be one of " + appConfigQuotedList(values) + " (case-sensitive)"
	}
}

func appConfigQuotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	if len(quoted) == 2 {
		return quoted[0] + " or " + quoted[1]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

// appConfigBoolean is Pocket ID's boolean_string: exactly "true" or "false".
var appConfigBoolean = appConfigOneOf("true", "false")

// appConfigMaxSessionMinutes is the largest whole number of minutes a Go
// time.Duration holds. Pocket ID turns the value into minutes with
// time.Duration(value) * time.Minute (AsDurationMinutes), which wraps around to
// a negative duration above this.
const appConfigMaxSessionMinutes = int64(1<<63-1) / int64(60e9)

// appConfigSessionDuration accepts what Pocket ID's integer_string accepts
// (strconv.Atoi on its 64-bit builds: an optional sign and decimal digits)
// and, beyond the server's own rule, only a positive duration that does not
// overflow: a session that ends as soon as it starts would lock every user,
// administrators included, out of the web interface.
func appConfigSessionDuration(value string) string {
	minutes, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return "must be a whole number of minutes"
	}
	if minutes < 1 || minutes > appConfigMaxSessionMinutes {
		return fmt.Sprintf("must be between 1 and %d minutes", appConfigMaxSessionMinutes)
	}
	return ""
}

func appConfigLength(minimum, maximum int) appConfigRule {
	return func(value string) string {
		if n := utf8.RuneCountInString(value); n < minimum || n > maximum {
			return fmt.Sprintf("must be %d to %d characters long", minimum, maximum)
		}
		return ""
	}
}

// appConfigJSONStringArray is Pocket ID's json_string_array.
func appConfigJSONStringArray(value string) string {
	var items []string
	if json.Unmarshal([]byte(value), &items) != nil || items == nil {
		return `must be a JSON array of strings, such as ["id-1", "id-2"] or []`
	}
	return ""
}

// appConfigJSONCustomClaims is Pocket ID's json_custom_claims.
func appConfigJSONCustomClaims(value string) string {
	var claims []*struct {
		Key   *string `json:"key"`
		Value *string `json:"value"`
	}
	if json.Unmarshal([]byte(value), &claims) != nil || claims == nil {
		return `must be a JSON array of objects with string "key" and "value" properties, such as [{"key": "department", "value": "it"}] or []`
	}
	for _, claim := range claims {
		if claim == nil || claim.Key == nil || claim.Value == nil {
			return `must be a JSON array of objects with string "key" and "value" properties, such as [{"key": "department", "value": "it"}] or []`
		}
	}
	return ""
}

// appConfigCIMDAllowlist is Pocket ID's cimd_url_allowlist: a JSON array of
// callback URL patterns, each checked as utils.ValidateCallbackURLPattern
// does, URL-pattern compilation included.
func appConfigCIMDAllowlist(value string) string {
	var patterns []string
	if json.Unmarshal([]byte(value), &patterns) != nil || patterns == nil {
		return `must be a JSON array of URL patterns, such as ["https://*.example.com/*"] or []`
	}
	for _, pattern := range patterns {
		if problem := appConfigCallbackURLPatternProblem(pattern); problem != "" {
			return fmt.Sprintf("has an invalid URL pattern %q: %s", pattern, problem)
		}
	}
	return ""
}

// appConfigCallbackURLPatternProblem is utils.ValidateCallbackURLPattern of
// Pocket ID 2.15.0 to 2.17.0 (2.14.0 differs only in the version of
// go-urlpattern): it returns "" when the server accepts pattern.
func appConfigCallbackURLPatternProblem(pattern string) string {
	if pattern == "*" {
		return ""
	}
	pattern, _, _ = strings.Cut(pattern, "#")
	u, err := url.Parse(appConfigCallbackURLPatternForURLParse(pattern))
	if err != nil {
		return "it is not a URL"
	}
	if u.Scheme == "" {
		return "it must include a scheme"
	}
	switch strings.ToLower(u.Scheme) {
	case "javascript", "data":
		return "its scheme is not allowed"
	}
	if _, err := urlpattern.New(appConfigNormalizeURLPattern(pattern), "", nil); err != nil {
		return "it is not a valid URL pattern"
	}
	return ""
}

// The functions below are copied from Pocket ID 2.17.0
// (backend/internal/utils/callback_url_util.go), renamed, so the provider
// accepts exactly the patterns the server does. Copyright (c) 2024, Elias
// Schneider; BSD 2-Clause License. THIRD_PARTY_NOTICES.md at the repository
// root holds the full notice, conditions and disclaimer, and lists the
// functions it covers.

// appConfigCallbackURLPatternForURLParse is callbackURLPatternForURLParse: it
// makes a wildcard scheme or port parseable.
func appConfigCallbackURLPatternForURLParse(pattern string) string {
	if after, ok := strings.CutPrefix(pattern, "*://"); ok {
		pattern = "https://" + after
	}
	scheme, rest, ok := strings.Cut(pattern, "://")
	if !ok {
		return pattern
	}
	authority := rest
	suffix := ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
		suffix = rest[i:]
	}
	userinfo := ""
	hostport := authority
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		userinfo = authority[:i+1]
		hostport = authority[i+1:]
	}
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end == -1 {
			return pattern
		}
		if len(hostport) > end+1 && hostport[end+1] == ':' && strings.Contains(hostport[end+2:], "*") {
			hostport = hostport[:end+2] + "443"
		}
	} else if i := strings.LastIndex(hostport, ":"); i >= 0 && strings.Contains(hostport[i+1:], "*") {
		hostport = hostport[:i+1] + "443"
	}
	return scheme + "://" + userinfo + hostport + suffix
}

// appConfigNormalizeURLPattern is normalizeToURLPatternStandard: it turns *
// and ** wildcards into urlpattern syntax and escapes literal colons.
func appConfigNormalizeURLPattern(pattern string) string {
	patternBase, patternPath := appConfigURLPatternExtractPath(pattern)
	var result strings.Builder
	result.Grow(len(pattern) + 5)
	appConfigURLPatternWriteBase(&result, patternBase)
	appConfigURLPatternWritePath(&result, patternPath)
	return result.String()
}

// appConfigURLPatternWriteBase is writeNormalizedBase.
func appConfigURLPatternWriteBase(result *strings.Builder, patternBase string) {
	// 0 = scheme, 1 = host before any "[", 2 = inside an IPv6 literal,
	// 3 = after the host.
	var step int
	for i := 0; i < len(patternBase); i++ {
		switch step {
		case 0:
			if i > 3 && patternBase[i] == '/' && patternBase[i-1] == '/' && patternBase[i-2] == ':' {
				step = 1
			}
		case 1:
			switch patternBase[i] {
			case '/', ']':
				step = 3
			case '[':
				step = 2
			case ':':
				if !appConfigURLPatternIsPortSeparator(patternBase, i) {
					result.WriteByte('\\')
				}
			}
		case 2:
			if patternBase[i] == '/' || patternBase[i] == ']' || patternBase[i] == '[' {
				step = 3
			}
			switch patternBase[i] {
			case ':':
				result.WriteByte('\\')
			case '/', ']', '[':
				step = 3
			}
		}
		result.WriteByte(patternBase[i])
	}
}

// appConfigURLPatternWritePath is writeNormalizedPath.
func appConfigURLPatternWritePath(result *strings.Builder, patternPath string) {
	for i := 0; i < len(patternPath); i++ {
		if patternPath[i] == '*' {
			if i+1 < len(patternPath) && patternPath[i+1] == '*' {
				result.WriteString("*")
				i++
			} else {
				result.WriteString(":p")
				result.WriteString(strconv.Itoa(i))
			}
		} else {
			if patternPath[i] == ':' {
				result.WriteByte('\\')
			}
			result.WriteByte(patternPath[i])
		}
	}
}

// appConfigURLPatternIsPortSeparator is isPortSeparator.
func appConfigURLPatternIsPortSeparator(s string, i int) bool {
	return i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9'
}

// appConfigURLPatternExtractPath is extractPath.
func appConfigURLPatternExtractPath(url string) (base string, path string) {
	pathStart := -1
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			pathStart = i + 3 + j
		}
	} else {
		pathStart = strings.IndexByte(url, '/')
	}
	if pathStart >= 0 {
		return url[:pathStart], url[pathStart:]
	}
	return url, ""
}

// appConfigEmailPattern is the e-mail expression of go-playground/validator
// v10.30.5 (the version Pocket ID 2.17.0 uses for its "email" binding), MIT
// licensed, Copyright (c) 2015 Dean Karn.
var appConfigEmailPattern = regexp.MustCompile("^(?:(?:(?:(?:[a-zA-Z]|\\d|[!#\\$%&'\\*\\+\\-\\/=\\?\\^_`{\\|}~]|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])+(?:\\.([a-zA-Z]|\\d|[!#\\$%&'\\*\\+\\-\\/=\\?\\^_`{\\|}~]|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])+)*)|(?:(?:\\x22)(?:(?:(?:(?:\\x20|\\x09)*(?:\\x0d\\x0a))?(?:\\x20|\\x09)+)?(?:(?:[\\x01-\\x08\\x0b\\x0c\\x0e-\\x1f\\x7f]|\\x21|[\\x23-\\x5b]|[\\x5d-\\x7e]|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])|(?:(?:[\\x01-\\x09\\x0b\\x0c\\x0d-\\x7f]|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}]))))*(?:(?:(?:\\x20|\\x09)*(?:\\x0d\\x0a))?(\\x20|\\x09)+)?(?:\\x22))))@(?:(?:(?:[a-zA-Z]|\\d|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])|(?:(?:[a-zA-Z]|\\d|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])(?:[a-zA-Z]|\\d|-|\\.|~|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])*(?:[a-zA-Z]|\\d|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])))\\.)+(?:(?:[a-zA-Z]|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])|(?:(?:[a-zA-Z]|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])(?:[a-zA-Z]|\\d|-|\\.|~|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])*(?:[a-zA-Z]|[\\x{00A0}-\\x{D7FF}\\x{F900}-\\x{FDCF}\\x{FDF0}-\\x{FFEF}])))\\.?$")

// appConfigEmail is go-playground/validator's isEmail: net/mail must parse
// the value and the expression above must match all of it, so a display name
// ("Name <a@example.com>") is refused.
func appConfigEmail(value string) string {
	if _, err := mail.ParseAddress(value); err != nil || !appConfigEmailPattern.MatchString(value) {
		return "must be a plain e-mail address such as no-reply@example.com (no display name)"
	}
	return ""
}

const (
	appConfigBooleanFormat = `"true" or "false"`
	appConfigAutoCreateKey = "autoCreateOidcClientSecret"
)

// appConfigSettings lists every attribute of pocketid_application_config
// except id and the write-only inputs, with Pocket ID's rules for it (Pocket
// ID 2.17.0 dto/app_config_dto.go and appconfig/model.go; 2.14.0 to 2.16.0
// differ only by not having autoCreateOidcClientSecret).
var appConfigSettings = []appConfigSetting{
	{attribute: "app_name", key: "appName", about: "The name of the application.", format: "1 to 30 characters", rule: appConfigLength(1, 30), defaultValue: "Pocket ID", required: true},
	{attribute: "session_duration", key: "sessionDuration", about: "How long a sign-in session lasts, in minutes.", format: "a whole number of minutes, at least 1", rule: appConfigSessionDuration, defaultValue: "60", required: true},
	{attribute: "home_page_url", key: "homePageUrl", about: "Where Pocket ID sends users after they sign in to it directly.", format: "a URL or path", defaultValue: "/settings/account", required: true},
	{attribute: "emails_verified", key: "emailsVerified", about: "Whether user e-mail addresses are considered verified.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},
	{attribute: "disable_animations", key: "disableAnimations", about: "Whether to disable UI animations.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},
	{attribute: "allow_own_account_edit", key: "allowOwnAccountEdit", about: "Whether users can edit their own account details.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "true", required: true},
	{attribute: "allow_user_signups", key: "allowUserSignups", about: "Who can create an account.", format: `"disabled", "withToken" (with a signup token only) or "open"`, rule: appConfigOneOf("disabled", "withToken", "open"), defaultValue: "disabled", required: true},
	{attribute: "signup_default_user_group_ids", key: "signupDefaultUserGroupIDs", about: "The user groups every new user is added to.", format: `a JSON array of user group IDs, such as ["<group-id>"]; "[]" for none`, rule: appConfigJSONStringArray, defaultValue: "[]"},
	{attribute: "signup_default_custom_claims", key: "signupDefaultCustomClaims", about: "The custom claims every new user gets.", format: `a JSON array of objects with string "key" and "value" properties, such as [{"key":"department","value":"it"}]; "[]" for none`, rule: appConfigJSONCustomClaims, defaultValue: "[]"},
	{attribute: "accent_color", key: "accentColor", about: "The accent color of the UI.", format: `"default" or a CSS color`, defaultValue: "default"},
	{attribute: "require_user_email", key: "requireUserEmail", about: "Whether every user must have an e-mail address.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "true", required: true},

	{attribute: "webauthn_user_verification", key: "webauthnUserVerification", about: "Whether passkey sign-in requires user verification.", format: `"required" or "preferred"`, rule: appConfigOneOf("required", "preferred"), defaultValue: "required", required: true},
	{attribute: "webauthn_allow_synced_passkeys", key: "webauthnAllowSyncedPasskeys", about: "Whether passkeys synced between devices are allowed.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "true", required: true},
	{attribute: "webauthn_authenticator_attachment", key: "webauthnAuthenticatorAttachment", about: "Which authenticators can hold a new passkey.", format: `"any", "platform" or "cross-platform"`, rule: appConfigOneOf("any", "platform", "cross-platform"), defaultValue: "any", required: true},
	{attribute: "cimd_url_allowlist", key: "cimdUrlAllowlist", about: "The Client ID Metadata Document URLs Pocket ID accepts.", format: `a JSON array of URL patterns (the syntax of callback URLs, wildcards allowed), such as ["https://*.example.com/*"]; "[]" for none. Each pattern is checked as Pocket ID checks it`, rule: appConfigCIMDAllowlist, defaultValue: "[]"},

	{attribute: "smtp_host", key: "smtpHost", about: "The SMTP server's host name."},
	{attribute: "smtp_port", key: "smtpPort", about: "The SMTP server's port."},
	{attribute: "smtp_from", key: "smtpFrom", about: "The sender address of e-mail Pocket ID sends.", format: "a plain e-mail address, without a display name", rule: appConfigEmail},
	{attribute: "smtp_user", key: "smtpUser", about: "The SMTP user name."},
	{attribute: "smtp_password", key: "smtpPassword", about: "The SMTP password.", sensitive: true},
	{attribute: "smtp_tls", key: "smtpTls", about: "How the SMTP connection is secured.", format: `"none", "starttls" or "tls"`, rule: appConfigOneOf("none", "starttls", "tls"), defaultValue: "none", required: true},
	{attribute: "smtp_skip_cert_verify", key: "smtpSkipCertVerify", about: "Whether to skip verifying the SMTP server's certificate.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},

	{attribute: "email_one_time_access_as_admin_enabled", key: "emailOneTimeAccessAsAdminEnabled", about: "Whether administrators can e-mail users a one-time sign-in link.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},
	{attribute: "email_one_time_access_as_unauthenticated_enabled", key: "emailOneTimeAccessAsUnauthenticatedEnabled", about: "Whether users can request a one-time sign-in link by e-mail without signing in.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},
	{attribute: "email_login_notification_enabled", key: "emailLoginNotificationEnabled", about: "Whether users are e-mailed when they sign in from a new device.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},
	{attribute: "email_api_key_expiration_enabled", key: "emailApiKeyExpirationEnabled", about: "Whether users are e-mailed before an API key expires.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},
	{attribute: "email_verification_enabled", key: "emailVerificationEnabled", about: "Whether users must verify their e-mail address.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},

	{attribute: "ldap_enabled", key: "ldapEnabled", about: "Whether LDAP synchronization is enabled.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},
	{attribute: "ldap_url", key: "ldapUrl", about: "The LDAP server's URL."},
	{attribute: "ldap_bind_dn", key: "ldapBindDn", about: "The DN Pocket ID binds to the LDAP server as."},
	{attribute: "ldap_bind_password", key: "ldapBindPassword", about: "The LDAP bind password.", sensitive: true},
	{attribute: "ldap_base", key: "ldapBase", about: "The LDAP search base."},
	{attribute: "ldap_user_search_filter", key: "ldapUserSearchFilter", about: "The LDAP filter that selects users.", defaultValue: "(objectClass=person)"},
	{attribute: "ldap_user_group_search_filter", key: "ldapUserGroupSearchFilter", about: "The LDAP filter that selects groups.", defaultValue: "(objectClass=groupOfNames)"},
	{attribute: "ldap_skip_cert_verify", key: "ldapSkipCertVerify", about: "Whether to skip verifying the LDAP server's certificate.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "false", required: true},
	{attribute: "ldap_attribute_user_unique_identifier", key: "ldapAttributeUserUniqueIdentifier", about: "The LDAP attribute that uniquely identifies a user."},
	{attribute: "ldap_attribute_user_username", key: "ldapAttributeUserUsername", about: "The LDAP attribute holding a user's username."},
	{attribute: "ldap_attribute_user_email", key: "ldapAttributeUserEmail", about: "The LDAP attribute holding a user's e-mail address."},
	{attribute: "ldap_attribute_user_first_name", key: "ldapAttributeUserFirstName", about: "The LDAP attribute holding a user's first name."},
	{attribute: "ldap_attribute_user_last_name", key: "ldapAttributeUserLastName", about: "The LDAP attribute holding a user's last name."},
	{attribute: "ldap_attribute_user_display_name", key: "ldapAttributeUserDisplayName", about: "The LDAP attribute holding a user's display name.", defaultValue: "cn"},
	{attribute: "ldap_attribute_user_profile_picture", key: "ldapAttributeUserProfilePicture", about: "The LDAP attribute holding a user's profile picture."},
	{attribute: "ldap_attribute_group_member", key: "ldapAttributeGroupMember", about: "The LDAP attribute listing a group's members.", defaultValue: "member"},
	{attribute: "ldap_attribute_group_unique_identifier", key: "ldapAttributeGroupUniqueIdentifier", about: "The LDAP attribute that uniquely identifies a group."},
	{attribute: "ldap_attribute_group_name", key: "ldapAttributeGroupName", about: "The LDAP attribute holding a group's name."},
	{attribute: "ldap_admin_group_name", key: "ldapAdminGroupName", about: "The LDAP group whose members are Pocket ID administrators."},
	{attribute: "ldap_soft_delete_users", key: "ldapSoftDeleteUsers", about: "Whether users removed from LDAP are disabled instead of deleted.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "true", required: true},

	{attribute: "auto_create_oidc_client_secret", key: appConfigAutoCreateKey, about: "Whether Pocket ID creates a client secret of its own when a confidential OIDC client is created.", format: appConfigBooleanFormat, rule: appConfigBoolean, defaultValue: "true", required: true, minVersion: "2.17.0"},
}

// description is the attribute's documentation: what it does, the accepted
// values, the default, and the rule for an empty value.
func (s appConfigSetting) description() string {
	var b strings.Builder
	b.WriteString(s.about)
	if s.format != "" {
		b.WriteString(" Accepted values: " + s.format + ".")
	}
	switch {
	case s.required:
		fmt.Fprintf(&b, " Pocket ID's default is %q; an empty value is refused.", s.defaultValue)
	case s.defaultValue != "":
		fmt.Fprintf(&b, " Pocket ID's default is %q; an empty value is refused, because Pocket ID would store the default instead.", s.defaultValue)
	default:
		b.WriteString(" Empty by default.")
	}
	if s.minVersion != "" {
		fmt.Fprintf(&b, " Requires Pocket ID %s or later: on an older server this attribute is null and setting it is refused at plan time.", s.minVersion)
	}
	b.WriteString(" When omitted, the server's current value is kept.")
	return b.String()
}

// problem returns what is wrong with value for this setting, or "".
func (s appConfigSetting) problem(value string) string {
	if value == "" {
		switch {
		case s.required:
			return "must not be empty: Pocket ID requires a value"
		case s.defaultValue != "":
			return fmt.Sprintf("must not be empty: Pocket ID would store its default %q instead, so the plan would never match the server; set %q or omit the attribute", s.defaultValue, s.defaultValue)
		}
		return ""
	}
	if s.rule == nil {
		return ""
	}
	return s.rule(value)
}

// appConfigValueValidator applies a setting's rules at plan time.
type appConfigValueValidator struct {
	setting appConfigSetting
}

func (v appConfigValueValidator) Description(context.Context) string {
	if v.setting.format == "" {
		return "any text"
	}
	return v.setting.format
}

func (v appConfigValueValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v appConfigValueValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := v.setting.problem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid application configuration value", v.setting.attribute+" "+problem+".")
	}
}

// appConfigKeyReported reports whether the server reported key in cfg: a
// pointer field is nil when it did not, and Additional holds the keys
// without a field. A plain string field cannot tell, so it counts as
// reported (every supported release has those keys).
func appConfigKeyReported(cfg *client.ApplicationConfig, key string) bool {
	if _, ok := cfg.Additional[key]; ok {
		return true
	}
	value := reflect.ValueOf(cfg).Elem()
	for i := 0; i < value.NumField(); i++ {
		tag, _, _ := strings.Cut(value.Type().Field(i).Tag.Get("json"), ",")
		if tag != key {
			continue
		}
		field := value.Field(i)
		return field.Kind() != reflect.Pointer || !field.IsNil()
	}
	return false
}
