package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\nfixture-image")

// The request bypasses caches: both no-cache headers, and a fresh random
// nocache value on every call next to the caller's own parameters.
func TestGetBinaryUncached_BypassesCaches(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		w.Header().Set("Content-Type", "image/PNG; charset=binary")
		w.Header().Set("Cache-Control", "public, max-age=900")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	query := url.Values{"light": {"false"}}
	for i := 0; i < 2; i++ {
		body, mediaType, err := c.getBinaryUncached(context.Background(), "/api/oidc/clients/c1/logo", query, 1<<20)
		require.NoError(t, err)
		assert.Equal(t, pngBytes, body)
		assert.Equal(t, "image/png", mediaType)
	}
	assert.Equal(t, url.Values{"light": {"false"}}, query, "the caller's query is not modified")

	require.Len(t, seen, 2)
	var nonces []string
	for _, r := range seen {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/oidc/clients/c1/logo", r.URL.Path)
		assert.Equal(t, "no-cache", r.Header.Get("Cache-Control"))
		assert.Equal(t, "no-cache", r.Header.Get("Pragma"))
		assert.Equal(t, "test-token", r.Header.Get("X-API-KEY"))
		assert.True(t, r.Close, "one connection per request")
		assert.Equal(t, "false", r.URL.Query().Get("light"))
		nonce := r.URL.Query().Get(nocacheParameter)
		decoded, err := hex.DecodeString(nonce)
		require.NoError(t, err)
		assert.Len(t, decoded, 16)
		nonces = append(nonces, nonce)
	}
	assert.NotEqual(t, nonces[0], nonces[1], "every call gets its own nocache value")
}

// A body over the caller's limit, declared or streamed, is the typed body
// error, not a read the server accepted anything for.
func TestGetBinaryUncached_SizeLimit(t *testing.T) {
	for name, declare := range map[string]bool{"declared": true, "streamed": false} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := bytes.Repeat([]byte("x"), 101)
				if declare {
					w.Header().Set("Content-Length", "101")
				} else {
					w.(http.Flusher).Flush() // headers out without a length: chunked
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()
			c, err := NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			body, mediaType, err := c.getBinaryUncached(context.Background(), "/api/x", nil, 100)
			assert.Nil(t, body)
			assert.Empty(t, mediaType)
			var bodyErr *ResponseBodyError
			require.ErrorAs(t, err, &bodyErr)
			assert.True(t, bodyErr.TooLarge)
			assert.False(t, bodyErr.Accepted, "a read changed nothing")
			assert.ErrorIs(t, err, errResponseTooLarge)
			assert.False(t, errors.Is(err, ErrResultUnread))
		})
	}
	t.Run("exactly the limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 100))
		}))
		defer server.Close()
		c, err := NewClient(server.URL, "test-token", false, 30)
		require.NoError(t, err)
		body, _, err := c.getBinaryUncached(context.Background(), "/api/x", nil, 100)
		require.NoError(t, err)
		assert.Len(t, body, 100)
	})
}

// A non-2xx answer is classified as usual and, like every answer, is never
// retried.
func TestGetBinaryUncached_ErrorsAndNoRetry(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		"no custom image": {404, `{"error":"Image not found","code":"image_not_found"}`, func(t *testing.T, err error) {
			assert.True(t, IsNotFound(err, ResourceImage))
		}},
		"missing route": {404, `{"error":"API endpoint not found"}`, func(t *testing.T, err error) {
			assert.False(t, IsNotFound(err, ResourceImage))
			var status *HTTPError
			require.ErrorAs(t, err, &status)
			assert.True(t, status.MissingEndpoint)
		}},
		"server error": {503, ``, func(t *testing.T, err error) {
			var status *HTTPError
			require.ErrorAs(t, err, &status)
			assert.Equal(t, 503, status.StatusCode)
		}},
		"rate limited": {429, ``, func(t *testing.T, err error) {
			var limited *RateLimitError
			assert.ErrorAs(t, err, &limited)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c, err := NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			_, _, err = c.getBinaryUncached(context.Background(), "/api/application-images/logo", nil, 0)
			require.Error(t, err)
			tc.check(t, err)
			assert.Equal(t, int32(1), requests.Load(), "never retried")
		})
	}
	t.Run("endpoint with a query", func(t *testing.T) {
		c, err := NewClient("http://127.0.0.1:1", "test-token", false, 30)
		require.NoError(t, err)
		_, _, err = c.getBinaryUncached(context.Background(), "/api/x?light=false", nil, 0)
		assert.Error(t, err)
	})
}

