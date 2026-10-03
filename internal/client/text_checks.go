package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// What the provider guarantees about the API key in text: no request body
// carries it, and no answer that carries it is used, except inside a secret
// value. A secret value (a client secret, a SCIM or signup or one-time token,
// the SMTP or LDAP password) goes only to sensitive state and is never logged
// or printed, so it is not checked: a configuration may legitimately send the
// same string as a SCIM token, for example. Every other string in a JSON
// request or answer, object keys included, is checked once decoded, so that
// a key written with JSON escapes (A, \/) is found too; a number is
// checked as written, since a static key can be all digits.

// secretFields are the JSON field names whose values are secret values in
// Pocket ID's requests and answers: a created client secret's "secret", and
// the "token" of a SCIM service provider, a signup token or a one-time access
// token.
var secretFields = map[string]bool{"secret": true, "token": true}

// secretSettings are the application settings whose values are secret, in
// the configuration update (a field of the body) and in the configuration
// answer (the "value" of the entry whose "key" names them).
var secretSettings = map[string]bool{"smtpPassword": true, "ldapBindPassword": true}

// errKeyInRequest refuses a request whose body carries the API key. Nothing
// was sent.
var errKeyInRequest = fmt.Errorf("%w: a value in this request contains the API key this provider sends; the request was not sent", ErrInvalidIdentifier)

// errKeyInResponse refuses an answer that carries the API key. Nothing from
// it is used, stored or shown.
var errKeyInResponse = fmt.Errorf("%w: the response contains the API key this provider sends, so nothing from it is used", ErrInvalidIdentifier)

// checkRequestText refuses a JSON request body that carries the API key
// outside a secret value. It returns an error wrapping ErrInvalidIdentifier
// and naming nothing; the request is not sent.
func (c *Client) checkRequestText(payload []byte) error {
	if c.jsonReflectsKey(payload) {
		return errKeyInRequest
	}
	return nil
}

// checkResponseText refuses a successful answer that carries the API key
// outside a secret value. After a mutation the change was made, so the error
// also wraps ErrResultUnread. A body that is not JSON is left to the caller's
// decoder, which refuses it.
func (c *Client) checkResponseText(method string, body []byte) error {
	if !c.jsonReflectsKey(body) {
		return nil
	}
	if method != http.MethodGet {
		return fmt.Errorf("%w: %w", ErrResultUnread, errKeyInResponse)
	}
	return errKeyInResponse
}

// jsonReflectsKey reports whether a JSON document contains the API key in any
// string, number or object key outside a secret value. It is false for a
// client without a key and for a document that does not decode.
func (c *Client) jsonReflectsKey(document []byte) bool {
	if c == nil || c.apiToken == "" || len(bytes.TrimSpace(document)) == 0 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	return c.valueReflectsKey(value)
}

func (c *Client) valueReflectsKey(value any) bool {
	switch v := value.(type) {
	case string:
		return c.reflectsKey(v)
	case json.Number:
		return c.reflectsKey(v.String())
	case []any:
		for _, element := range v {
			if c.valueReflectsKey(element) {
				return true
			}
		}
	case map[string]any:
		setting, _ := v["key"].(string)
		secretSetting := secretSettings[setting]
		for key, element := range v {
			if c.reflectsKey(key) {
				return true
			}
			if secretFields[key] || secretSettings[key] || (secretSetting && key == "value") {
				continue
			}
			if c.valueReflectsKey(element) {
				return true
			}
		}
	}
	return false
}
