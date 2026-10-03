package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Client represents a Pocket-ID API client
type Client struct {
	baseURL string
	// apiToken is the API key exactly as the server receives it (see
	// normalizeAPIKey). Everything that sends, guards or compares the key
	// uses this value.
	apiToken   string
	httpClient *http.Client
	retry      retryPolicy
}

// NewClient creates a new Pocket-ID API client. The API token is used in the
// form the server receives it: without surrounding spaces and tabs.
func NewClient(baseURL, apiToken string, skipTLSVerify bool, timeout int64) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if apiToken == "" {
		return nil, fmt.Errorf("API token is required")
	}
	apiToken = normalizeAPIKey(apiToken)
	if apiToken == "" {
		return nil, fmt.Errorf("API token is required: the configured value is only spaces and tabs")
	}

	// See newTransport for how connections are handled.
	transport := newTransport(&tls.Config{
		// Allow users to skip TLS verification for development environments
		// This is controlled by provider configuration and defaults to false
		InsecureSkipVerify: skipTLSVerify, // #nosec G402 - Legitimate use case for development
	})

	// A read, retries included, may always use one full configured timeout.
	retry := defaultRetryPolicy
	if configured := time.Duration(timeout) * time.Second; configured > retry.maxElapsed {
		retry.maxElapsed = configured
	}

	return &Client{
		baseURL:  baseURL,
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout:       time.Duration(timeout) * time.Second,
			Transport:     transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
		},
		retry: retry,
	}, nil
}

// normalizeAPIKey returns the API key as it goes out on the wire. Go writes
// a header value without the optional whitespace HTTP allows around it
// (spaces and tabs; net/http's Header.Write trims them), and refuses to send
// a value with a CR, LF or other control character at all, so the key the
// server receives is the configured one with surrounding spaces and tabs
// removed. Comparing a response with any other form would miss the key the
// server actually got.
func normalizeAPIKey(key string) string {
	return strings.Trim(key, " \t")
}

// Retry limits for reads. Only GET is ever retried; a mutation is sent once.
const (
	// maxReadAttempts is the most application-level attempts one GET gets.
	// Each attempt is one request on a fresh connection (DisableKeepAlives),
	// so it is also the most times the GET goes out on the wire: Go's
	// transport has no reused connection to replay it on.
	maxReadAttempts = 4
	// maxRetryWait caps a single wait between attempts. A server asking for a
	// longer wait (Retry-After) gets no retry: the error is returned at once.
	maxRetryWait = 10 * time.Second
	// maxRetryElapsed bounds a whole GET, retries included: its attempts run
	// under a deadline this long after the first one began (or the context's
	// own deadline, if earlier), and no wait starts that would not end before
	// it, since the attempt after the wait needs time too.
	// NewClient raises it to the configured HTTP timeout when that is
	// longer, so a single attempt can always use the full timeout.
	maxRetryElapsed = 30 * time.Second
)

// retryPolicy holds the limits above. It is a field so tests can shorten it;
// the retry arithmetic is tested in fake time (testing/synctest), so the loop
// uses the real clock throughout.
type retryPolicy struct {
	maxAttempts int
	backoffUnit time.Duration
	maxWait     time.Duration
	maxElapsed  time.Duration
}

// sleepContext waits d, or until ctx is done, whose error it then returns.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var defaultRetryPolicy = retryPolicy{
	maxAttempts: maxReadAttempts,
	backoffUnit: time.Second,
	maxWait:     maxRetryWait,
	maxElapsed:  maxRetryElapsed,
}

