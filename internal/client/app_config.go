package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// reported holds every key the server's response gave a string value,
	// so a key it left out stays distinguishable from one it reported as "".
	reported map[string]bool
}

// Reported reports whether the response cfg was decoded from gave key a
// string value (possibly ""). It is false for a configuration built in code,
// and for a key the server left out.
func (cfg *ApplicationConfig) Reported(key string) bool {
	return cfg.reported[key]
}

// ErrIncompleteApplicationConfig is returned when a response lists a setting
// without a value (the value missing or null). Reading it as "" could make an
// update clear that setting. A value of another JSON type is not the JSON this
// provider expects and is reported as ErrUndecodableResponse.
var ErrIncompleteApplicationConfig = errors.New("the application configuration response lists a setting without a string value")

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

// appConfigWireVariable is one entry of an application configuration
// response, with its value kept raw so a missing or null value is not taken
// for "".
type appConfigWireVariable struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// decodeApplicationConfig decodes the key/value list returned by the
// application configuration endpoints with decode (decodeResponse for a read,
// decodeResult for an update), so a body or value that is not the JSON
// expected gives that decoder's fixed error. Every listed value must be a
// JSON string: a missing or null value returns ErrIncompleteApplicationConfig.
// JSON tags also map WebAuthn and CIMD fields; absent older-server keys remain
// empty instead of inventing security defaults (Reported tells them apart
// from ""). A *string field stays nil when its key is absent, so it is omitted
// from an update to that server. Keys with no field go to Additional.
func decodeApplicationConfig(body []byte, decode func([]byte, any) error) (*ApplicationConfig, error) {
	var vars []appConfigWireVariable
	if err := decode(body, &vars); err != nil {
		return nil, err
	}
	values := make(map[string]string, len(vars))
	for _, v := range vars {
		raw := bytes.TrimSpace(v.Value)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return nil, ErrIncompleteApplicationConfig
		}
		var value string
		if err := decode(raw, &value); err != nil {
			return nil, err
		}
		values[v.Key] = value
	}

	cfg := &ApplicationConfig{reported: make(map[string]bool, len(values))}
	for key := range values {
		cfg.reported[key] = true
	}
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

	return cfg, nil
}

// GetApplicationConfig retrieves the full application configuration, including
// private values, from GET /api/application-configuration/all.
func (c *Client) GetApplicationConfig(ctx context.Context) (*ApplicationConfig, error) {
	body, err := c.doRequest(ctx, "GET", "/api/application-configuration/all", nil)
	if err != nil {
		return nil, err
	}

	cfg, err := decodeApplicationConfig(body, decodeResponse)
	if err != nil {
		return nil, err
	}
	if err := c.checkAppConfigGroupIDs(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// checkAppConfigGroupIDs checks the user group IDs the configuration holds in
// signupDefaultUserGroupIDs, a JSON array of IDs in a string: the string must
// not contain the API key, and when it is such an array each ID must pass
// checkReturnedID. A value that is not an array is left to the caller, which
// shows it as the string it is.
func (c *Client) checkAppConfigGroupIDs(cfg *ApplicationConfig) error {
	if c.reflectsKey(cfg.SignupDefaultUserGroupIDs) {
		return fmt.Errorf("%w: the signup default user group IDs in the response contain the API key this provider sent", ErrInvalidIdentifier)
	}
	var ids []string
	if json.Unmarshal([]byte(cfg.SignupDefaultUserGroupIDs), &ids) != nil {
		return nil
	}
	for _, id := range ids {
		if err := c.checkReturnedID("user group", "", id); err != nil {
			return err
		}
	}
	return nil
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

	updated, err := decodeApplicationConfig(body, decodeResult)
	if errors.Is(err, ErrResultUnread) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrResultUnread, err)
	}
	if err := c.checkAppConfigGroupIDs(updated); err != nil {
		return nil, unreadResult(err)
	}
	return updated, nil
}

// SyncLdap triggers an LDAP synchronization. It returns an error if LDAP is not
// enabled or the sync fails.
func (c *Client) SyncLdap(ctx context.Context) error {
	_, err := c.doRequest(ctx, "POST", "/api/application-configuration/sync-ldap", nil)
	return err
}
