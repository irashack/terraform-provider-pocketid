package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Every request body type the client sends.
var (
	_ textBearing = UserCreateRequest{}
	_ textBearing = UpdateUserGroupsRequest{}
	_ textBearing = UserGroupCreateRequest{}
	_ textBearing = OIDCClientCreateRequest{}
	_ textBearing = UpdateAllowedUserGroupsRequest{}
	_ textBearing = secretCreateRequest{}
	_ textBearing = OneTimeAccessTokenRequest{}
	_ textBearing = ScimServiceProviderCreateRequest{}
	_ textBearing = SignupTokenCreateRequest{}
	_ textBearing = customClaimsRequest{}
	_ textBearing = groupMembersRequest{}
	_ textBearing = appConfigRequest{}
	_ textBearing = APICreateRequest{}
	_ textBearing = APIUpdateRequest{}
	_ textBearing = apiPermissionsUpdateRequest{}
	_ textBearing = apiCIMDAccessRequest{}
	_ textBearing = APIClientGrant{}
)

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
	if documents, ok := body.(jsonBearing); ok {
		for _, document := range documents.jsonDocuments() {
			if JSONRepeatsMember(string(document)) {
				return errRepeatedMemberInRequest
			}
		}
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
// the provider keeps as it is (a federated identity's public key), each
// occurrence counted, so a member repeated under the same name is inspected
// too. A document that does not decode gives its raw text.
func jsonTexts(document []byte) []string {
	texts, _, ok := walkJSON(document)
	if !ok {
		return []string{string(document)}
	}
	return texts
}

// JSONRepeatsMember reports whether an object anywhere in the JSON document
// repeats a member name (compared after unescaping). Such a document reads
// differently to different parsers (Go keeps the last value), so the
// provider neither keeps nor sends one. A document that does not decode is
// reported as repeating, too.
func JSONRepeatsMember(document string) bool {
	_, repeated, ok := walkJSON([]byte(document))
	return repeated || !ok
}

// walkJSON reads a JSON document token by token. It returns every string
// (object member names included) and number in it, each occurrence counted
// and unescaped, whether an object repeats a member name, and whether the
// document is valid JSON.
func walkJSON(document []byte) (texts []string, repeated, ok bool) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	type frame struct {
		object    bool
		expectKey bool
		names     map[string]bool
	}
	var stack []*frame
	// value marks a value read in the current object, after which a member
	// name comes next.
	value := func() {
		if len(stack) > 0 && stack[len(stack)-1].object {
			stack[len(stack)-1].expectKey = true
		}
	}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return texts, repeated, len(stack) == 0
		}
		if err != nil {
			return texts, repeated, false
		}
		switch t := token.(type) {
		case json.Delim:
			switch t {
			case '{':
				value()
				stack = append(stack, &frame{object: true, expectKey: true, names: map[string]bool{}})
			case '[':
				value()
				stack = append(stack, &frame{})
			default:
				stack = stack[:len(stack)-1]
			}
		case string:
			texts = append(texts, t)
			if top := len(stack) - 1; top >= 0 && stack[top].object && stack[top].expectKey {
				repeated = repeated || stack[top].names[t]
				stack[top].names[t] = true
				stack[top].expectKey = false
				continue
			}
			value()
		case json.Number:
			texts = append(texts, t.String())
			value()
		default:
			value()
		}
	}
}

// jsonBearing is a request body that sends JSON documents as they are (a
// federated identity's public keys); checkRequestText refuses one that
// repeats a member.
type jsonBearing interface {
	jsonDocuments() [][]byte
}

func federatedIdentityDocuments(identities []OIDCClientFederatedIdentity) [][]byte {
	var documents [][]byte
	for _, identity := range identities {
		for _, key := range identity.PublicKeys {
			documents = append(documents, key)
		}
	}
	return documents
}

func (r OIDCClientCreateRequest) jsonDocuments() [][]byte {
	return federatedIdentityDocuments(r.Credentials.FederatedIdentities)
}

// errRepeatedMemberInRequest refuses a request that would send a JSON
// document repeating a member. Nothing was sent.
var errRepeatedMemberInRequest = fmt.Errorf("%w: a JSON document in this request repeats an object member; the request was not sent", ErrInvalidIdentifier)

// errRepeatedMemberInResponse refuses an answer that carries a JSON document
// repeating a member; nothing of it is used.
var errRepeatedMemberInResponse = fmt.Errorf("%w: a JSON document in the response repeats an object member", ErrUndecodableResponse)

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

// timeTexts returns the forms a time from an answer is stored or printed in:
// RFC 3339 with and without fractions of a second, in UTC and as sent.
func timeTexts(t time.Time) []string {
	utc := t.UTC()
	return []string{utc.Format(time.RFC3339), utc.Format(time.RFC3339Nano), t.Format(time.RFC3339Nano)}
}

// secretMetadataTexts returns the text the provider takes from a secret's
// metadata: its prefix and its times.
func secretMetadataTexts(secret *ClientSecretMetadata) []string {
	texts := append([]string{secret.Prefix}, timeTexts(secret.CreatedAt)...)
	if secret.ExpiresAt != nil {
		texts = append(texts, timeTexts(*secret.ExpiresAt)...)
	}
	return texts
}

// shownTexts of a client: its settings, URLs and federated identities, the
// groups it lists and the metadata of the secrets it lists.
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
	for i := range o.Credentials.Secrets {
		texts = append(texts, secretMetadataTexts(&o.Credentials.Secrets[i])...)
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
