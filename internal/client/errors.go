package client

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrorResponse represents an error response from the Pocket-ID API
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// HTTPError is a non-2xx answer other than 429. It never carries the body's
// text: its message names only the status.
//
// When the body is Pocket ID's structured error (dto.ErrorDto, written by
// middleware.ErrorHandlerMiddleware: {"error", "code", "details",
// "request_id"}), Code and Resource hold its "code" and "details.resource".
// The shape is identical in v2.14.0 through v2.17.0. Both come from the
// response, so they are kept only when they look like Pocket ID's own values
// (lower-case code, a short plain resource name); compare them, do not print
// them.
type HTTPError struct {
	StatusCode int
	// Code is Pocket ID's stable error code, such as "not_found",
	// "user_not_found" or "already_in_use"; empty when the body was not its
	// structured error.
	Code string
	// Resource is details.resource, such as "OIDC client" or "User group",
	// which Pocket ID sets on its generic "not_found" error.
	Resource string
	// MissingEndpoint is set for the router's own 404 for an unknown /api
	// route, {"error":"API endpoint not found"} with no code. It means the
	// route does not exist (an older server, a wrong base URL), never that an
	// object is gone.
	MissingEndpoint bool
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// ErrResultUnread marks an error returned after the server accepted a
// mutation, when what it now holds could not be read back. The change was
// made; only its result is unknown. Test with errors.Is.
var ErrResultUnread = errors.New("the server accepted the change, but its result could not be read")

// Resource identifies a kind of Pocket ID object the way its not-found error
// names it. Most kinds share the code "not_found" and differ only in
// details.resource (apperror.NotFound(name)); a few have a code of their own
// with no details (apperror.UserNotFound, APIKeyNotFound, ImageNotFound).
//
// A new kind that follows the same pattern can be declared in the file that
// needs it: client.Resource{Code: "not_found", Name: "Signup token"}. Check
// the server's apperror call for the exact name first.
type Resource struct {
	// Code is the error code a missing object of this kind is reported with.
	Code string
	// Name is the details.resource value that must match when Code is
	// "not_found"; ignored otherwise.
	Name string
}

// The kinds Pocket ID v2.14.0 to v2.17.0 report as missing (the apperror
// constructors called from its services and handlers).
var (
	ResourceUser                = Resource{Code: "user_not_found"}
	ResourceUserGroup           = Resource{Code: "not_found", Name: "User group"}
	ResourceOIDCClient          = Resource{Code: "not_found", Name: "OIDC client"}
	ResourceClientSecret        = Resource{Code: "not_found", Name: "Client secret"}
	ResourceSCIMServiceProvider = Resource{Code: "not_found", Name: "SCIM service provider"}
	ResourceAPI                 = Resource{Code: "not_found", Name: "API"}
	ResourcePasskey             = Resource{Code: "not_found", Name: "Passkey"}
	ResourceAPIKey              = Resource{Code: "api_key_not_found"}
	ResourceImage               = Resource{Code: "image_not_found"}
)

const codeNotFound = "not_found"

// IsNotFound reports whether err is Pocket ID's own confirmation that an
// object of kind r does not exist: HTTP 404 with r's code and, for the shared
// "not_found" code, r's details.resource. Nothing else satisfies it: not the
// router's "API endpoint not found", not a proxy's or load balancer's 404 page,
// not a body too large to read, not another kind's not-found error, and not a
// transport failure.
//
// Which object the error is about depends on the request, so pass the kind
// whose absence the caller needs confirmed. A request through a parent can
// report the parent: DELETE /oidc/clients/{id}/secrets/{secretId} answers
// ResourceOIDCClient when the client is gone and ResourceClientSecret when
// only the secret is. And a lookup by a parent's ID can report the child even
// when the parent is gone: GET /oidc/clients/{id}/scim-service-provider
// answers ResourceSCIMServiceProvider for a missing client too.
func IsNotFound(err error, r Resource) bool {
	if r.Code == "" {
		return false
	}
	var status *HTTPError
	if !errors.As(err, &status) || status.StatusCode != http.StatusNotFound || status.Code != r.Code {
		return false
	}
	return r.Code != codeNotFound || (r.Name != "" && status.Resource == r.Name)
}

// RateLimitError is an HTTP 429 answer. RetryAfter is the delay the server
// asked for, parsed and bounded when the response was read (0 when it named
// none or an unusable one); the header's raw text is never kept.
type RateLimitError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("HTTP %d: %s (retry after %s)", e.StatusCode, e.Message, e.RetryAfter)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// IsUserNotFound is IsNotFound(err, ResourceUser).
func IsUserNotFound(err error) bool {
	return IsNotFound(err, ResourceUser)
}

// IsOIDCClientNotFound is IsNotFound(err, ResourceOIDCClient).
func IsOIDCClientNotFound(err error) bool {
	return IsNotFound(err, ResourceOIDCClient)
}

// IsDefiniteRejection excludes transport failures and server errors: they may
// occur after a mutation committed. Such results require read-only inspection.
func IsDefiniteRejection(err error) bool {
	var status *HTTPError
	return errors.As(err, &status) && status.StatusCode >= 400 && status.StatusCode < 500 && status.StatusCode != 408
}
