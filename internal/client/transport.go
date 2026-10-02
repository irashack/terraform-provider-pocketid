package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Client represents a Pocket-ID API client
type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
	retry      retryPolicy
}

// NewClient creates a new Pocket-ID API client
func NewClient(baseURL, apiToken string, skipTLSVerify bool, timeout int64) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if apiToken == "" {
		return nil, fmt.Errorf("API token is required")
	}

	// Configure HTTP client with TLS settings
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			// Allow users to skip TLS verification for development environments
			// This is controlled by provider configuration and defaults to false
			InsecureSkipVerify: skipTLSVerify, // #nosec G402 - Legitimate use case for development
		},
	}

	return &Client{
		baseURL:  baseURL,
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout:       time.Duration(timeout) * time.Second,
			Transport:     transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Retry limits for reads. Only GET is ever retried; a mutation is sent once.
const (
	// maxReadAttempts is the most times one GET is sent.
	maxReadAttempts = 4
	// maxRetryWait caps a single wait between attempts. A server asking for a
	// longer wait (Retry-After) gets no retry: the error is returned at once.
	maxRetryWait = 10 * time.Second
	// maxRetryElapsed bounds the time a GET spends retrying: no wait starts
	// that would end more than this long after the first attempt began, nor
	// after the context's deadline, whichever is earlier.
	maxRetryElapsed = 30 * time.Second
)

// retryPolicy holds the limits above. It is a field so tests can shorten it.
type retryPolicy struct {
	maxAttempts int
	backoffUnit time.Duration
	maxWait     time.Duration
	maxElapsed  time.Duration
}

var defaultRetryPolicy = retryPolicy{
	maxAttempts: maxReadAttempts,
	backoffUnit: time.Second,
	maxWait:     maxRetryWait,
	maxElapsed:  maxRetryElapsed,
}

// doRequest performs an HTTP request to the Pocket-ID API. ctx bounds the
// whole call: cancelling it aborts an in-flight request and any wait before a
// retry, and its deadline also bounds how long a GET keeps retrying.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	policy := c.retry
	if policy.maxAttempts == 0 {
		policy = defaultRetryPolicy
	}
	retryDeadline := time.Now().Add(policy.maxElapsed)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(retryDeadline) {
		retryDeadline = deadline
	}

	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, fmt.Errorf("request abandoned before attempt %d: %w (previous attempt: %w)", attempt, err, lastErr)
			}
			return nil, fmt.Errorf("request not sent: %w", err)
		}

		respBody, err := c.doSingleRequest(ctx, method, endpoint, body)
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
		if time.Now().Add(wait).After(retryDeadline) {
			return nil, fmt.Errorf("not retrying: waiting %s would pass the time allowed for this request after %d attempt(s): %w", wait, attempt, lastErr)
		}

		tflog.Warn(ctx, "Request failed with retryable error", map[string]interface{}{
			"error":        err.Error(),
			"attempt":      attempt,
			"max_attempts": policy.maxAttempts,
			"backoff":      wait.String(),
		})

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("context cancelled during retry backoff: %w (previous attempt: %w)", ctx.Err(), lastErr)
		}
	}
}

// isRetryableError determines if an error is retryable
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Network errors are retryable
	if strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "timeout") {
		return true
	}

	// 5xx errors are retryable
	if strings.Contains(errStr, "HTTP 502") ||
		strings.Contains(errStr, "HTTP 503") ||
		strings.Contains(errStr, "HTTP 504") ||
		strings.Contains(errStr, "HTTP 500") {
		return true
	}

	// 429 Too Many Requests is retryable (rate limiting)
	if strings.Contains(errStr, "HTTP 429") {
		return true
	}

	// Check if it's a RateLimitError
	if _, ok := err.(*RateLimitError); ok {
		return true
	}

	return false
}

// doSingleRequest performs a single HTTP request without retries
func (c *Client) doSingleRequest(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("error marshaling request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	url := fmt.Sprintf("%s%s", c.baseURL, endpoint)
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-KEY", c.apiToken) // Note: Using X-API-KEY header, not Authorization Bearer

	// Log request details (excluding sensitive headers)
	tflog.Debug(ctx, "Pocket-ID API Request", map[string]interface{}{
		"method":   method,
		"url":      url,
		"endpoint": endpoint,
		"headers": map[string]string{
			"Content-Type": req.Header.Get("Content-Type"),
			"Accept":       req.Header.Get("Accept"),
			"X-API-KEY":    "[REDACTED]",
		},
	})

	resp, err := c.httpClient.Do(req)
	if err != nil {
		tflog.Error(ctx, "HTTP Request Failed", map[string]interface{}{
			"error": err.Error(),
			"url":   url,
		})
		return nil, fmt.Errorf("error making request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			tflog.Warn(ctx, "Failed to close response body", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	limit := int64(maxErrorBodyBytes)
	if ok {
		limit = maxResponseBodyBytes
	}
	respBody, readErr := readBounded(resp, limit)

	// Log response details
	tflog.Debug(ctx, "Pocket-ID API Response", map[string]interface{}{
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"url":         url,
	})

	if ok {
		if readErr != nil {
			return nil, readErr
		}
		return respBody, nil
	}

	// Error bodies can echo tokens or secrets. Preserve status, never their
	// contents. A body that could not be read in full is not parsed, so it
	// can never be taken for one of Pocket ID's structured errors.
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &RateLimitError{
			StatusCode: http.StatusTooManyRequests,
			Message:    http.StatusText(http.StatusTooManyRequests),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	return nil, classifyError(resp.StatusCode, respBody, readErr == nil)
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

// errResponseTooLarge reports a body over its limit. It never carries any of
// the body's content.
var errResponseTooLarge = errors.New("response body exceeds the size limit")

// readBounded reads at most limit bytes of the response body. A larger body
// (by its Content-Length, or by what arrives) is an error that does not
// include any of it.
func readBounded(resp *http.Response, limit int64) ([]byte, error) {
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("HTTP %d: %w (%d bytes declared, limit %d); body not read", resp.StatusCode, errResponseTooLarge, resp.ContentLength, limit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("HTTP %d: %w (limit %d bytes); body discarded", resp.StatusCode, errResponseTooLarge, limit)
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
