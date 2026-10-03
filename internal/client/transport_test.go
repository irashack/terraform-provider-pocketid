package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name          string
		baseURL       string
		apiToken      string
		skipTLSVerify bool
		timeout       int64
		wantErr       bool
		errMsg        string
	}{
		{
			name:     "valid configuration",
			baseURL:  "https://pocket-id.example.com",
			apiToken: "test-token",
			timeout:  30,
			wantErr:  false,
		},
		{
			name:     "missing base URL",
			baseURL:  "",
			apiToken: "test-token",
			timeout:  30,
			wantErr:  true,
			errMsg:   "base URL is required",
		},
		{
			name:     "missing API token",
			baseURL:  "https://pocket-id.example.com",
			apiToken: "",
			timeout:  30,
			wantErr:  true,
			errMsg:   "API token is required",
		},
		{
			name:          "with TLS skip",
			baseURL:       "https://pocket-id.example.com",
			apiToken:      "test-token",
			skipTLSVerify: true,
			timeout:       60,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := client.NewClient(tt.baseURL, tt.apiToken, tt.skipTLSVerify, tt.timeout)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, c)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, c)
			}
		})
	}
}

func TestClient_ErrorHandling(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		responseBody   string
		expectedErrMsg string
	}{
		{
			name:           "400 Bad Request",
			statusCode:     http.StatusBadRequest,
			responseBody:   `{"error": "Invalid request"}`,
			expectedErrMsg: "HTTP 400: Bad Request",
		},
		{
			name:           "401 Unauthorized",
			statusCode:     http.StatusUnauthorized,
			responseBody:   `{"error": "Invalid API key"}`,
			expectedErrMsg: "HTTP 401: Unauthorized",
		},
		{
			name:           "404 Not Found",
			statusCode:     http.StatusNotFound,
			responseBody:   `{"error": "Client not found"}`,
			expectedErrMsg: "HTTP 404: Not Found",
		},
		{
			name:           "500 Internal Server Error",
			statusCode:     http.StatusInternalServerError,
			responseBody:   `{"error": "Internal server error"}`,
			expectedErrMsg: "HTTP 500: Internal Server Error",
		},
		{
			name:           "Invalid JSON response",
			statusCode:     http.StatusBadRequest,
			responseBody:   `invalid json`,
			expectedErrMsg: "HTTP 400: Bad Request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if _, err := fmt.Fprint(w, tt.responseBody); err != nil {
					t.Fatalf("Failed to write response: %v", err)
				}
			}))
			defer server.Close()

			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			_, err = c.GetClient(context.Background(), "test-client-id")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErrMsg)
		})
	}
}

func TestClient_RetryLogic(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Simulate transient errors for first 2 attempts
			w.WriteHeader(http.StatusServiceUnavailable)
			if _, err := fmt.Fprint(w, `{"error": "Service temporarily unavailable"}`); err != nil {
				t.Fatalf("Failed to write response: %v", err)
			}
			return
		}
		// Success on third attempt
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&client.OIDCClient{
			ID:   "test-client-id",
			Name: "Test Client",
		}); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetClient(context.Background(), "test-client-id")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test-client-id", result.ID)
	assert.Equal(t, 3, attempts, "Should have made 3 attempts")
}

