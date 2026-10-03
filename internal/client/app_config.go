package client

import (
	"context"
	"reflect"
	"strings"
)

// ApplicationConfig represents the writable application configuration of a
// Pocket-ID instance. Every value is stored as a string by the API. The JSON
// tags match the keys returned by GET /api/application-configuration/all and
// the body expected by PUT /api/application-configuration.
type ApplicationConfig struct {
	// General
	AppName                   string `json:"appName"`
	SessionDuration           string `json:"sessionDuration"`
	HomePageURL               string `json:"homePageUrl"`
	EmailsVerified            string `json:"emailsVerified"`
	DisableAnimations         string `json:"disableAnimations"`
	AllowOwnAccountEdit       string `json:"allowOwnAccountEdit"`
	AllowUserSignups          string `json:"allowUserSignups"`
	SignupDefaultUserGroupIDs string `json:"signupDefaultUserGroupIDs"`
	SignupDefaultCustomClaims string `json:"signupDefaultCustomClaims"`
	AccentColor               string `json:"accentColor"`
	RequireUserEmail          string `json:"requireUserEmail"`

	WebauthnUserVerification        string `json:"webauthnUserVerification"`
	WebauthnAllowSyncedPasskeys     string `json:"webauthnAllowSyncedPasskeys"`
	WebauthnAuthenticatorAttachment string `json:"webauthnAuthenticatorAttachment"`
	CIMDURLAllowlist                string `json:"cimdUrlAllowlist"`

	// Email / SMTP
	SmtpHost           string `json:"smtpHost"`
	SmtpPort           string `json:"smtpPort"`
	SmtpFrom           string `json:"smtpFrom"`
	SmtpUser           string `json:"smtpUser"`
	SmtpPassword       string `json:"smtpPassword"`
	SmtpTls            string `json:"smtpTls"`
	SmtpSkipCertVerify string `json:"smtpSkipCertVerify"`

	EmailOneTimeAccessAsAdminEnabled           string `json:"emailOneTimeAccessAsAdminEnabled"`
	EmailOneTimeAccessAsUnauthenticatedEnabled string `json:"emailOneTimeAccessAsUnauthenticatedEnabled"`
	EmailLoginNotificationEnabled              string `json:"emailLoginNotificationEnabled"`
	EmailApiKeyExpirationEnabled               string `json:"emailApiKeyExpirationEnabled"`
	EmailVerificationEnabled                   string `json:"emailVerificationEnabled"`

	// LDAP
	LdapEnabled                        string `json:"ldapEnabled"`
	LdapUrl                            string `json:"ldapUrl"`
	LdapBindDn                         string `json:"ldapBindDn"`
	LdapBindPassword                   string `json:"ldapBindPassword"`
	LdapBase                           string `json:"ldapBase"`
	LdapUserSearchFilter               string `json:"ldapUserSearchFilter"`
	LdapUserGroupSearchFilter          string `json:"ldapUserGroupSearchFilter"`
	LdapSkipCertVerify                 string `json:"ldapSkipCertVerify"`
	LdapAttributeUserUniqueIdentifier  string `json:"ldapAttributeUserUniqueIdentifier"`
	LdapAttributeUserUsername          string `json:"ldapAttributeUserUsername"`
	LdapAttributeUserEmail             string `json:"ldapAttributeUserEmail"`
	LdapAttributeUserFirstName         string `json:"ldapAttributeUserFirstName"`
	LdapAttributeUserLastName          string `json:"ldapAttributeUserLastName"`
	LdapAttributeUserDisplayName       string `json:"ldapAttributeUserDisplayName"`
	LdapAttributeUserProfilePicture    string `json:"ldapAttributeUserProfilePicture"`
	LdapAttributeGroupMember           string `json:"ldapAttributeGroupMember"`
	LdapAttributeGroupUniqueIdentifier string `json:"ldapAttributeGroupUniqueIdentifier"`
	LdapAttributeGroupName             string `json:"ldapAttributeGroupName"`
	LdapAdminGroupName                 string `json:"ldapAdminGroupName"`
	LdapSoftDeleteUsers                string `json:"ldapSoftDeleteUsers"`

	// Settings below are carried through an update unchanged and are not
	// exposed as attributes. A pointer is nil when the server did not report
	// the key, so the update omits it instead of sending a value an older
	// server never had.

	// AutoCreateOIDCClientSecret (Pocket ID 2.17.0+) makes client creation
	// return a server-generated secret. Required by 2.17's update validator.
	AutoCreateOIDCClientSecret *string `json:"autoCreateOidcClientSecret,omitempty"`

	// Additional holds every key the server reported that has no field above,
	// with the value it reported. An update sends each of them back
	// unchanged, so a setting a newer Pocket ID adds (and may require) keeps
	// its value through an update by a provider that does not know it. A
	// named field always takes precedence over an entry here.
	Additional map[string]string `json:"-"`
}

