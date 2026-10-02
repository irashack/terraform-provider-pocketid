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

// doRequest performs an HTTP request to the Pocket-ID API
func (c *Client) doRequest(method, endpoint string, body interface{}) ([]byte, error) {
	return c.doRequestWithContext(context.Background(), method, endpoint, body)
}

// doRequestWithContext performs an HTTP request to the Pocket-ID API with context support
func (c *Client) doRequestWithContext(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		var backoff time.Duration

		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff = time.Duration(1<<(attempt-1)) * time.Second

			// Special handling for rate limit errors to respect Retry-After header
			if rateLimitErr, ok := lastErr.(*RateLimitError); ok && rateLimitErr.RetryAfter != "" {
				retryAfterSeconds := parseRetryAfter(rateLimitErr.RetryAfter)
				if retryAfterSeconds > 0 {
					backoff = time.Duration(retryAfterSeconds) * time.Second
					tflog.Info(ctx, "Rate limited, using Retry-After header", map[string]interface{}{
						"retry_after":     rateLimitErr.RetryAfter,
						"backoff_seconds": retryAfterSeconds,
					})
				}
			}

			tflog.Debug(ctx, "Retrying request after backoff", map[string]interface{}{
				"attempt": attempt,
				"backoff": backoff.String(),
			})

			select {
			case <-time.After(backoff):
				// Continue with retry
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
			}
		}

		respBody, err := c.doSingleRequest(ctx, method, endpoint, body)
		if err == nil {
			return respBody, nil
		}

		lastErr = err

		// Determine if error is retryable
		if method != http.MethodGet || !isRetryableError(err) {
			return nil, err
		}

		tflog.Warn(ctx, "Request failed with retryable error", map[string]interface{}{
			"error":        err.Error(),
			"attempt":      attempt + 1,
			"max_attempts": maxRetries + 1,
		})
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", maxRetries+1, lastErr)
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