// Nothing the server controls reaches the provider's logs, Go's standard
// logger or the error: not the reason phrase, not the Content-Type (which is
// also not returned when it carries the key), not the body, not a malformed
// status line.
func TestGetBinaryUncached_NothingServerControlledLogged(t *testing.T) {
	const key = "Fixture-API-Key-0123456789abcdef"
	responses := map[string]func(string) string{
		"reflected in headers and body": func(k string) string {
			return fmt.Sprintf("HTTP/1.1 200 %s\r\nContent-Type: image/%s\r\nContent-Length: %d\r\n\r\n%s", k, k, len(k), k)
		},
		"reflected in an error": func(k string) string {
			return fmt.Sprintf("HTTP/1.1 404 %s\r\nContent-Type: image/%s\r\nContent-Length: %d\r\n\r\n%s", k, k, len(k), k)
		},
		"malformed status line": func(k string) string { return "HTTP/1.1 " + k + " OK\r\n\r\n" },
	}
	// The guard compares the key the server received: configured with
	// surrounding whitespace, it still arrives (and is reflected) bare.
	configured := map[string]string{"bare": key, "padded": " \t" + key + "\t "}
	for name, respond := range responses {
		for form, token := range configured {
			t.Run(name+"/"+form, func(t *testing.T) {
				testNothingServerControlledLogged(t, key, token, respond)
			})
		}
	}
}

func testNothingServerControlledLogged(t *testing.T, key, token string, respond func(string) string) {
	t.Helper()
	standard := captureStandardLog(t)
	url, _ := rawServer(t, respond)
	var logs bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &logs)
	c, err := NewClient(url, token, false, 5)
	require.NoError(t, err)

	_, mediaType, err := c.getBinaryUncached(ctx, "/api/x", nil, 0)
	assert.NotContains(t, strings.ToLower(mediaType), strings.ToLower(key))
	if err != nil {
		walkErrorTree(err, func(e error) { assert.NotContains(t, e.Error(), key) })
	}
	assert.NotEmpty(t, logs.String())
	assert.NotContains(t, logs.String(), key)
	assert.NotContains(t, strings.ToLower(logs.String()), strings.ToLower(key))
	assert.NotContains(t, standard.String(), key)
}

// A static API key can be all digits, so a server can reflect it as a
// Content-Length that Go parses as a valid (and oversized) length. The
// size error names only this client's limit, never the declared length,
// on the binary path and the JSON path alike; a body that turns out too
// large as it streams in is refused without quoting any of it either.
func TestOversizedBodyErrorsQuoteNoServerNumber(t *testing.T) {
	const key = "4815162342108151" // a 16-digit numeric static key
	declared := func(k string) string {
		return "HTTP/1.1 200 OK\r\nContent-Type: image/png\r\nContent-Length: " + k + "\r\n\r\n" + k
	}
	streamed := func(k string) string { // no length: the body ends when the connection closes
		return "HTTP/1.1 200 OK\r\nContent-Type: image/png\r\nConnection: close\r\n\r\n" + strings.Repeat(k, 4)
	}
	calls := map[string]struct {
		respond func(string) string
		call    func(c *Client, ctx context.Context) error
	}{
		"declared, binary read": {declared, func(c *Client, ctx context.Context) error {
			_, _, err := c.getBinaryUncached(ctx, "/api/x", nil, 16)
			return err
		}},
		"declared, JSON read": {declared, func(c *Client, ctx context.Context) error {
			_, err := c.doRequest(ctx, http.MethodGet, "/api/x", nil)
			return err
		}},
		"declared, JSON mutation": {declared, func(c *Client, ctx context.Context) error {
			_, err := c.doRequest(ctx, http.MethodPut, "/api/x", map[string]string{})
			return err
		}},
		"streamed, binary read": {streamed, func(c *Client, ctx context.Context) error {
			_, _, err := c.getBinaryUncached(ctx, "/api/x", nil, 16)
			return err
		}},
	}
	for name, tc := range calls {
		t.Run(name, func(t *testing.T) {
			standard := captureStandardLog(t)
			url, _ := rawServer(t, tc.respond)
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)
			c, err := NewClient(url, key, false, 5)
			require.NoError(t, err)

			err = tc.call(c, ctx)
			var bodyErr *ResponseBodyError
			require.ErrorAs(t, err, &bodyErr)
			assert.True(t, bodyErr.TooLarge)
			assert.ErrorIs(t, err, errResponseTooLarge)
			walkErrorTree(err, func(e error) { assert.NotContains(t, e.Error(), key) })
			assert.NotContains(t, logs.String(), key)
			assert.NotContains(t, standard.String(), key)
		})
	}
}

// The media type returned is a type/subtype pair or nothing: a bare token,
// which mime.ParseMediaType accepts because it also parses
// Content-Disposition values, is no media type.
func TestMediaTypeNeedsTypeAndSubtype(t *testing.T) {
	c, err := NewClient("https://pocket-id.example.com", "test-token", false, 30)
	require.NoError(t, err)
	for header, want := range map[string]string{
		"image/png":                  "image/png",
		"IMAGE/SVG+XML; charset=x":   "image/svg+xml",
		"application/octet-stream":   "application/octet-stream",
		"attachment":                 "",
		"attachment; filename=a.png": "",
		"png":                        "",
		"image/":                     "",
		"/png":                       "",
		"image/png/x":                "",
		"":                           "",
	} {
		assert.Equal(t, want, c.mediaType(header), "%q", header)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "attachment")
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()
	c, err = NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	body, mediaType, err := c.getBinaryUncached(context.Background(), "/api/x", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, []byte("x"), body)
	assert.Empty(t, mediaType)
}