func TestClient_RetryExhaustion(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		// Always return 503
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := fmt.Fprint(w, `{"error": "Service unavailable"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.GetClient(context.Background(), "test-client-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "request failed after 4 attempts")
	assert.Equal(t, 4, attempts, "Should have made 4 attempts (initial + 3 retries)")
}

func TestClient_NonRetryableError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		// Return 404 which is not retryable
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":"API endpoint not found"}`)
		if _, err := fmt.Fprint(w, `{"error": "Not found"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.GetClient(context.Background(), "test-client-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404: Not Found")
	assert.Equal(t, 1, attempts, "Should have made only 1 attempt (no retries for 404)")
}

// A cancelled context aborts a request that is already in flight: the call
// returns as soon as the context is cancelled, not when the server answers.
func TestClient_CancelAbortsInFlightRequest(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err = c.GetClient(ctx, "test-client-id")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 2*time.Second, "the call must end when the context is cancelled")
	assert.Equal(t, int32(1), attempts.Load(), "a cancelled request is not retried")
}

// A cancelled context aborts the wait before a retry.
func TestClient_CancelAbortsRetryWait(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "8")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	_, err = c.GetClient(ctx, "test-client-id")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 2*time.Second, "the retry wait must end when the context is cancelled")
	assert.Equal(t, int32(1), attempts.Load())
}

// A context that is already cancelled sends nothing, so a mutation is never
// started for a caller that has given up.
func TestClient_CancelledContextSendsNoMutation(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = c.DeleteClient(ctx, "test-client-id")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(0), attempts.Load(), "no request may be sent with a cancelled context")
}

// A server asking for a longer wait than the provider allows gets no retry:
// the error is returned at once instead of blocking for the requested time.
func TestClient_OversizedRetryAfterIsNotWaited(t *testing.T) {
	for name, value := range map[string]string{
		"seconds":   "86400",
		"http-date": time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat),
	} {
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Retry-After", value)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()

			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			start := time.Now()
			_, err = c.GetClient(context.Background(), "test-client-id")
			elapsed := time.Since(start)

			require.Error(t, err)
			var rateLimited *client.RateLimitError
			assert.ErrorAs(t, err, &rateLimited)
			assert.Contains(t, err.Error(), "not retrying")
			assert.Less(t, elapsed, 2*time.Second, "an oversized Retry-After must not be waited for")
			assert.Equal(t, int32(1), attempts.Load())
		})
	}
}

// A wait that would end after the context's deadline is not started.
func TestClient_RetryStopsBeforeContextDeadline(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err = c.GetClient(ctx, "test-client-id")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not retrying")
	assert.Contains(t, err.Error(), "HTTP 429")
	assert.Less(t, elapsed, time.Second, "no wait may start that ends after the deadline")
	assert.Equal(t, int32(1), attempts.Load())
}

// The total time a GET spends retrying is bounded even when every single
// wait is short.
func TestClient_RetryTimeIsBounded(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	client.SetRetryPolicyForTest(c, 20, 100*time.Millisecond, time.Second, 1200*time.Millisecond)

	start := time.Now()
	_, err = c.GetClient(context.Background(), "test-client-id")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not retrying")
	assert.Less(t, elapsed, 3*time.Second)
	// The 100ms, 200ms and 400ms waits end by about 700ms, well inside the
	// 1200ms budget (500ms to spare for four requests); the 800ms wait that
	// would follow ends at about 1500ms, past it (300ms to spare).
	assert.Equal(t, int32(4), attempts.Load())
}

func TestClient_RateLimitHandling(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Simulate rate limit for first 2 attempts
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1") // 1 second retry
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := fmt.Fprint(w, `{"error": "Rate limit exceeded"}`); err != nil {
				t.Fatalf("Failed to write response: %v", err)
			}
			return
		}
		// Success on third attempt
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&client.OIDCClient{
			ID:   "test-client-id",
			Name: "Test Client",
		}); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	start := time.Now()
	result, err := c.GetClient(context.Background(), "test-client-id")
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test-client-id", result.ID)
	assert.Equal(t, "Test Client", result.Name)
	assert.Equal(t, 3, attempts)
	// Should have waited at least 2 seconds (2 retries with 1 second each)
	assert.True(t, elapsed >= 2*time.Second, "Expected at least 2 seconds elapsed, got %v", elapsed)
}

func TestClient_RateLimitWithoutRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			// Simulate rate limit without Retry-After header
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := fmt.Fprint(w, `{"error": "Rate limit exceeded"}`); err != nil {
				t.Fatalf("Failed to write response: %v", err)
			}
			return
		}
		// Success on second attempt
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&client.OIDCClient{
			ID:   "test-client-id",
			Name: "Test Client",
		}); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetClient(context.Background(), "test-client-id")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test-client-id", result.ID)
	assert.Equal(t, 2, attempts)
}

// Test rate limiting with Retry-After header (numeric seconds)
func TestClient_RateLimitWithRetryAfterSeconds(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.Header().Set("Retry-After", "2") // 2 seconds
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := fmt.Fprint(w, `{"error": "Rate limit exceeded"}`); err != nil {
				t.Fatalf("Failed to write response: %v", err)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"id": "test-id", "name": "Test Client"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	start := time.Now()
	result, err := c.GetClient(context.Background(), "test-id")
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 2, attempts)
	// Should wait approximately 2 seconds
	assert.True(t, elapsed >= 1900*time.Millisecond && elapsed <= 2500*time.Millisecond,
		"Expected wait time around 2 seconds, got %v", elapsed)
}

// Test timeout handling
func TestClient_RequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than client timeout
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, `{"id": "test-id"}`); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	// Create client with 1 second timeout
	c, err := client.NewClient(server.URL, "test-token", false, 1)
	require.NoError(t, err)

	start := time.Now()
	result, err := c.GetClient(context.Background(), "test-id")
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "deadline exceeded")
	// Should timeout after approximately 1 second
	assert.True(t, elapsed >= 900*time.Millisecond && elapsed <= 1500*time.Millisecond,
		"Expected timeout around 1 second, got %v", elapsed)
}

// Test error response with empty body
func TestClient_ErrorResponseEmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// No body
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	result, err := c.GetClient(context.Background(), "test-id")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "HTTP 500")
}

// Test with non-retryable errors
func TestClient_NonRetryableErrors(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		attempts   int
	}{
		{
			name:       "400 Bad Request",
			statusCode: http.StatusBadRequest,
			attempts:   1,
		},
		{
			name:       "401 Unauthorized",
			statusCode: http.StatusUnauthorized,
			attempts:   1,
		},
		{
			name:       "403 Forbidden",
			statusCode: http.StatusForbidden,
			attempts:   1,
		},
		{
			name:       "404 Not Found",
			statusCode: http.StatusNotFound,
			attempts:   1,
		},
		{
			name:       "409 Conflict",
			statusCode: http.StatusConflict,
			attempts:   1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.WriteHeader(tc.statusCode)
				if _, err := fmt.Fprintf(w, `{"error": "Error %d"}`, tc.statusCode); err != nil {
					t.Fatalf("Failed to write response: %v", err)
				}
			}))
			defer server.Close()

			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			result, err := c.GetClient(context.Background(), "test-id")
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), fmt.Sprintf("HTTP %d", tc.statusCode))
			assert.Equal(t, tc.attempts, attempts, "Should not retry for %d errors", tc.statusCode)
		})
	}
}

// A slow GET ends when the time allowed for the whole read runs out, even in
// the middle of an attempt, rather than when that attempt would have ended.
func TestClient_SlowReadEndsAtTheRetryDeadline(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	client.SetRetryPolicyForTest(c, 4, 50*time.Millisecond, time.Second, 500*time.Millisecond)

	start := time.Now()
	_, err = c.GetClient(context.Background(), "test-client-id")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, elapsed, 400*time.Millisecond, "the attempt ran until the deadline")
	assert.Less(t, elapsed, 10*time.Second, "the attempt is cut off at the 500ms deadline, not after the 30s HTTP timeout")
	assert.Equal(t, int32(1), attempts.Load())
}

// The deadline bounds reads only: a slow read within it succeeds, and a
// mutation is bounded by the HTTP timeout alone.
func TestClient_RetryDeadlineLeavesFastReadsAndMutationsAlone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			time.Sleep(2 * time.Second) // well past the read deadline below
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"test-client-id","name":"n"}`)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	// An immediate local answer fits a 1s read deadline with room to spare.
	client.SetRetryPolicyForTest(c, 4, 50*time.Millisecond, time.Second, time.Second)

	_, err = c.GetClient(context.Background(), "test-client-id")
	require.NoError(t, err)
	_, err = c.UpdateClient(context.Background(), "test-client-id", &client.OIDCClientCreateRequest{Name: "n"})
	require.NoError(t, err, "a mutation slower than the read deadline still completes")
}

