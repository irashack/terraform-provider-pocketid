package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		if rateLimitErr, ok := lastErr.(*RateLimitError); ok && rateLimitErr.RetryAfter != "" {
			if requested := time.Duration(parseRetryAfter(rateLimitErr.RetryAfter)) * time.Second; requested > 0 {
				wait = requested
			}
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	// Log response details
	tflog.Debug(ctx, "Pocket-ID API Response", map[string]interface{}{
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"url":         url,
	})

	// Error bodies can echo tokens or secrets. Preserve status, never their contents.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload struct {
			Error   string         `json:"error"`
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		}
		parsed := json.Unmarshal(respBody, &payload) == nil
		missing := resp.StatusCode == 404 && parsed && payload.Error == "API endpoint not found"
		// Pocket-ID's structured errors (dto.ErrorDto) carry a stable "code"
		// alongside the human-readable "error" message. A missing user is
		// reported with the code below by every endpoint that looks one up
		// (confirmed against the pinned v2.14.0/v2.15.0 source: apperror.UserNotFound(),
		// serialized by middleware.ErrorHandlerMiddleware). The unmatched-route
		// 404 above is a bare gin.H with no "code" field, so the two never collide.
		userNotFound := resp.StatusCode == 404 && parsed && payload.Code == "user_not_found"
		// A missing OIDC client is apperror.NotFound("OIDC client"): code
		// "not_found" with details.resource "OIDC client", identical in the
		// v2.14.0 to v2.17.0 source (getClientInternal and DeleteClient). Other
		// resources share the "not_found" code, so the resource must match too.
		resource, _ := payload.Details["resource"].(string)
		clientNotFound := resp.StatusCode == 404 && parsed && payload.Code == "not_found" && resource == "OIDC client"
		if resp.StatusCode == 429 {
			return nil, &RateLimitError{StatusCode: http.StatusTooManyRequests, Message: http.StatusText(http.StatusTooManyRequests), RetryAfter: resp.Header.Get("Retry-After")}
		}
		return nil, &HTTPError{StatusCode: resp.StatusCode, MissingEndpoint: missing, UserNotFound: userNotFound, ClientNotFound: clientNotFound}
	}

	return respBody, nil
}

// parseRetryAfter parses the Retry-After header value
// It can be either a delay in seconds or an HTTP-date
func parseRetryAfter(retryAfter string) int {
	// First try to parse as integer seconds
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
		return seconds
	}

	// Try to parse as HTTP-date
	if t, err := http.ParseTime(retryAfter); err == nil {
		delay := time.Until(t).Seconds()
		if delay > 0 {
			return int(delay)
		}
	}

	// Default to 60 seconds if we can't parse
	return 60
}