// doRequest performs an HTTP request to the Pocket-ID API. ctx bounds the
// whole call: cancelling it aborts an in-flight request and any wait before a
// retry. A GET, its retries included, also ends at the retry deadline (see
// maxRetryElapsed); a mutation is sent once and bounded by the HTTP timeout.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	policy := c.retry
	if policy.maxAttempts == 0 {
		policy = defaultRetryPolicy
	}
	start := time.Now()
	retryDeadline := start.Add(policy.maxElapsed)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(retryDeadline) {
		retryDeadline = deadline
	}
	if method == http.MethodGet {
		// The deadline applies to the attempts themselves, not only to
		// whether another one starts: a slow attempt is cut off too.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, retryDeadline.Sub(start))
		defer cancel()
	}
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("error marshaling request body: %w", err)
		}
		payload = encoded
	}

	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, fmt.Errorf("request abandoned before attempt %d: %w (previous attempt: %w)", attempt, err, lastErr)
			}
			return nil, fmt.Errorf("request not sent: %w", err)
		}

		respBody, err := c.send(ctx, method, endpoint, "application/json", payload)
		if err == nil {
			return respBody, nil
		}
		lastErr = err

		// Mutations are never retried, and neither is anything once the
		// caller has given up.
		if method != http.MethodGet || ctx.Err() != nil || !isRetryableError(err) {
			return nil, err
		}
		if attempt >= policy.maxAttempts {
			return nil, fmt.Errorf("request failed after %d attempts: %w", attempt, lastErr)
		}

		// Exponential backoff (1, 2, 4 units) unless the server asked for a
		// specific wait.
		wait := policy.backoffUnit * time.Duration(1<<(attempt-1))
		var rateLimitErr *RateLimitError
		if errors.As(lastErr, &rateLimitErr) && rateLimitErr.RetryAfter > 0 {
			wait = rateLimitErr.RetryAfter
		}
		if wait > policy.maxWait {
			return nil, fmt.Errorf("not retrying: the server asked to wait %s, longer than the %s this provider waits: %w", wait, policy.maxWait, lastErr)
		}
		if !time.Now().Add(wait).Before(retryDeadline) {
			return nil, fmt.Errorf("not retrying: waiting %s would use up the time allowed for this request after %d attempt(s): %w", wait, attempt, lastErr)
		}

		tflog.Warn(ctx, "Request failed with retryable error", map[string]interface{}{
			"error":        err.Error(),
			"attempt":      attempt,
			"max_attempts": policy.maxAttempts,
			"backoff":      wait.String(),
		})

		if err := sleepContext(ctx, wait); err != nil {
			return nil, fmt.Errorf("context cancelled during retry backoff: %w (previous attempt: %w)", err, lastErr)
		}
	}
}