// A response the decoder rejects never reaches the error text: the decoder's
// own message can quote a literal from the response (here a number too large
// for its field, which a server can make as long as it likes), so the
// methods return ErrUndecodableResponse alone.
func TestClient_DecodeErrorsNeverQuoteTheResponse(t *testing.T) {
	const literal = "98765432109876543210987654321098765432109876543210"
	cases := map[string]struct {
		body string
		call func(c *client.Client) error
	}{
		"client": {`{"id":"c1","accessTokenDurationMinutes":` + literal + `}`, func(c *client.Client) error {
			_, err := c.GetClient(context.Background(), "c1")
			return err
		}},
		"client list page": {`{"data":[{"id":"c1","accessTokenDurationMinutes":` + literal + `}],"pagination":{"totalPages":1,"totalItems":1,"currentPage":1}}`, func(c *client.Client) error {
			_, err := c.ListClients(context.Background())
			return err
		}},
		"pagination block": {`{"data":[],"pagination":{"totalPages":` + literal + `}}`, func(c *client.Client) error {
			_, err := c.ListUserGroups(context.Background())
			return err
		}},
		"group": {`{"id":"g","userCount":` + literal + `}`, func(c *client.Client) error {
			_, err := c.GetUserGroup(context.Background(), validUUID)
			return err
		}},
		"user": {`{"id":"u","isAdmin":` + literal + `}`, func(c *client.Client) error {
			_, err := c.GetUser(context.Background(), validUUID)
			return err
		}},
		"application config": {`[{"key":"appName","value":` + literal + `}]`, func(c *client.Client) error {
			_, err := c.GetApplicationConfig(context.Background())
			return err
		}},
		"custom claims": {`[{"key":"k","value":` + literal + `}]`, func(c *client.Client) error {
			_, err := c.UpdateUserCustomClaims(context.Background(), validUUID, nil)
			return err
		}},
		"SCIM": {`{"id":"s","endpoint":` + literal + `}`, func(c *client.Client) error {
			_, err := c.GetClientScimServiceProvider(context.Background(), "c1")
			return err
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			err = tc.call(c)
			require.Error(t, err)
			assert.Equal(t, client.ErrUndecodableResponse, err, "the sentinel alone, wrapping nothing")
			assert.NotContains(t, err.Error(), literal[:12])
		})
	}
}
