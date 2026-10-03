package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pngFile() MultipartFile {
	return MultipartFile{FieldName: "file", FileName: "logo.png", ContentType: "image/png", Content: []byte("\x89PNG\r\n\x1a\nfixture-image-bytes")}
}

func TestUpload_SendsOneMultipartFile(t *testing.T) {
	var got struct {
		method, path, query, key, field, name, partType string
		content                                         []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query, got.key = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-API-KEY")
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		require.NoError(t, err)
		require.Equal(t, "multipart/form-data", mediaType)
		reader := multipart.NewReader(r.Body, params["boundary"])
		part, err := reader.NextPart()
		require.NoError(t, err)
		got.field, got.name, got.partType = part.FormName(), part.FileName(), part.Header.Get("Content-Type")
		got.content, err = io.ReadAll(part)
		require.NoError(t, err)
		_, err = reader.NextPart()
		require.ErrorIs(t, err, io.EOF, "exactly one part")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	body, err := c.upload(context.Background(), http.MethodPost, "/api/oidc/clients/c1/logo?light=false", pngFile(), 2<<20)
	require.NoError(t, err)
	assert.Empty(t, body)
	assert.Equal(t, "POST", got.method)
	assert.Equal(t, "/api/oidc/clients/c1/logo", got.path)
	assert.Equal(t, "light=false", got.query)
	assert.Equal(t, "test-token", got.key)
	assert.Equal(t, "file", got.field)
	assert.Equal(t, "logo.png", got.name)
	assert.Equal(t, "image/png", got.partType)
	assert.Equal(t, pngFile().Content, got.content)

	// No content type means application/octet-stream.
	file := pngFile()
	file.ContentType = ""
	_, err = c.upload(context.Background(), http.MethodPut, "/api/application-images/favicon", file, 0)
	require.NoError(t, err)
	assert.Equal(t, "application/octet-stream", got.partType)
	assert.Equal(t, "PUT", got.method)
}

// An upload is sent once whatever the answer, including the answers a GET
// would be retried on.
func TestUpload_NeverRetried(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(status)
			}))
			defer server.Close()
			c, err := NewClient(server.URL, "test-token", false, 30)
			require.NoError(t, err)

			_, err = c.upload(context.Background(), http.MethodPost, "/api/users/x/profile-picture", pngFile(), 0)
			require.Error(t, err)
			assert.Equal(t, int32(1), requests.Load())
		})
	}
	t.Run("dropped connection", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		}))
		defer server.Close()
		c, err := NewClient(server.URL, "test-token", false, 30)
		require.NoError(t, err)
		_, err = c.upload(context.Background(), http.MethodPost, "/api/x", pngFile(), 0)
		require.Error(t, err)
		assert.Equal(t, int32(1), requests.Load())
	})
}

// Anything that would make a malformed or oversized upload is refused before
// a request is sent.
func TestUpload_RefusedBeforeSending(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	cases := map[string]func(*MultipartFile){
		"empty field":           func(f *MultipartFile) { f.FieldName = "" },
		"quoted field":          func(f *MultipartFile) { f.FieldName = `file"; name="other` },
		"empty name":            func(f *MultipartFile) { f.FileName = "" },
		"path in name":          func(f *MultipartFile) { f.FileName = "../logo.png" },
		"backslash in name":     func(f *MultipartFile) { f.FileName = `dir\logo.png` },
		"quote in name":         func(f *MultipartFile) { f.FileName = `logo".png` },
		"header break in name":  func(f *MultipartFile) { f.FileName = "logo.png\r\nX-Injected: 1" },
		"dot dot name":          func(f *MultipartFile) { f.FileName = ".." },
		"bad content type":      func(f *MultipartFile) { f.ContentType = "not a type" },
		"header break in type":  func(f *MultipartFile) { f.ContentType = "image/png\r\nX-Injected: 1" },
		"over the caller limit": func(f *MultipartFile) { f.Content = make([]byte, 2<<20+1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			file := pngFile()
			mutate(&file)
			_, err := c.upload(context.Background(), http.MethodPost, "/api/x", file, 2<<20)
			assert.ErrorIs(t, err, ErrInvalidUpload)
		})
	}
	t.Run("over the global limit", func(t *testing.T) {
		file := pngFile()
		file.Content = make([]byte, maxUploadBytes+1)
		_, err := c.upload(context.Background(), http.MethodPost, "/api/x", file, 1<<40)
		assert.ErrorIs(t, err, ErrInvalidUpload)
	})
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.upload(ctx, http.MethodPost, "/api/x", pngFile(), 0)
		assert.ErrorIs(t, err, context.Canceled)
	})
	assert.Zero(t, requests.Load(), "nothing may be sent")
}

