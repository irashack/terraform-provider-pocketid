package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPLogsAndDiagnosticsExcludeCredentials(t *testing.T) {
	for _, status := range []int{201, 400, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"secret":"fixture-secret","error":"fixture-token fixture-secret"}`)
			}))
			defer s.Close()
			c, _ := NewClient(s.URL, "fixture-token", false, 1)
			_, err := c.doRequest(ctx, "POST", "/api/oidc/clients/fixture/secrets", map[string]string{"secret": "fixture-secret"})
			require.NotEmpty(t, logs.String())
			require.NotContains(t, logs.String(), "fixture-token")
			require.NotContains(t, logs.String(), "fixture-secret")
			if err != nil {
				require.NotContains(t, err.Error(), "fixture-token")
				require.NotContains(t, err.Error(), "fixture-secret")
			}
		})
	}
}

// A hostile or broken server (or proxy) that reflects the API key it
// received, in a 429's Retry-After or in an oversized body, must not get the
// key into the returned error or the provider's log.
func TestHostileServerCannotReflectAPIKey(t *testing.T) {
	const key = "fixture-api-key-0123456789abcdef"
	notFound := `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`

	cases := []struct {
		name    string
		method  string
		handler func(w http.ResponseWriter, received string)
		check   func(t *testing.T, err error)
	}{
		{
			name:   "429 retry-after reflects key",
			method: http.MethodGet,
			handler: func(w http.ResponseWriter, received string) {
				w.Header().Set("Retry-After", received)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprintf(w, `{"error":%q}`, received)
			},
			check: func(t *testing.T, err error) {
				var rateLimited *RateLimitError
				require.ErrorAs(t, err, &rateLimited)
				assert.Zero(t, rateLimited.RetryAfter, "an unparsable Retry-After is no delay")
			},
		},
		{
			name:   "429 retry-after with a numeric prefix",
			method: http.MethodPost,
			handler: func(w http.ResponseWriter, received string) {
				w.Header().Set("Retry-After", "5 "+received)
				w.WriteHeader(http.StatusTooManyRequests)
			},
		},
		{
			name:   "oversized success body without length",
			method: http.MethodGet,
			handler: func(w http.ResponseWriter, received string) {
				w.WriteHeader(http.StatusOK)
				chunk := strings.Repeat(received, 1024)
				for written := 0; written <= maxResponseBodyBytes; written += len(chunk) {
					if _, err := io.WriteString(w, chunk); err != nil {
						return
					}
				}
			},
			check: func(t *testing.T, err error) { assert.ErrorIs(t, err, errResponseTooLarge) },
		},
		{
			name:   "oversized success body with declared length",
			method: http.MethodGet,
			handler: func(w http.ResponseWriter, received string) {
				w.Header().Set("Content-Length", fmt.Sprint(maxResponseBodyBytes+1))
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, received)
			},
			check: func(t *testing.T, err error) { assert.ErrorIs(t, err, errResponseTooLarge) },
		},
		{
			name:   "oversized error body",
			method: http.MethodPut,
			handler: func(w http.ResponseWriter, received string) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, strings.Repeat(received, maxErrorBodyBytes/len(received)+1))
			},
			check: func(t *testing.T, err error) {
				var status *HTTPError
				require.ErrorAs(t, err, &status)
				assert.Equal(t, http.StatusBadRequest, status.StatusCode)
			},
		},
		{
			// Trailing whitespace keeps it valid JSON: only the size limit
			// stops this from reading as Pocket ID's not-found error.
			name:   "oversized structured not-found",
			method: http.MethodGet,
			handler: func(w http.ResponseWriter, received string) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, notFound+strings.Repeat(" ", maxErrorBodyBytes))
			},
			check: func(t *testing.T, err error) {
				assert.False(t, IsOIDCClientNotFound(err), "an unread body must never confirm absence")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tc.handler(w, r.Header.Get("X-API-KEY"))
			}))
			defer server.Close()

			c, err := NewClient(server.URL, key, false, 30)
			require.NoError(t, err)
			c.retry = retryPolicy{maxAttempts: 2, backoffUnit: time.Millisecond, maxWait: time.Second, maxElapsed: time.Second}

			_, err = c.doRequest(ctx, tc.method, "/api/oidc/clients/fixture", nil)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), key)
			assert.NotContains(t, logs.String(), key)
			if tc.check != nil {
				tc.check(t, err)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"   ":                           0,
		"0":                             0,
		"-5":                            0,
		"7":                             7 * time.Second,
		" 7 ":                           7 * time.Second,
		"86400":                         24 * time.Hour,
		"604800":                        maxRetryAfter,
		"99999999999999999999999999999": 0, // overflows int64: unusable
		"9223372036854775807":           maxRetryAfter,
		"1.5":                           0,
		"soon":                          0,
		now.Add(30 * time.Second).Format(http.TimeFormat):    31 * time.Second,
		now.Add(-time.Minute).Format(http.TimeFormat):        0,
		now.Add(30 * 24 * time.Hour).Format(http.TimeFormat): maxRetryAfter,
	}
	for value, want := range cases {
		assert.Equal(t, want, parseRetryAfter(value, now), "Retry-After %q", value)
	}
}

// rawServer answers every request with the bytes respond returns, written
// straight to the connection, so a test can send what httptest cannot: an
// arbitrary reason phrase, a malformed status line or header, a broken body.
// respond receives the X-API-KEY the request carried.
func rawServer(t *testing.T, respond func(key string) string) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var requests atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, req.Body)
				requests.Add(1)
				_, _ = io.WriteString(conn, respond(req.Header.Get("X-API-KEY")))
			}()
		}
	}()
	return "http://" + listener.Addr().String(), &requests
}

// A server that reflects the API key into the status line, a header line,
// the Content-Length or a chunked body must not get it into the returned
// error or the provider's log, whether the response parses or not.
func TestRawWireReflectionNeverReachesErrorsOrLogs(t *testing.T) {
	const key = "fixture-api-key-0123456789abcdef"
	cases := map[string]struct {
		response func(key string) string
		check    func(t *testing.T, err error)
	}{
		"reason phrase on success": {
			response: func(k string) string { return "HTTP/1.1 200 " + k + "\r\nContent-Length: 2\r\n\r\n{}" },
			check:    func(t *testing.T, err error) { require.NoError(t, err) },
		},
		"reason phrase on an error": {
			response: func(k string) string {
				body := `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`
				return fmt.Sprintf("HTTP/1.1 404 %s\r\nContent-Length: %d\r\n\r\n%s", k, len(body), body)
			},
			check: func(t *testing.T, err error) { assert.True(t, IsNotFound(err, ResourceOIDCClient)) },
		},
		"malformed status code": {
			response: func(k string) string { return "HTTP/1.1 " + k + " OK\r\n\r\n" },
		},
		"malformed status line": {
			response: func(k string) string { return k + "\r\n\r\n" },
		},
		"malformed header line": {
			response: func(k string) string { return "HTTP/1.1 200 OK\r\n" + k + "\r\n\r\n" },
		},
		"malformed content length": {
			response: func(k string) string { return "HTTP/1.1 200 OK\r\nContent-Length: " + k + "\r\n\r\n" },
		},
		"malformed chunked body": {
			response: func(k string) string { return "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n" + k + "\r\n" },
		},
	}
	for name, tc := range cases {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(name+"/"+method, func(t *testing.T) {
				url, _ := rawServer(t, tc.response)
				var logs bytes.Buffer
				ctx := tflogtest.RootLogger(context.Background(), &logs)
				c, err := NewClient(url, key, false, 5)
				require.NoError(t, err)
				c.retry = retryPolicy{maxAttempts: 2, backoffUnit: time.Millisecond, maxWait: time.Second, maxElapsed: 5 * time.Second}

				_, err = c.doRequest(ctx, method, "/api/oidc/clients/fixture", nil)
				if tc.check != nil {
					tc.check(t, err)
				} else {
					require.Error(t, err)
				}
				if err != nil {
					assert.NotContains(t, err.Error(), key)
					for unwrapped := errors.Unwrap(err); unwrapped != nil; unwrapped = errors.Unwrap(unwrapped) {
						assert.NotContains(t, unwrapped.Error(), key, "no wrapped error carries it either")
					}
				}
				assert.NotEmpty(t, logs.String())
				assert.NotContains(t, logs.String(), key)
			})
		}
	}
}

// The fixed descriptions keep the classifications callers and the retry
// loop rely on.
func TestTransportErrorsKeepTheirClassification(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		url := "http://" + listener.Addr().String()
		require.NoError(t, listener.Close())
		c, err := NewClient(url, "test-token", false, 5)
		require.NoError(t, err)
		_, err = c.send(context.Background(), http.MethodGet, "/api/x", "application/json", nil)
		var transport *TransportError
		require.ErrorAs(t, err, &transport)
		assert.ErrorIs(t, err, syscall.ECONNREFUSED)
		assert.True(t, isRetryableError(err))
		assert.Equal(t, "GET /api/x: no response: connection refused", err.Error())
	})
	t.Run("client timeout", func(t *testing.T) {
		url, _ := rawServer(t, func(string) string { time.Sleep(2 * time.Second); return "" })
		c, err := NewClient(url, "test-token", false, 1)
		require.NoError(t, err)
		_, err = c.send(context.Background(), http.MethodGet, "/api/x", "application/json", nil)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.False(t, isRetryableError(err), "the client's own timeout is not retried")
	})
	t.Run("cancelled", func(t *testing.T) {
		url, _ := rawServer(t, func(string) string { time.Sleep(2 * time.Second); return "" })
		c, err := NewClient(url, "test-token", false, 30)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		_, err = c.send(ctx, http.MethodGet, "/api/x", "application/json", nil)
		assert.ErrorIs(t, err, context.Canceled)
		assert.False(t, isRetryableError(err))
	})
	t.Run("closed without a response", func(t *testing.T) {
		url, _ := rawServer(t, func(string) string { return "" })
		c, err := NewClient(url, "test-token", false, 5)
		require.NoError(t, err)
		_, err = c.send(context.Background(), http.MethodPost, "/api/x", "application/json", []byte("{}"))
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
		assert.False(t, isRetryableError(err))
	})
}
