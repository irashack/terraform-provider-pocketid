package client

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorResponse represents an error response from the Pocket-ID API
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// HTTPError exposes status without leaking an API error body.
type HTTPError struct {
	StatusCode      int
	MissingEndpoint bool
	// UserNotFound is set only for a 404 whose body is Pocket-ID's own
	// structured "user_not_found" error code - a positive identification
	// that the user is gone, as opposed to any other 404 (a wrong base URL,
	// a proxy's generic not-found page, or an endpoint that doesn't exist on
	// an older server). See IsUserNotFound.
	UserNotFound bool
	// ClientNotFound is set only for a 404 whose body is Pocket-ID's own
	// structured not-found error for an OIDC client. See IsOIDCClientNotFound.
	ClientNotFound bool
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// RateLimitError represents a 429 rate limit error with optional Retry-After information
type RateLimitError struct {
	StatusCode int
	Message    string
	RetryAfter string // Can be seconds or HTTP-date
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("HTTP %d: %s (Retry-After: %s)", e.StatusCode, e.Message, e.RetryAfter)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// IsUserNotFound reports whether err is a confirmed "no such user" response
// from Pocket-ID - never true for a generic or malformed 404, which must be
// treated as a real error rather than assumed to mean the user is gone.
func IsUserNotFound(err error) bool {
	var status *HTTPError
	return errors.As(err, &status) && status.UserNotFound
}

// IsOIDCClientNotFound reports whether err is a confirmed "no such OIDC
// client" response from Pocket-ID. A bare, proxy or missing-route 404 does
// not prove the client is gone and never satisfies it.
func IsOIDCClientNotFound(err error) bool {
	var status *HTTPError
	return errors.As(err, &status) && status.ClientNotFound
}

// IsDefiniteRejection excludes transport failures and server errors: they may
// occur after a mutation committed. Such results require read-only inspection.
func IsDefiniteRejection(err error) bool {
	var status *HTTPError
	return errors.As(err, &status) && status.StatusCode >= 400 && status.StatusCode < 500 && status.StatusCode != 408
}
