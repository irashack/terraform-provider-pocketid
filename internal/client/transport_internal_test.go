package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
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
					walkErrorTree(err, func(e error) {
						assert.NotContains(t, e.Error(), key, "neither the error nor anything it wraps carries it")
					})
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

// A 2xx whose body cannot be read in full means, for a mutation, that the
// server accepted it: the error wraps ErrResultUnread, so callers treat the
// outcome as uncertain and never repeat it. For a GET it is just an error.
func TestUnreadableSuccessBody(t *testing.T) {
	oversizedDeclared := func(string) string {
		return fmt.Sprintf("HTTP/1.1 201 Created\r\nContent-Length: %d\r\n\r\n{}", maxResponseBodyBytes+1)
	}
	oversizedStreamed := func(string) string {
		return "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n" + strings.Repeat(" ", maxResponseBodyBytes+1)
	}
	interrupted := func(string) string {
		return "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{\"token\":"
	}
	cases := []struct {
		name     string
		method   string
		response func(string) string
		tooLarge bool
		cause    error
	}{
		{"POST declared oversize", http.MethodPost, oversizedDeclared, true, errResponseTooLarge},
		{"PUT streamed oversize", http.MethodPut, oversizedStreamed, true, errResponseTooLarge},
		{"POST interrupted", http.MethodPost, interrupted, false, io.ErrUnexpectedEOF},
		{"DELETE interrupted", http.MethodDelete, interrupted, false, io.ErrUnexpectedEOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, requests := rawServer(t, tc.response)
			c, err := NewClient(url, "test-token", false, 5)
			require.NoError(t, err)

			_, err = c.doRequest(context.Background(), tc.method, "/api/x", map[string]string{"a": "b"})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrResultUnread)
			assert.ErrorIs(t, err, tc.cause)
			var body *ResponseBodyError
			require.ErrorAs(t, err, &body)
			assert.True(t, body.Accepted)
			assert.Equal(t, tc.tooLarge, body.TooLarge)
			assert.Equal(t, int32(1), requests.Load(), "an accepted mutation is never repeated")
		})
	}
	t.Run("GET oversize is only an error", func(t *testing.T) {
		url, requests := rawServer(t, oversizedDeclared)
		c, err := NewClient(url, "test-token", false, 5)
		require.NoError(t, err)
		_, err = c.doRequest(context.Background(), http.MethodGet, "/api/x", nil)
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrResultUnread))
		assert.ErrorIs(t, err, errResponseTooLarge)
		assert.Equal(t, int32(1), requests.Load(), "an oversized answer is not retried")
	})
	t.Run("GET interrupted is only an error", func(t *testing.T) {
		url, _ := rawServer(t, interrupted)
		c, err := NewClient(url, "test-token", false, 5)
		require.NoError(t, err)
		_, err = c.doRequest(context.Background(), http.MethodGet, "/api/x", nil)
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrResultUnread))
	})
	t.Run("rejection with an oversized body stays a rejection", func(t *testing.T) {
		url, _ := rawServer(t, func(string) string {
			return fmt.Sprintf("HTTP/1.1 400 Bad Request\r\nContent-Length: %d\r\n\r\n", maxErrorBodyBytes+1) + strings.Repeat(" ", maxErrorBodyBytes+1)
		})
		c, err := NewClient(url, "test-token", false, 5)
		require.NoError(t, err)
		_, err = c.doRequest(context.Background(), http.MethodPost, "/api/x", nil)
		assert.False(t, errors.Is(err, ErrResultUnread))
		assert.True(t, IsDefiniteRejection(err))
	})
	// The callers that already handle ErrResultUnread get it for these too.
	t.Run("one-time token and user groups", func(t *testing.T) {
		url, requests := rawServer(t, interrupted)
		c, err := NewClient(url, "test-token", false, 5)
		require.NoError(t, err)
		const user = "11111111-1111-4111-8111-111111111111"
		_, err = c.CreateOneTimeAccessToken(context.Background(), user, &OneTimeAccessTokenRequest{TTL: "1h"})
		assert.ErrorIs(t, err, ErrResultUnread)
		_, err = c.UpdateUserGroups(context.Background(), user, []string{"g"})
		assert.ErrorIs(t, err, ErrResultUnread)
		assert.Equal(t, int32(2), requests.Load())
	})
}

// A read may always use one full configured timeout: the retry deadline is
// 30 seconds or the HTTP timeout, whichever is longer.
func TestNewClientRetryDeadline(t *testing.T) {
	for timeout, want := range map[int64]time.Duration{0: maxRetryElapsed, 10: maxRetryElapsed, 30: maxRetryElapsed, 120: 120 * time.Second} {
		c, err := NewClient("http://127.0.0.1:1", "test-token", false, timeout)
		require.NoError(t, err)
		assert.Equal(t, want, c.retry.maxElapsed, "timeout %d", timeout)
	}
}

// walkErrorTree calls visit for err and every error it wraps, following both
// Unwrap() error and Unwrap() []error.
func walkErrorTree(err error, visit func(error)) {
	if err == nil {
		return
	}
	visit(err)
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, inner := range wrapped.Unwrap() {
			walkErrorTree(inner, visit)
		}
	case interface{ Unwrap() error }:
		walkErrorTree(wrapped.Unwrap(), visit)
	}
}

// captureStandardLog redirects Go's standard logger, which net/http writes
// some connection events to, for the rest of the test.
func captureStandardLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf syncBuffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf.Buffer
}

// syncBuffer is a bytes.Buffer safe for the logger's writes from other
// goroutines; it is read only after they are done.
type syncBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}