// isRetryableError reports whether a failed GET may be sent again: a rate
// limit, a 500/502/503/504, or a connection that was refused, reset, timed
// out at the network level or could not resolve the host. A cancelled
// request, an expired deadline (including the HTTP client's own timeout) and
// a response that was not valid HTTP are not retried.
func isRetryableError(err error) bool {
	var rateLimited *RateLimitError
	if errors.As(err, &rateLimited) {
		return true
	}
	var status *HTTPError
	if errors.As(err, &status) {
		switch status.StatusCode {
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	var transport *TransportError
	if errors.As(err, &transport) {
		return transport.retryable
	}
	var body *ResponseBodyError
	if errors.As(err, &body) {
		return body.retryable
	}
	return false
}

// send performs one HTTP request, never retried: the body (payload, sent with
// contentType; nil for none) goes out once, and the response is read within
// its size limit and classified. Neither the request nor the response body is
// logged, and errors carry only the status and Pocket ID's error code.
func (c *Client) send(ctx context.Context, method, endpoint, contentType string, payload []byte) ([]byte, error) {
	body, _, err := c.sendWith(ctx, method, endpoint, contentType, payload, sendOptions{})
	return body, err
}

// sendOptions adjusts one send. The zero value is the JSON exchange every
// API method uses.
type sendOptions struct {
	// accept is the Accept header; empty means application/json.
	accept string
	// headers are extra request headers. They cannot replace Content-Type,
	// Accept or X-API-KEY.
	headers http.Header
	// maxBody limits a 2xx body; 0 means maxResponseBodyBytes.
	maxBody int64
}

// sendWith is send with options, and also returns the response's
// Content-Type header (server-controlled: compare it, do not log it).
func (c *Client) sendWith(ctx context.Context, method, endpoint, contentType string, payload []byte, options sendOptions) ([]byte, string, error) {
	// An identifier from configuration, state or import that carries the API
	// key never reaches a URL, a log line or an error: the request is refused
	// before it is built.
	if err := c.checkEndpoint(endpoint); err != nil {
		return nil, "", err
	}

	// The HTTP timeout is applied to the request's context as well as to the
	// HTTP client (whose own deadline it reaches first), so that setting up
	// the connection, which Go's transport does on a context detached from
	// the request's, is bounded by it too (see withRequestContext).
	if timeout := c.httpClient.Timeout; timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ctx = withRequestContext(ctx)

	url := fmt.Sprintf("%s%s", c.baseURL, endpoint)
	req, err := newRequest(ctx, method, url, payload)
	if err != nil {
		return nil, "", err
	}

	// Set headers
	accept := options.accept
	if accept == "" {
		accept = "application/json"
	}
	logged := map[string]string{}
	for name, values := range options.headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
		logged[http.CanonicalHeaderKey(name)] = req.Header.Get(name)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-API-KEY", c.apiToken) // Note: Using X-API-KEY header, not Authorization Bearer
	// Ask the server to close the connection too (see newTransport).
	req.Close = true
	logged["Content-Type"] = req.Header.Get("Content-Type")
	logged["Accept"] = accept
	logged["X-API-KEY"] = "[REDACTED]"

	// Log request details (excluding sensitive headers)
	tflog.Debug(ctx, "Pocket-ID API Request", map[string]interface{}{
		"method":   method,
		"url":      url,
		"endpoint": endpoint,
		"headers":  logged,
	})

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// The transport's error can quote what the server sent (a malformed
		// status line or header), so only a fixed description of it is
		// logged or returned.
		failure := newTransportError(method, endpoint, "no response", err)
		tflog.Error(ctx, "HTTP Request Failed", map[string]interface{}{
			"error": failure.Error(),
			"url":   url,
		})
		return nil, "", failure
	}
	defer func() { _ = resp.Body.Close() }()

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	limit := int64(maxErrorBodyBytes)
	if ok {
		limit = maxResponseBodyBytes
		if options.maxBody > 0 {
			limit = options.maxBody
		}
	}
	respBody, readErr := readBounded(resp, limit)

	// Log response details. resp.Status would include the server's own
	// reason phrase, which can be anything (even the key it received), so
	// only the code and its standard text are logged.
	tflog.Debug(ctx, "Pocket-ID API Response", map[string]interface{}{
		"status_code": resp.StatusCode,
		"status":      http.StatusText(resp.StatusCode),
		"url":         url,
	})

	if ok {
		if readErr != nil {
			// A 2xx to a mutation means the server accepted it: only its
			// result is unknown, and the request must not be repeated.
			readErr.Accepted = method != http.MethodGet && method != http.MethodHead
			return nil, "", readErr
		}
		return respBody, resp.Header.Get("Content-Type"), nil
	}

	// Error bodies can echo tokens or secrets. Preserve status, never their
	// contents. A body that could not be read in full is not parsed, so it
	// can never be taken for one of Pocket ID's structured errors.
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", &RateLimitError{
			StatusCode: http.StatusTooManyRequests,
			Message:    http.StatusText(http.StatusTooManyRequests),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	return nil, "", classifyError(resp.StatusCode, respBody, readErr == nil)
}

var (
	errorCodePattern     = regexp.MustCompile(`^[a-z][a-z_]{0,63}$`)
	errorResourcePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z ]{0,63}$`)
)

// classifyError builds the HTTPError for a non-2xx, non-429 response. body is
// used only when complete is true (it was read in full within its limit); a
// truncated or unread body never yields a code.
func classifyError(status int, body []byte, complete bool) *HTTPError {
	result := &HTTPError{StatusCode: status}
	if !complete {
		return result
	}
	var payload struct {
		Error   string         `json:"error"`
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return result
	}
	// The router's 404 for an unknown /api route is a bare gin.H with no
	// code (frontend/frontend_included.go), so it never collides with a
	// structured error.
	result.MissingEndpoint = status == http.StatusNotFound && payload.Code == "" && payload.Error == "API endpoint not found"
	if errorCodePattern.MatchString(payload.Code) {
		result.Code = payload.Code
		if resource, ok := payload.Details["resource"].(string); ok && errorResourcePattern.MatchString(resource) {
			result.Resource = resource
		}
	}
	return result
}

// Response size limits. Pocket ID's largest admin responses (a 100-item
// page of users with their groups and claims, the full application
// configuration) are far below the first; its error bodies are a short JSON
// object.
const (
	maxResponseBodyBytes = 16 << 20
	maxErrorBodyBytes    = 64 << 10
)

// ErrUndecodableResponse is a response body that is not the JSON a method
// expects. It is returned alone: the JSON decoder's own error is dropped,
// because it can quote the response (a numeric literal that does not fit, the
// type of a value), and a server can make that literal as long as the
// response limit allows.
var ErrUndecodableResponse = errors.New("error unmarshaling response: the response is not the JSON this provider expects")

// undecodableResultError is a 2xx answer to a mutation whose body is not the
// JSON expected: the server made the change and only its result is unknown,
// so it wraps ErrResultUnread as well as ErrUndecodableResponse. Like those,
// it carries fixed text only, never the decoder's error.
type undecodableResultError struct{ message string }

func (e undecodableResultError) Error() string { return e.message }

func (undecodableResultError) Unwrap() []error {
	return []error{ErrResultUnread, ErrUndecodableResponse}
}

var errUndecodableResult error = undecodableResultError{
	message: "error unmarshaling response: the server accepted the change, but its response is not the JSON this provider expects; inspect the object before trying again",
}

// decodeResult decodes the body of a 2xx answer to a mutation into v. When it
// cannot, the change was made but its result is unknown: the error wraps
// ErrResultUnread and ErrUndecodableResponse, and nothing else. Reads use
// decodeResponse.
func decodeResult(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return errUndecodableResult
	}
	return nil
}

// decodeResponse decodes a JSON response body into v, returning
// ErrUndecodableResponse, and nothing else, when it cannot.
func decodeResponse(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return ErrUndecodableResponse
	}
	return nil
}

// errResponseTooLarge reports a body over its limit. It never carries any of
// the body's content.
var errResponseTooLarge = errors.New("response body exceeds the size limit")

// ResponseBodyError is a response whose status arrived but whose body could
// not be read in full: it was over the size limit (by its declared length or
// as it arrived) or the read was interrupted. Its message never contains any
// of the body.
//
// For a 2xx answer to a mutation, Accepted is set and the error also wraps
// ErrResultUnread: the server accepted the change and only its result is
// unknown, so the request must not be repeated. For a GET it is simply a
// failed read.
type ResponseBodyError struct {
	StatusCode int
	// Reason is a fixed description of what went wrong.
	Reason string
	// TooLarge is set when the body was over the size limit.
	TooLarge bool
	// Accepted is set for a 2xx answer to a mutation.
	Accepted  bool
	causes    []error
	retryable bool
}

func (e *ResponseBodyError) Error() string {
	message := fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Reason)
	if e.Accepted {
		message += "; " + ErrResultUnread.Error()
	}
	return message
}

// Unwrap gives errResponseTooLarge or the read failure's classification (a
// context error, the bare syscall.Errno, io.ErrUnexpectedEOF), and
// ErrResultUnread when Accepted.
func (e *ResponseBodyError) Unwrap() []error {
	causes := append([]error(nil), e.causes...)
	if e.Accepted {
		causes = append(causes, ErrResultUnread)
	}
	return causes
}

// readBounded reads at most limit bytes of the response body. A larger body
// (by its Content-Length, or by what arrives) or an interrupted read is a
// ResponseBodyError that does not include any of it.
func readBounded(resp *http.Response, limit int64) ([]byte, *ResponseBodyError) {
	if resp.ContentLength > limit {
		return nil, &ResponseBodyError{
			StatusCode: resp.StatusCode, TooLarge: true, causes: []error{errResponseTooLarge},
			Reason: fmt.Sprintf("%s (%d bytes declared, limit %d); body not read", errResponseTooLarge, resp.ContentLength, limit),
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		reason, causes, retryable := classifyTransportError(err)
		return nil, &ResponseBodyError{StatusCode: resp.StatusCode, Reason: "reading the response body failed: " + reason, causes: causes, retryable: retryable}
	}
	if int64(len(body)) > limit {
		return nil, &ResponseBodyError{
			StatusCode: resp.StatusCode, TooLarge: true, causes: []error{errResponseTooLarge},
			Reason: fmt.Sprintf("%s (limit %d bytes); body discarded", errResponseTooLarge, limit),
		}
	}
	return body, nil
}

// maxRetryAfter is the largest delay parseRetryAfter reports. Anything this
// long is far beyond maxRetryWait and only needs to read as "too long".
const maxRetryAfter = 7 * 24 * time.Hour

// parseRetryAfter turns a Retry-After header (delay-seconds or an HTTP-date)
// into a delay between 0 and maxRetryAfter. It is called where the header is
// read, so the raw value is never stored, logged or put in an error. An
// absent, malformed or past value is 0: the caller then uses its own backoff.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds >= int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := at.Sub(now)
		switch {
		case delay <= 0:
			return 0
		case delay > maxRetryAfter:
			return maxRetryAfter
		default:
			// Round up: waiting a fraction too long is harmless, too short is not.
			return delay.Truncate(time.Second) + time.Second
		}
	}
	return 0
}

// newRequest builds the request send sends. A non-empty payload becomes a
// one-shot body: wrapped so that NewRequest does not recognize a
// *bytes.Reader and set GetBody, so Go's transport has nothing to replay the
// request from; its length is set explicitly so it is sent with
// Content-Length, never chunked. An empty but non-nil payload is
// http.NoBody, Go's marker for an explicitly empty body (Content-Length: 0);
// a nil payload is no body at all.
func newRequest(ctx context.Context, method, url string, payload []byte) (*http.Request, error) {
	var body io.Reader
	switch {
	case len(payload) > 0:
		body = struct{ io.Reader }{bytes.NewReader(payload)}
	case payload != nil:
		body = http.NoBody
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}
	if len(payload) > 0 {
		req.ContentLength = int64(len(payload))
	}
	return req, nil
}

// TransportError is a request that got no usable HTTP response: the
// connection failed or timed out, the request was cancelled, or what came back
// was not valid HTTP. Its message is built from a fixed set of descriptions,
// never from the underlying error, whose text can quote what the server sent
// (Go's HTTP client includes a malformed status line or header in it).
// Unwrap gives only a classification: context.Canceled,
// context.DeadlineExceeded or a connection errno, so errors.Is keeps working.
type TransportError struct {
	Method   string
	Endpoint string
	// Reason is the fixed description of what went wrong.
	Reason    string
	causes    []error
	retryable bool
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("%s %s: %s", e.Method, e.Endpoint, e.Reason)
}

// Unwrap returns the classification, never the original error.
func (e *TransportError) Unwrap() []error { return e.causes }

func newTransportError(method, endpoint, stage string, err error) *TransportError {
	reason, causes, retryable := classifyTransportError(err)
	return &TransportError{Method: method, Endpoint: endpoint, Reason: stage + ": " + reason, causes: causes, retryable: retryable}
}

// isNativeErrno reports whether nativeErrnoClass knows errno.
func isNativeErrno(errno syscall.Errno) bool {
	_, _, ok := nativeErrnoClass(errno)
	return ok
}

// classifyTransportError maps a client or connection error to a fixed
// description, the errors it may wrap, and whether a GET may be retried after
// it. The wrapped errors are only sentinels whose text is fixed: a context
// error (checked first, so cancellation and deadlines keep their meaning; the
// HTTP client's own timeout is a deadline), the bare syscall.Errno the
// failure carried, if any, kept whatever the description and the retry
// decision, and io.ErrUnexpectedEOF for a connection closed early.
func classifyTransportError(err error) (string, []error, bool) {
	var causes []error
	switch {
	case errors.Is(err, context.Canceled):
		causes = append(causes, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		causes = append(causes, context.DeadlineExceeded)
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		causes = append(causes, errno)
	}

	var dnsErr *net.DNSError
	var netErr net.Error
	var certErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	switch {
	case errors.Is(err, context.Canceled):
		return "request cancelled", causes, false
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out (context deadline exceeded)", causes, false
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "no such host", causes, true
	case errno != 0 && isNativeErrno(errno):
		reason, retryable, _ := nativeErrnoClass(errno)
		return reason, causes, retryable
	case errors.As(err, &netErr) && netErr.Timeout():
		return "network timeout", causes, true
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused", causes, true
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset by peer", causes, true
	case errors.Is(err, syscall.EPIPE):
		return "the connection broke while the request was being sent", causes, false
	case errors.Is(err, syscall.ENETUNREACH):
		return "network unreachable", causes, false
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "host unreachable", causes, false
	case errors.As(err, &certErr):
		return "TLS certificate verification failed", causes, false
	case errors.As(err, &recordErr):
		return "TLS handshake failed (the server did not answer with TLS)", causes, false
	case errors.As(err, &dnsErr):
		return "host name lookup failed", causes, false
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "the connection closed before a complete response", append(causes, io.ErrUnexpectedEOF), false
	case errno != 0:
		return "connection failed", causes, false
	default:
		return "no valid HTTP response (malformed response or protocol error)", causes, false
	}
}
