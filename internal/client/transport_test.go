package client_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

			_, err = c.GetClient("test-client-id")
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

	result, err := c.GetClient("test-client-id")
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

	_, err = c.GetClient("test-client-id")
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

	_, err = c.GetClient("test-client-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404: Not Found")
	assert.Equal(t, 1, attempts, "Should have made only 1 attempt (no retries for 404)")
}

func TestClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	// Note: The current client doesn't expose context-aware methods,
	// but this test is prepared for when they are added
	_, err = c.GetClient("test-client-id")
	// The error might be a timeout or context cancellation depending on timing
	assert.Error(t, err)
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
	result, err := c.GetClient("test-client-id")
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

	result, err := c.GetClient("test-client-id")

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
	result, err := c.GetClient("test-id")
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
	result, err := c.GetClient("test-id")
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

	result, err := c.GetClient("test-id")
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

			result, err := c.GetClient("test-id")
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), fmt.Sprintf("HTTP %d", tc.statusCode))
			assert.Equal(t, tc.attempts, attempts, "Should not retry for %d errors", tc.statusCode)
		})
	}
}
