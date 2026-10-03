package client

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidIdentifier marks an identifier the client refused to put into a
// request path. Nothing was sent. The identifier itself is never included in
// the error: it may have come from a server response.
var ErrInvalidIdentifier = errors.New("invalid identifier")

// uuidPattern is the 8-4-4-4-12 hexadecimal form. Pocket ID generates every
// object ID that is not a client ID this way (model.Base.BeforeCreate, and the
// secret migration for client secrets), and a caller-chosen user ID must be
// one too (UserCreateDto: binding "uuid").
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// clientIDPattern is Pocket ID's rule for a client ID chosen at creation
// (OidcClientCreateDto: binding "client_id,min=2,max=128" with
// validateClientIDRegex), unchanged from v2.0.0 to v2.17.0. A generated client
// ID is a UUID, which also matches.
var clientIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,128}$`)

// ValidateUUID checks that id has the UUID form Pocket ID uses for users, user
// groups, client secrets, SCIM service providers and the other objects it
// generates IDs for. kind names the object in the error, for example "user".
func ValidateUUID(kind, id string) error {
	if !uuidPattern.MatchString(id) {
		return fmt.Errorf("%w: %s ID must be a UUID (8-4-4-4-12 hexadecimal digits)", ErrInvalidIdentifier, kind)
	}
	return nil
}

// ValidateClientID checks id against Pocket ID's rule for an OIDC client ID:
// 2 to 128 characters from A-Z, a-z, 0-9, '.', '_' and '-'. ".." passes that
// rule but is a relative path segment, so it is refused as well.
//
// Clients created from a Client ID Metadata Document (client_type "cimd") have
// an https URL as their ID; it does not pass, so this client cannot address
// such a client by ID.
func ValidateClientID(id string) error {
	if !clientIDPattern.MatchString(id) || id == ".." {
		return fmt.Errorf("%w: OIDC client ID must be 2 to 128 characters of letters, digits, '.', '_' and '-'", ErrInvalidIdentifier)
	}
	return nil
}

// uuidSegment returns id escaped for use as one path segment, after checking
// it is a UUID.
func uuidSegment(kind, id string) (string, error) {
	if err := ValidateUUID(kind, id); err != nil {
		return "", err
	}
	return url.PathEscape(id), nil
}

// clientIDSegment returns an OIDC client ID escaped for use as one path
// segment, after checking it with ValidateClientID.
func clientIDSegment(id string) (string, error) {
	if err := ValidateClientID(id); err != nil {
		return "", err
	}
	return url.PathEscape(id), nil
}

// checkCreatedID decides whether the ID a create response returned may be
// used. requested is the ID the request supplied, empty when the server
// chooses it. An ID the server chose must be a UUID: Pocket ID generates one
// for every object it creates (model.Base.BeforeCreate), OIDC clients
// included. An ID the caller supplied must come back exactly. And no ID may
// contain the API key this client sends: Pocket ID accepts any static API
// key of 16 or more characters, so a key can itself look like a UUID, and a
// server that returned it as an ID would get it logged and into URLs.
// Anything else is refused, with an error that never includes the returned
// value.
func (c *Client) checkCreatedID(kind, requested, returned string) error {
	if c.reflectsKey(returned) {
		return fmt.Errorf("%w: the %s ID in the create response contains the API key this provider sent", ErrInvalidIdentifier, kind)
	}
	if requested != "" {
		if returned != requested {
			return fmt.Errorf("%w: the %s ID in the create response is not the one requested", ErrInvalidIdentifier, kind)
		}
		return nil
	}
	if !uuidPattern.MatchString(returned) {
		return fmt.Errorf("%w: the %s ID in the create response is not a UUID", ErrInvalidIdentifier, kind)
	}
	return nil
}

// kindOIDCClient is the kind to pass checkReturnedID for an OIDC client's ID.
// It is the one kind whose IDs are not all UUIDs: a client ID may be chosen
// at creation (ValidateClientID's rule), and a client registered from a
// Client ID Metadata Document has an https URL as its ID.
const kindOIDCClient = "OIDC client"

// maxCIMDClientIDLength bounds the https URL accepted as a CIMD client's ID.
// Pocket ID sets no limit of its own; this one only keeps an absurd value
// out of state.
const maxCIMDClientIDLength = 2048

// checkReturnedID decides whether an object ID that a response carries may be
// used: kept in state, logged, compared, or put into a later request. Every
// method that returns an object ID passes each one through it before
// returning, nested IDs included (a user's groups, a client's allowed
// groups, the client a SCIM provider belongs to, every item of a list); a
// create response's own ID goes through checkCreatedID instead.
//
//   - No ID may contain the API key this client sends (see checkCreatedID
//     for why a UUID-shaped key matters).
//   - addressed is the ID the call named, in its path or as the parent the
//     object must belong to; the returned ID must then be exactly it, not
//     another object's and not a case variant. Pass "" when the call named
//     none: a list item, an object looked up through its parent, a nested
//     object.
//   - An ID the call did not name must have its kind's form: a UUID, which
//     Pocket ID generates for every object (model.Base.BeforeCreate), except
//     for kindOIDCClient, whose ID is one ValidateClientID accepts (UUIDs
//     included) or a CIMD client's https URL.
//
// An empty ID is refused like any other; a caller whose response may
// legitimately omit an object checks for its presence first. kind names the
// object in the error, which wraps ErrInvalidIdentifier and never includes
// the returned value.
func (c *Client) checkReturnedID(kind, addressed, returned string) error {
	form := isUUID
	if kind == kindOIDCClient {
		form = isOIDCClientID
	}
	return c.checkResponseID(kind, form, addressed, returned)
}

// checkResponseID is checkReturnedID with the form of an unaddressed ID
// given explicitly. listAll uses it with isOIDCClientID, which accepts every
// form a Pocket ID object ID can take, because it serves lists of every kind.
func (c *Client) checkResponseID(kind string, form func(string) bool, addressed, returned string) error {
	switch {
	case c.reflectsKey(returned):
		return fmt.Errorf("%w: the %s ID in the response contains the API key this provider sent", ErrInvalidIdentifier, kind)
	case addressed != "":
		if returned != addressed {
			return fmt.Errorf("%w: the %s ID in the response is not the one requested", ErrInvalidIdentifier, kind)
		}
	case !form(returned):
		return fmt.Errorf("%w: the %s ID in the response is not a valid %s ID", ErrInvalidIdentifier, kind, kind)
	}
	return nil
}

func isUUID(id string) bool { return uuidPattern.MatchString(id) }

// isOIDCClientID reports whether id is an ID Pocket ID can give an OIDC
// client: one ValidateClientID accepts, or a CIMD client's URL. Every UUID is
// one, so this is also the widest form any object ID takes.
func isOIDCClientID(id string) bool {
	return ValidateClientID(id) == nil || isCIMDClientID(id)
}

// isCIMDClientID applies the rules Pocket ID's OIDC library sets for a client
// ID that is a metadata document URL (ParseCIMDURL in
// github.com/pocket-id/fosite v1.3.0, which 2.14.0 to 2.17.0 use): https, a
// host, a path without "." or ".." segments, and no user information, query
// or fragment. It also requires printable ASCII without spaces and at most
// maxCIMDClientIDLength bytes.
func isCIMDClientID(id string) bool {
	if len(id) > maxCIMDClientIDLength || strings.ContainsAny(id, "#?") {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	u, err := url.Parse(id)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil || u.Path == "" {
		return false
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// reflectsKey reports whether value contains the API key this client sends,
// in the form the server receives it (see normalizeAPIKey).
func (c *Client) reflectsKey(value string) bool {
	return c.apiToken != "" && strings.Contains(value, c.apiToken)
}
