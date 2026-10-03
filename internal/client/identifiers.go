package client

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidIdentifier marks an identifier the client refused. For one that
// was to go into a request (a path segment or query parameter from
// configuration, state or import), nothing was sent. For one that came back
// in a response (checkCreatedID, checkReturnedID), the request was made and
// only its answer is refused. The identifier itself is never included in the
// error: it may carry the API key, or have come from a server response.
var ErrInvalidIdentifier = errors.New("invalid identifier")

// uuidPattern is the 8-4-4-4-12 hexadecimal form. Pocket ID generates every
// object ID that is not a client ID this way (model.Base.BeforeCreate, and the
// secret migration for client secrets), and a caller-chosen user ID must be
// one too (UserCreateDto: binding "uuid").
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// clientIDPattern is Pocket ID's rule for a client ID chosen at creation
// (OidcClientCreateDto: binding "client_id,min=2,max=128" with
// validateClientIDRegex), unchanged from v2.0.0 to v2.17.0. A generated client
// ID is a UUID, which also matches. It governs the IDs this client puts into
// requests (ValidateClientID); an ID in a response is held to the server's
// own, wider rules instead (isReturnedClientID).
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

// ValidateIdentifier checks an identifier that comes from configuration,
// state or import before it is used anywhere, logged or shown: it must not
// contain the API key this client sends (as the server receives it), and it
// must have its kind's form (ValidateClientID for kindOIDCClient, "OIDC
// client"; a UUID for every other kind). The errors wrap ErrInvalidIdentifier,
// are fixed text and never include the identifier.
//
// Every request applies the key check to its whole path and query anyway
// (checkEndpoint), so a key-bearing identifier is never sent; calling this
// first also keeps it out of the logs and diagnostics written before the
// request, such as an import's.
func (c *Client) ValidateIdentifier(kind, id string) error {
	if c.reflectsKey(id) {
		return fmt.Errorf("%w: the %s ID contains the API key this provider sends, so it is not used", ErrInvalidIdentifier, kind)
	}
	if kind == kindOIDCClient {
		return ValidateClientID(id)
	}
	return ValidateUUID(kind, id)
}

// errKeyInEndpoint is checkEndpoint's refusal. It names neither the request
// nor the identifier: both would show the key.
var errKeyInEndpoint = fmt.Errorf("%w: an identifier or parameter of this request contains the API key this provider sends; the request was not sent", ErrInvalidIdentifier)

// errMalformedEndpoint is checkEndpoint's refusal of a path or query it
// cannot decode, and so cannot check.
var errMalformedEndpoint = fmt.Errorf("%w: the request's path or query is not well formed; the request was not sent", ErrInvalidIdentifier)

// checkEndpoint refuses a request endpoint (path and query, as sendWith
// receives it) that contains the API key this client sends, in its raw form
// or once its path and its query parameters are decoded, so that an escaped
// key is found too. Every identifier that goes into a path (uuidSegment,
// clientIDSegment and any other builder) or a query (a search term, a
// filter) passes through it before a request is built or anything is
// logged; an endpoint it cannot decode is refused as well.
func (c *Client) checkEndpoint(endpoint string) error {
	if c.reflectsKey(endpoint) {
		return errKeyInEndpoint
	}
	path, query, _ := strings.Cut(endpoint, "?")
	decodedPath, err := url.PathUnescape(path)
	if err != nil {
		return errMalformedEndpoint
	}
	if c.reflectsKey(decodedPath) {
		return errKeyInEndpoint
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return errMalformedEndpoint
	}
	for name, list := range values {
		if c.reflectsKey(name) {
			return errKeyInEndpoint
		}
		for _, value := range list {
			if c.reflectsKey(value) {
				return errKeyInEndpoint
			}
		}
	}
	return nil
}

// uuidSegment returns id escaped for use as one path segment, after checking
// it is a UUID. It cannot see the API key; the request refuses a segment
// that contains it (checkEndpoint).
func uuidSegment(kind, id string) (string, error) {
	if err := ValidateUUID(kind, id); err != nil {
		return "", err
	}
	return url.PathEscape(id), nil
}

// clientIDSegment returns an OIDC client ID escaped for use as one path
// segment, after checking it with ValidateClientID. Like uuidSegment, it
// leaves the API key check to the request (checkEndpoint).
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

// kindOIDCClient is the kind to pass checkReturnedID (and ValidateIdentifier)
// for an OIDC client's ID. It is the one kind whose IDs are not all UUIDs: a
// client ID may be chosen at creation, and a client registered from a Client
// ID Metadata Document has an https URL as its ID.
const kindOIDCClient = "OIDC client"

