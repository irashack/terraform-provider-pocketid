package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// What the provider guarantees about the API key in text, beyond identifiers
// (identifiers.go, returned_ids.go): no request carries it in a configured
// value, and no answer whose text carries it is used, except inside a secret
// value.
//
// The checks run on decoded values, field by field: each request type lists
// the text it sends (textsToSend), and each answer type the text the provider
// takes from it (shownTexts). An escaped key is therefore found, a JSON field
// name is never mistaken for data, and a field the provider does not decode
// is not looked at. A number is checked as it is written in decimal, since a
// static key can be all digits.
//
// The secret values of a type are not checked, and only those: a client
// secret's value, the token of a SCIM service provider, a signup token or a
// one-time access token, and the SMTP and LDAP passwords of the application
// configuration go only to sensitive state and are never shown, and a
// configuration may legitimately use the same string as a SCIM token, for
// example. A value of the same name elsewhere (a custom claim called
// smtpPassword) is ordinary text and is checked.

// textBearing is a request body: textsToSend returns every value it carries
// except its secret values. doRequest refuses a body that does not declare
// its texts this way, so a new request type cannot skip the check.
type textBearing interface {
	textsToSend() []string
}

// ErrKeyInResponse marks an answer that carries the API key in a value the
// provider would store, log or show. Nothing from it is used; a create whose
// own ID passed its check keeps only that ID, so the caller can recover the
// object. It wraps ErrInvalidIdentifier and names no value.
var ErrKeyInResponse = fmt.Errorf("%w: a value in the response contains the API key this provider sends, so the response is not used", ErrInvalidIdentifier)

// errKeyInRequest refuses a request whose body carries the API key outside a
// secret value. Nothing was sent.
var errKeyInRequest = fmt.Errorf("%w: a value in this request contains the API key this provider sends; the request was not sent", ErrInvalidIdentifier)

// checkRequestText refuses a request body that carries the API key outside a
// secret value of its type, or that does not declare its texts.
func (c *Client) checkRequestText(body any) error {
	bearer, ok := body.(textBearing)
	if !ok {
		return fmt.Errorf("request body of type %T declares no texts; the request was not sent", body)
	}
	if c.containsKey(bearer.textsToSend()...) {
		return errKeyInRequest
	}
	return nil
}

// checkReturnedText returns ErrKeyInResponse when any of texts, taken from an
// answer, contains the API key.
func (c *Client) checkReturnedText(texts ...string) error {
	if c.containsKey(texts...) {
		return ErrKeyInResponse
	}
	return nil
}

func optionalText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func intText(n int64) string { return strconv.FormatInt(n, 10) }

// jsonTexts returns every string, number and object key of a JSON document
// the provider keeps as it is (a federated identity's public key). A document
// that does not decode gives its raw text.
func jsonTexts(document []byte) []string {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return []string{string(document)}
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case json.Number:
			out = append(out, v.String())
		case []any:
			for _, element := range v {
				walk(element)
			}
		case map[string]any:
			for key, element := range v {
				out = append(out, key)
				walk(element)
			}
		}
	}
	walk(value)
	return out
}

func claimTexts(claims []CustomClaim) []string {
	out := make([]string, 0, 2*len(claims))
	for _, claim := range claims {
		out = append(out, claim.Key, claim.Value)
	}
	return out
}

// Answers: the text the provider takes from each type.

func (u *User) shownTexts() []string {
	texts := []string{u.Username, u.Email, u.FirstName, u.LastName, u.DisplayName, optionalText(u.Locale), optionalText(u.LdapID)}
	texts = append(texts, claimTexts(u.CustomClaims)...)
	for i := range u.UserGroups {
		texts = append(texts, u.UserGroups[i].shownTexts()...)
	}
	return texts
}

func (g *UserGroup) shownTexts() []string {
	texts := []string{g.Name, g.FriendlyName, optionalText(g.LdapID), g.CreatedAt, intText(int64(g.UserCount))}
	texts = append(texts, claimTexts(g.CustomClaims)...)
	for i := range g.Users {
		texts = append(texts, g.Users[i].shownTexts()...)
	}
	return texts
}

func (g *GroupDetail) shownTexts() []string {
	texts := []string{g.Name, g.FriendlyName, optionalText(g.LdapID), g.CreatedAt}
	return append(texts, claimTexts(g.CustomClaims)...)
}

func federatedIdentityTexts(identities []OIDCClientFederatedIdentity) []string {
	var texts []string
	for _, identity := range identities {
		texts = append(texts, identity.Issuer, identity.Subject, identity.Audience, identity.JWKS)
		for _, key := range identity.PublicKeys {
			texts = append(texts, jsonTexts(key)...)
		}
	}
	return texts
}

// shownTexts of a client: its settings, URLs and federated identities, and
// the groups it lists. The secrets it lists carry only an ID, a prefix of at
// most four characters (which cannot hold a key Pocket ID accepts: at least
// 16) and times that decode only as times.
func (o *OIDCClient) shownTexts() []string {
	texts := []string{o.Name, o.BackchannelLogoutURL, o.LaunchURL, o.ClientType, o.Description,
		optionalText(o.LogoURL), optionalText(o.DarkLogoURL),
		intText(o.AccessTokenDurationMinutes), intText(o.RefreshTokenDurationMinutes), intText(o.AllowedUserGroupsCount)}
	texts = append(texts, o.CallbackURLs...)
	texts = append(texts, o.LogoutCallbackURLs...)
	texts = append(texts, federatedIdentityTexts(o.Credentials.FederatedIdentities)...)
	for i := range o.AllowedUserGroups {
		texts = append(texts, o.AllowedUserGroups[i].shownTexts()...)
	}
	return texts
}