// Values returns cfg as the key/value map an update sends: every key in
// Additional, then every named field over them. A pointer field that is nil
// (a key the server did not report) is left out.
func (cfg *ApplicationConfig) Values() map[string]string {
	body := make(map[string]string, len(cfg.Additional)+64)
	for key, value := range cfg.Additional {
		body[key] = value
	}
	cfgValue := reflect.ValueOf(cfg).Elem()
	cfgType := cfgValue.Type()
	for i := 0; i < cfgType.NumField(); i++ {
		key, _, _ := strings.Cut(cfgType.Field(i).Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		field := cfgValue.Field(i)
		switch field.Kind() {
		case reflect.Pointer:
			if field.IsNil() {
				continue
			}
			body[key] = field.Elem().String()
		case reflect.String:
			body[key] = field.String()
		}
	}
	return body
}

// AppConfigVariable represents a single key/value entry as returned by the
// application configuration endpoints.
type AppConfigVariable struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// appConfigVariablesToConfig converts the key/value variable slice returned by
// the application configuration endpoints into an ApplicationConfig struct.
// JSON tags also map WebAuthn and CIMD fields; absent older-server keys remain
// empty instead of inventing security defaults. A *string field stays nil when
// its key is absent, so it is omitted from an update to that server. Keys
// with no field go to Additional.
func appConfigVariablesToConfig(vars []AppConfigVariable) *ApplicationConfig {
	values := make(map[string]string, len(vars))
	for _, v := range vars {
		values[v.Key] = v.Value
	}

	cfg := &ApplicationConfig{}
	cfgValue := reflect.ValueOf(cfg).Elem()
	cfgType := cfgValue.Type()
	for i := 0; i < cfgType.NumField(); i++ {
		key, _, _ := strings.Cut(cfgType.Field(i).Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		value, ok := values[key]
		if !ok {
			continue
		}
		delete(values, key)
		field := cfgValue.Field(i)
		if field.Kind() == reflect.Pointer {
			field.Set(reflect.ValueOf(&value))
			continue
		}
		field.SetString(value)
	}
	if len(values) > 0 {
		cfg.Additional = values
	}

	return cfg
}

// GetApplicationConfig retrieves the full application configuration, including
// private values, from GET /api/application-configuration/all.
func (c *Client) GetApplicationConfig(ctx context.Context) (*ApplicationConfig, error) {
	body, err := c.doRequest(ctx, "GET", "/api/application-configuration/all", nil)
	if err != nil {
		return nil, err
	}

	var vars []AppConfigVariable
	if err := decodeResponse(body, &vars); err != nil {
		return nil, err
	}

	return appConfigVariablesToConfig(vars), nil
}

// UpdateApplicationConfig updates the application configuration via
// PUT /api/application-configuration. The provided config is sent in full,
// Additional included. Pocket ID replaces its whole configuration with the
// body: a key that is missing, or an empty string, resets that setting to its
// default.
func (c *Client) UpdateApplicationConfig(ctx context.Context, cfg *ApplicationConfig) (*ApplicationConfig, error) {
	body, err := c.doRequest(ctx, "PUT", "/api/application-configuration", cfg.Values())
	if err != nil {
		return nil, err
	}

	var vars []AppConfigVariable
	if err := decodeResult(body, &vars); err != nil {
		return nil, err
	}

	return appConfigVariablesToConfig(vars), nil
}

// SyncLdap triggers an LDAP synchronization. It returns an error if LDAP is not
// enabled or the sync fails.
func (c *Client) SyncLdap(ctx context.Context) error {
	_, err := c.doRequest(ctx, "POST", "/api/application-configuration/sync-ldap", nil)
	return err
}
