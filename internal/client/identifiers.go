package client

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
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
// included. An ID the caller supplied must come back exactly. Anything else
// is refused, with an error that never includes the returned value: a server
// could put text it received (the API key) there, and an accepted ID is
// logged and put into URLs.
func checkCreatedID(kind, requested, returned string) error {
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