// Uploads get the same error handling and redaction as every other request:
// status-only errors, structured not-found, and nothing of the key or the
// file in errors or logs.
func TestUpload_ErrorsAndRedaction(t *testing.T) {
	const key = "fixture-api-key-0123456789abcdef"
	marker := []byte("file-content-marker-0123456789")
	for name, tc := range map[string]struct {
		status int
		body   string
		check  func(*testing.T, error)
	}{
		"rejected": {http.StatusBadRequest, `{"error":"%s","code":"invalid_image"}`, func(t *testing.T, err error) {
			var status *HTTPError
			require.True(t, errors.As(err, &status))
			assert.Equal(t, "invalid_image", status.Code)
			assert.True(t, IsDefiniteRejection(err))
		}},
		"too large": {http.StatusRequestEntityTooLarge, `{"error":"%s","code":"file_too_large"}`, nil},
		"client gone": {http.StatusNotFound, `{"error":"%s not found","code":"not_found","details":{"resource":"OIDC client"}}`, func(t *testing.T, err error) {
			assert.True(t, IsNotFound(err, ResourceOIDCClient))
		}},
		"rate limited": {http.StatusTooManyRequests, `{"error":"%s"}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received := r.Header.Get("X-API-KEY")
				w.Header().Set("Retry-After", received)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, tc.body, received+string(marker))
			}))
			defer server.Close()
			c, err := NewClient(server.URL, key, false, 30)
			require.NoError(t, err)

			file := pngFile()
			file.Content = marker
			_, err = c.upload(ctx, http.MethodPost, "/api/oidc/clients/c1/logo", file, 0)
			require.Error(t, err)
			for _, text := range []string{err.Error(), logs.String()} {
				assert.NotContains(t, text, key)
				assert.False(t, strings.Contains(text, string(marker)), "the file's content is not echoed")
			}
			if tc.check != nil {
				tc.check(t, err)
			}
		})
	}
}

// staleConnectionServer answers the first request on each connection with
// 204, leaving the connection open, then reads any further request on it
// and closes the connection without answering: the stale kept-alive
// connection Go's transport would replay a replayable request on. It counts
// connections and requests.
func staleConnectionServer(t *testing.T, statusLine ...string) (string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	status := "HTTP/1.1 204 No Content"
	if len(statusLine) > 0 {
		status = statusLine[0]
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var connections, requests atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				for served := 0; ; served++ {
					req, err := http.ReadRequest(reader)
					if err != nil {
						return
					}
					_, _ = io.Copy(io.Discard, req.Body)
					requests.Add(1)
					if served > 0 {
						return // close without an answer
					}
					_, _ = io.WriteString(conn, status+"\r\nContent-Length: 0\r\n\r\n")
				}
			}()
		}
	}()
	return "http://" + listener.Addr().String(), &connections, &requests
}

// No request goes out on a connection an earlier request used, so none can
// meet a stale connection and be replayed: every send, with or without a
// body, opens its own connection and is read by the server exactly once.
func TestSend_EachRequestOnItsOwnConnection(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			url, connections, requests := staleConnectionServer(t)
			c, err := NewClient(url, "test-token", false, 5)
			require.NoError(t, err)

			for _, payload := range [][]byte{nil, []byte(`{"a":1}`), nil, []byte(`{"a":2}`)} {
				_, err = c.send(context.Background(), method, "/api/x", "application/json", payload)
				require.NoError(t, err)
			}
			assert.Equal(t, int32(4), connections.Load())
			assert.Equal(t, int32(4), requests.Load())
		})
	}
	t.Run("upload", func(t *testing.T) {
		url, connections, requests := staleConnectionServer(t)
		c, err := NewClient(url, "test-token", false, 5)
		require.NoError(t, err)
		_, err = c.upload(context.Background(), http.MethodPost, "/api/x", pngFile(), 0)
		require.NoError(t, err)
		_, err = c.upload(context.Background(), http.MethodPut, "/api/x", pngFile(), 0)
		require.NoError(t, err)
		assert.Equal(t, int32(2), connections.Load())
		assert.Equal(t, int32(2), requests.Load())
	})
}

// Uploads use POST or PUT only; anything else is refused before sending.
func TestUpload_OnlyPostAndPut(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodPatch, http.MethodOptions, ""} {
		_, err := c.upload(context.Background(), method, "/api/x", pngFile(), 0)
		assert.ErrorIs(t, err, ErrInvalidUpload, method)
	}
	assert.Zero(t, requests.Load())
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		_, err := c.upload(context.Background(), method, "/api/x", pngFile(), 0)
		assert.NoError(t, err, method)
	}
	assert.Equal(t, int32(2), requests.Load())
}

// framingServer records how each request's body was framed on the wire: its
// Content-Length header (empty when absent) and its Transfer-Encoding.
func framingServer(t *testing.T) (string, func() (string, []string, int)) {
	t.Helper()
	var mu sync.Mutex
	var contentLength string
	var transferEncoding []string
	var bodyLength int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		contentLength = r.Header.Get("Content-Length")
		transferEncoding = r.TransferEncoding
		bodyLength = len(body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server.URL, func() (string, []string, int) {
		mu.Lock()
		defer mu.Unlock()
		return contentLength, transferEncoding, bodyLength
	}
}

// Every body goes out with an exact Content-Length, never chunked: an empty
// one as Content-Length: 0, a JSON one and a multipart upload with their
// length. A request without a body declares none.
func TestSend_BodyFraming(t *testing.T) {
	url, seen := framingServer(t)
	c, err := NewClient(url, "test-token", false, 5)
	require.NoError(t, err)
	ctx := context.Background()

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		_, err = c.send(ctx, method, "/api/x", "application/json", []byte{})
		require.NoError(t, err)
		length, encoding, n := seen()
		// Go declares an explicitly empty body only for methods that usually
		// carry one; a DELETE simply has none. Neither is ever chunked.
		wantLength := "0"
		if method == http.MethodDelete {
			wantLength = ""
		}
		assert.Equal(t, wantLength, length, "%s with an empty body", method)
		assert.Empty(t, encoding, method)
		assert.Zero(t, n)

		payload := []byte(`{"name":"framing"}`)
		_, err = c.send(ctx, method, "/api/x", "application/json", payload)
		require.NoError(t, err)
		length, encoding, n = seen()
		assert.Equal(t, fmt.Sprint(len(payload)), length, "%s with a JSON body", method)
		assert.Empty(t, encoding, method)
		assert.Equal(t, len(payload), n)
	}

	_, err = c.doRequest(ctx, http.MethodPut, "/api/x", plainBody{"a": "b"})
	require.NoError(t, err)
	length, encoding, n := seen()
	assert.Equal(t, fmt.Sprint(n), length, "doRequest's JSON body")
	assert.Empty(t, encoding)

	for _, file := range []MultipartFile{pngFile(), {FieldName: "file", FileName: "empty.png", Content: nil}} {
		_, err = c.upload(ctx, http.MethodPost, "/api/x", file, 0)
		require.NoError(t, err)
		length, encoding, n = seen()
		assert.NotEmpty(t, length, "an upload declares its length")
		assert.Equal(t, fmt.Sprint(n), length)
		assert.Empty(t, encoding)
	}

	_, err = c.send(ctx, http.MethodGet, "/api/x", "application/json", nil)
	require.NoError(t, err)
	length, encoding, n = seen()
	assert.Empty(t, length, "a GET without a body declares no length")
	assert.Empty(t, encoding)
	assert.Zero(t, n)
}

// Four attempts are four requests on the wire. The server answers 503 on a
// connection's first request and closes it without an answer if a second
// request arrives on it, which a client reusing connections meets and then
// silently replays (a GET without a body is replayable): about seven wire
// requests for four attempts. With a fresh connection per attempt there is
// nothing to replay.
func TestRetriedReadIsSentOncePerAttempt(t *testing.T) {
	url, connections, requests := staleConnectionServer(t, "HTTP/1.1 503 Service Unavailable")
	c, err := NewClient(url, "test-token", false, 5)
	require.NoError(t, err)
	c.retry = retryPolicy{maxAttempts: 4, backoffUnit: time.Millisecond, maxWait: time.Second, maxElapsed: 10 * time.Second}

	_, err = c.doRequest(context.Background(), http.MethodGet, "/api/x", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 4 attempts")
	assert.Equal(t, int32(4), requests.Load(), "four attempts, four requests")
	assert.Equal(t, int32(4), connections.Load(), "each on its own connection")
}

// No request with a body can be replayed by Go's transport: newRequest never
// gives it a GetBody to replay from, whatever the method, and declares its
// exact length.
func TestNewRequest_BodyIsOneShot(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, err := newRequest(context.Background(), method, "http://127.0.0.1:1/api/x", []byte(`{"a":1}`))
		require.NoError(t, err)
		assert.Nil(t, req.GetBody, method)
		assert.Equal(t, int64(7), req.ContentLength, method)
	}
}