// shownTexts of a SCIM service provider; its token is its secret.
func (s *ScimServiceProvider) shownTexts() []string {
	texts := []string{s.Endpoint, optionalText(s.LastSyncedAt), s.CreatedAt}
	if s.OidcClient != nil {
		texts = append(texts, s.OidcClient.Name)
	}
	return texts
}

// shownTexts of a signup token; its token value is its secret.
func (t *SignupToken) shownTexts() []string {
	texts := []string{t.ExpiresAt, t.CreatedAt, intText(int64(t.UsageLimit)), intText(int64(t.UsageCount))}
	for i := range t.UserGroups {
		texts = append(texts, t.UserGroups[i].shownTexts()...)
	}
	return texts
}

func (k *APIKey) shownTexts() []string {
	return []string{k.Name, optionalText(k.Description), k.ExpiresAt, optionalText(k.LastUsedAt), k.CreatedAt}
}

func (p *Passkey) shownTexts() []string {
	return append([]string{p.Name, p.CreatedAt, p.AAGUID}, p.Transports...)
}

// appConfigSecretKeys are the settings whose values are secret in the
// application configuration: they are sent and stored only as sensitive
// values.
var appConfigSecretKeys = map[string]bool{"smtpPassword": true, "ldapBindPassword": true}

// shownTexts of the application configuration: the value of every setting
// the provider maps to a field, except its two passwords. Settings it does
// not know (Additional) are only sent back unchanged, never stored or shown.
func (cfg *ApplicationConfig) shownTexts() []string {
	var texts []string
	cfgValue := reflect.ValueOf(cfg).Elem()
	cfgType := cfgValue.Type()
	for i := 0; i < cfgType.NumField(); i++ {
		key, _, _ := strings.Cut(cfgType.Field(i).Tag.Get("json"), ",")
		if key == "" || key == "-" || appConfigSecretKeys[key] {
			continue
		}
		field := cfgValue.Field(i)
		switch field.Kind() {
		case reflect.Pointer:
			if !field.IsNil() {
				texts = append(texts, field.Elem().String())
			}
		case reflect.String:
			texts = append(texts, field.String())
		}
	}
	return texts
}

// Requests: the text each request type sends, its secret values excepted.

func (r UserCreateRequest) textsToSend() []string {
	texts := []string{r.ID, r.Username, r.Email, r.FirstName, r.LastName, r.DisplayName, optionalText(r.Locale)}
	return append(texts, r.UserGroupIDs...)
}

func (r UpdateUserGroupsRequest) textsToSend() []string { return r.UserGroupIDs }

func (r UserGroupCreateRequest) textsToSend() []string { return []string{r.Name, r.FriendlyName} }

func (r OIDCClientCreateRequest) textsToSend() []string {
	texts := []string{r.Name, optionalText(r.ClientID), optionalText(r.BackchannelLogoutURL), optionalText(r.LaunchURL),
		r.Description, optionalText(r.LogoURL), optionalText(r.DarkLogoURL),
		intText(r.AccessTokenDurationMinutes), intText(r.RefreshTokenDurationMinutes)}
	texts = append(texts, r.CallbackURLs...)
	texts = append(texts, r.LogoutCallbackURLs...)
	return append(texts, federatedIdentityTexts(r.Credentials.FederatedIdentities)...)
}

func (r UpdateAllowedUserGroupsRequest) textsToSend() []string { return r.UserGroupIDs }

// secretCreateRequest is the body of a secret creation; Secret is the
// secret's value.
type secretCreateRequest struct {
	Secret    string     `json:"secret,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

func (r secretCreateRequest) textsToSend() []string {
	if r.ExpiresAt == nil {
		return nil
	}
	encoded, _ := json.Marshal(r.ExpiresAt)
	return []string{string(encoded)}
}

func (r OneTimeAccessTokenRequest) textsToSend() []string { return []string{r.TTL} }

// textsToSend of a SCIM service provider request; its token is its secret.
func (r ScimServiceProviderCreateRequest) textsToSend() []string {
	return []string{r.Endpoint, r.OidcClientID}
}

func (r SignupTokenCreateRequest) textsToSend() []string {
	return append([]string{r.TTL, intText(int64(r.UsageLimit))}, r.UserGroupIDs...)
}

// customClaimsRequest is the body of a custom claims replacement.
type customClaimsRequest []CustomClaim

func (r customClaimsRequest) textsToSend() []string { return claimTexts(r) }

// groupMembersRequest is the body of a group's member replacement.
type groupMembersRequest struct {
	UserIDs []string `json:"userIds"`
}

func (r groupMembersRequest) textsToSend() []string { return r.UserIDs }

// appConfigRequest is the body of a configuration update: every setting,
// Additional included. The texts checked are the settings the provider maps
// (what the configuration sets), its passwords excepted; Additional holds
// only what the server reported and goes back unchanged.
type appConfigRequest struct {
	cfg *ApplicationConfig
}

func (r appConfigRequest) MarshalJSON() ([]byte, error) { return json.Marshal(r.cfg.Values()) }

func (r appConfigRequest) textsToSend() []string { return r.cfg.shownTexts() }

func (r APICreateRequest) textsToSend() []string { return []string{r.Name, r.Resource} }

func (r APIUpdateRequest) textsToSend() []string { return []string{r.Name} }

func (r apiPermissionsUpdateRequest) textsToSend() []string {
	var texts []string
	for _, p := range r.Permissions {
		texts = append(texts, p.Key, p.Name, optionalText(p.Description))
	}
	return texts
}

func (r apiCIMDAccessRequest) textsToSend() []string { return r.PermissionIDs }

func (g APIClientGrant) textsToSend() []string {
	return append(append([]string{}, g.UserDelegatedPermissionIDs...), g.ClientPermissionIDs...)
}