// checkReturnedID decides whether an object ID that a response carries may be
// used: kept in state, logged, compared, or put into a later request. Every
// method that returns an object ID passes each one through it before
// returning, nested IDs included (a user's groups, a client's allowed
// groups, the client a SCIM provider belongs to, every item of a list); a
// create response's own ID goes through checkCreatedID instead.
//
//   - No ID may contain the API key this client sends (see checkCreatedID
//     for why a UUID-shaped key matters). This is checked first.
//   - addressed is the ID the call named, in its path or as the parent the
//     object must belong to; the returned ID must then be the same ID, not
//     another object's. For a UUID kind that means the same UUID: both must
//     be UUIDs, compared without regard to case, because PostgreSQL keeps
//     users, groups and SCIM providers in native UUID columns (id UUID in
//     backend/resources/migrations/postgres) and answers a request for
//     "ABC..." with "abc...". An OIDC client ID is text and must come back
//     byte for byte. A caller that sends a later request for the object uses
//     the ID the server returned, not its own spelling. Pass "" when the call
//     named none: a list item, an object looked up through its parent, a
//     nested object.
//   - An ID the call did not name must have a form the server itself can
//     give that kind: a UUID, which Pocket ID generates for every object
//     (model.Base.BeforeCreate), except for kindOIDCClient, which takes any
//     ID Pocket ID's own client-ID rules accept (see isReturnedClientID).
//
// These are the server's rules, not this client's rules for what it puts in
// a request path: an ID the server legitimately holds is accepted here even
// where this client cannot address it (ValidateClientID refuses "..", which
// is a relative path segment, and every CIMD URL), so that a list holding
// such a client still reads. The overall response size limit still bounds
// every ID.
//
// An empty ID is refused like any other; a caller whose response may
// legitimately omit an object checks for its presence first. kind names the
// object in the error, which wraps ErrInvalidIdentifier and never includes
// the returned value.
func (c *Client) checkReturnedID(kind, addressed, returned string) error {
	rule := uuidIDs
	if kind == kindOIDCClient {
		rule = clientIDs
	}
	return c.checkResponseID(kind, rule, addressed, returned)
}

// idRule is what checkResponseID needs to know about a kind of ID: the forms
// the server can give it, and how two spellings of one ID compare.
type idRule struct {
	valid func(string) bool
	// sameID reports whether returned names the object addressed named.
	sameID func(addressed, returned string) bool
}

var (
	// uuidIDs: a UUID, compared as a UUID (without regard to case).
	uuidIDs = idRule{valid: isUUID, sameID: sameUUID}
	// clientIDs: an OIDC client ID, compared byte for byte. Every UUID is
	// one too, so listAll uses this rule for lists of every kind.
	clientIDs = idRule{valid: isReturnedClientID, sameID: func(addressed, returned string) bool { return returned == addressed }}
)

// sameUUID reports whether two strings are the same UUID: both valid UUIDs
// that differ at most in the case of their hexadecimal digits.
func sameUUID(addressed, returned string) bool {
	return isUUID(addressed) && isUUID(returned) && strings.EqualFold(addressed, returned)
}

// checkResponseID is checkReturnedID with the rule for the kind given
// explicitly. listAll uses clientIDs, which accepts every form a Pocket ID
// object ID can take, because it serves lists of every kind.
func (c *Client) checkResponseID(kind string, rule idRule, addressed, returned string) error {
	switch {
	case c.reflectsKey(returned):
		return fmt.Errorf("%w: the %s ID in the response contains the API key this provider sent", ErrInvalidIdentifier, kind)
	case addressed != "":
		if !rule.sameID(addressed, returned) {
			return fmt.Errorf("%w: the %s ID in the response is not the one requested", ErrInvalidIdentifier, kind)
		}
	case !rule.valid(returned):
		return fmt.Errorf("%w: the %s ID in the response is not a valid %s ID", ErrInvalidIdentifier, kind, kind)
	}
	return nil
}

func isUUID(id string) bool { return uuidPattern.MatchString(id) }

// serverClientIDPattern is Pocket ID's own rule for an ordinary client ID:
// validateClientIDRegex, "^[a-zA-Z0-9._-]+$", in
// backend/internal/dto/validations.go, identical in v2.14.0 to v2.17.0. The
// create DTO adds min=2,max=128 (OidcClientCreateDto, binding
// "omitempty,client_id,min=2,max=128"), but the server's own ValidateClientID,
// which also decides whether a stored ID is an ordinary one, applies the
// pattern alone, so a response is held to the pattern alone. It accepts "."
// and ".."; every generated client ID, a UUID, matches.
var serverClientIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// isReturnedClientID reports whether id is an ID Pocket ID can hold for an
// OIDC client: an ordinary one (serverClientIDPattern) or a CIMD client's URL
// (isCIMDClientID). Every UUID is one, so this is also the widest form any
// object ID takes.
func isReturnedClientID(id string) bool {
	return serverClientIDPattern.MatchString(id) || isCIMDClientID(id)
}

// isCIMDClientID applies, exactly, the rules a CIMD client's ID passes before
// Pocket ID stores it: ParseCIMDURL in github.com/pocket-id/fosite v1.3.0
// (cimd.go; used by v2.14.0 to v2.17.0, which store the ID as given). The URL
// must parse (net/url, which refuses control characters), use the https
// scheme in any case, name a host, carry no user information, no fragment
// (and no "#" at all), no query (and no bare "?"), and a non-empty path
// whose decoded segments include no "." or "..". Spaces, other printable
// characters and Unicode in the path are allowed, and there is no length
// limit of its own.
func isCIMDClientID(id string) bool {
	u, err := url.Parse(id)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
		return false
	}
	if u.Fragment != "" || u.RawFragment != "" || strings.Contains(id, "#") {
		return false
	}
	if u.RawQuery != "" || u.ForceQuery || u.Path == "" {
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
