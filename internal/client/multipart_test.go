package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