// A server that keeps writing after its response (here: a second response
// carrying the key it received) reaches neither the provider's log nor Go's
// standard logger: every connection is closed after one response, so
// nothing reads from it while it is idle. Every request also goes out on a
// connection of its own.
func TestNothingReadFromAnIdleConnection(t *testing.T) {
	const key = "fixture-api-key-0123456789abcdef"
	standard := captureStandardLog(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var connections, requests atomic.Int32
	var closeHeaders atomic.Int32
	done := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer func() { _ = conn.Close(); done <- struct{}{} }()
				reader := bufio.NewReader(conn)
				for {
					req, err := http.ReadRequest(reader)
					if err != nil {
						return
					}
					_, _ = io.Copy(io.Discard, req.Body)
					requests.Add(1)
					if req.Close {
						closeHeaders.Add(1)
					}
					received := req.Header.Get("X-API-KEY")
					_, _ = io.WriteString(conn, "HTTP/1.1 204 No Content\r\n\r\n")
					time.Sleep(50 * time.Millisecond)
					_, _ = io.WriteString(conn, "HTTP/1.1 200 OK "+received+"\r\nContent-Length: 0\r\n\r\n")
				}
			}()
		}
	}()

	var logs bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &logs)
	c, err := NewClient("http://"+listener.Addr().String(), key, false, 5)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err = c.doRequest(ctx, http.MethodDelete, "/api/x", nil)
		require.NoError(t, err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the server never saw its connection close")
		}
	}

	assert.Equal(t, int32(3), connections.Load(), "one connection per request")
	assert.Equal(t, int32(3), requests.Load())
	assert.Equal(t, int32(3), closeHeaders.Load(), "each request asks for the connection to be closed")
	assert.NotContains(t, standard.String(), key)
	assert.NotContains(t, logs.String(), key)
}

// Whatever description and retry decision a connection failure gets, the
// bare errno it carried stays reachable through errors.Is, for a failed
// request and for a failed body read alike, and nothing else of it does.
func TestTransportErrorsKeepTheirErrno(t *testing.T) {
	cases := []struct {
		errno     syscall.Errno
		op, call  string
		reason    string
		retryable bool
	}{
		{syscall.ECONNREFUSED, "dial", "connect", "connection refused", true},
		{syscall.ECONNRESET, "read", "read", "connection reset by peer", true},
		{syscall.ETIMEDOUT, "dial", "connect", "network timeout", true},
		{syscall.EPIPE, "write", "write", "the connection broke while the request was being sent", false},
		{syscall.ENETUNREACH, "dial", "connect", "network unreachable", false},
		{syscall.EHOSTUNREACH, "dial", "connect", "host unreachable", false},
	}
	for _, tc := range cases {
		t.Run(tc.errno.Error(), func(t *testing.T) {
			raw := &url.Error{Op: "Post", URL: "http://192.0.2.7:1411/api/x", Err: &net.OpError{
				Op: tc.op, Net: "tcp", Err: os.NewSyscallError(tc.call, tc.errno),
			}}

			err := newTransportError(http.MethodPost, "/api/x", "no response", raw)
			assert.ErrorIs(t, err, tc.errno)
			assert.Equal(t, "POST /api/x: no response: "+tc.reason, err.Error())
			assert.Equal(t, tc.retryable, isRetryableError(err))
			walkErrorTree(err, func(e error) {
				assert.NotContains(t, e.Error(), "192.0.2.7", "nothing of the original error is kept")
			})

			reason, causes, retryable := classifyTransportError(raw)
			body := &ResponseBodyError{StatusCode: 200, Reason: reason, causes: causes, retryable: retryable}
			assert.ErrorIs(t, body, tc.errno)
			assert.Equal(t, tc.retryable, isRetryableError(body))
		})
	}
	t.Run("context errors come first", func(t *testing.T) {
		raw := &url.Error{Op: "Get", URL: "http://192.0.2.7/api/x", Err: fmt.Errorf("%w: %w", context.DeadlineExceeded, os.NewSyscallError("read", syscall.ETIMEDOUT))}
		err := newTransportError(http.MethodGet, "/api/x", "no response", raw)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.ErrorIs(t, err, syscall.ETIMEDOUT)
		assert.False(t, isRetryableError(err), "an expired deadline is not retried")
	})
}

// The Winsock codes Windows reports get the same descriptions and retry
// decisions as their POSIX counterparts. The mapping is checked by number,
// so it runs on every platform; on Windows classifyTransportError applies it
// and keeps the bare errno as a cause.
func TestWinsockClass(t *testing.T) {
	cases := map[uintptr]struct {
		reason    string
		retryable bool
	}{
		10061: {"connection refused", true},
		10054: {"connection reset by peer", true},
		10060: {"network timeout", true},
		10051: {"network unreachable", false},
		10065: {"host unreachable", false},
	}
	for code, want := range cases {
		reason, retryable, ok := winsockClass(code)
		assert.True(t, ok, "%d", code)
		assert.Equal(t, want.reason, reason, "%d", code)
		assert.Equal(t, want.retryable, retryable, "%d", code)
	}
	for _, code := range []uintptr{0, 111, 104, 10053, 10064} {
		_, _, ok := winsockClass(code)
		assert.False(t, ok, "%d is not mapped", code)
	}
	if runtime.GOOS != "windows" {
		_, _, ok := nativeErrnoClass(syscall.Errno(10061))
		assert.False(t, ok, "only Windows applies the Winsock mapping")
	} else {
		raw := &url.Error{Op: "Get", URL: "http://192.0.2.7/api/x", Err: &net.OpError{
			Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", syscall.Errno(10061)),
		}}
		err := newTransportError(http.MethodGet, "/api/x", "no response", raw)
		assert.ErrorIs(t, err, syscall.Errno(10061))
		assert.True(t, isRetryableError(err))
	}
}
