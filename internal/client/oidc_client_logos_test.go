package client_test

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
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

type clientLogoRequest struct {
	method, path, query string
	field, fileName     string
	partType            string
	content             []byte
}

func clientLogoServer(t *testing.T, status int, body string) (*client.Client, *[]clientLogoRequest) {
	t.Helper()
	var seen []clientLogoRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := clientLogoRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		if mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mediaType == "multipart/form-data" {
			reader := multipart.NewReader(r.Body, params["boundary"])
			part, err := reader.NextPart()
			require.NoError(t, err)
			got.field, got.fileName, got.partType = part.FormName(), part.FileName(), part.Header.Get("Content-Type")
			got.content, _ = io.ReadAll(part)
			_, err = reader.NextPart()
			assert.ErrorIs(t, err, io.EOF, "one part only")
		}
		seen = append(seen, got)
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)
	return c, &seen
}

func TestClient_UploadClientLogo(t *testing.T) {
	content := []byte("\x89PNG synthetic image bytes")
	t.Run("dark logo as multipart", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusNoContent, "")
		require.NoError(t, c.UploadClientLogo(context.Background(), "c1", false, ".PNG", content))
		require.Len(t, *seen, 1)
		got := (*seen)[0]
		assert.Equal(t, "POST", got.method)
		assert.Equal(t, "/api/oidc/clients/c1/logo", got.path)
		assert.Equal(t, "light=false", got.query)
		assert.Equal(t, "file", got.field)
		assert.Equal(t, "logo.png", got.fileName, "Pocket ID reads the type from the lower-cased extension")
		assert.Equal(t, "image/png", got.partType)
		assert.Equal(t, content, got.content)
	})
	t.Run("light logo", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusNoContent, "")
		require.NoError(t, c.UploadClientLogo(context.Background(), "c1", true, "svg", []byte("<svg/>")))
		assert.Equal(t, "light=true", (*seen)[0].query)
		assert.Equal(t, "logo.svg", (*seen)[0].fileName)
	})
	t.Run("never retried", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusServiceUnavailable, `{"error":"synthetic"}`)
		err := c.UploadClientLogo(context.Background(), "c1", true, "png", content)
		require.Error(t, err)
		assert.False(t, client.IsDefiniteRejection(err))
		assert.Len(t, *seen, 1)
	})
	t.Run("refused before sending", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusNoContent, "")
		err := c.UploadClientLogo(context.Background(), "c1", true, "bmp", content)
		require.ErrorIs(t, err, client.ErrInvalidUpload)
		err = c.UploadClientLogo(context.Background(), "c1", true, "", content)
		require.ErrorIs(t, err, client.ErrInvalidUpload)
		err = c.UploadClientLogo(context.Background(), "c1", true, "png", bytes.Repeat([]byte{1}, client.ClientLogoMaxBytes+1))
		require.ErrorIs(t, err, client.ErrInvalidUpload)
		err = c.UploadClientLogo(context.Background(), "../c1", true, "png", content)
		require.ErrorIs(t, err, client.ErrInvalidIdentifier)
		assert.Empty(t, *seen)
	})
	t.Run("the largest file fits the server's request limit", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusNoContent, "")
		require.NoError(t, c.UploadClientLogo(context.Background(), "c1", true, "jpeg", bytes.Repeat([]byte{1}, client.ClientLogoMaxBytes)))
		require.Len(t, *seen, 1)
	})
}

func TestClient_GetAndDeleteClientLogo(t *testing.T) {
	t.Run("get", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusOK, "image-bytes")
		served, err := c.GetClientLogo(context.Background(), "c1", false)
		require.NoError(t, err)
		assert.Equal(t, []byte("image-bytes"), served)
		assert.Equal(t, clientLogoRequest{method: "GET", path: "/api/oidc/clients/c1/logo", query: "light=false"}, (*seen)[0])
	})
	t.Run("delete", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusNoContent, "")
		require.NoError(t, c.DeleteClientLogo(context.Background(), "c1", true))
		assert.Equal(t, clientLogoRequest{method: "DELETE", path: "/api/oidc/clients/c1/logo", query: "light=true"}, (*seen)[0])
	})
	t.Run("no such logo", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusNotFound, `{"error":"Image not found","code":"image_not_found"}`)
		err := c.DeleteClientLogo(context.Background(), "c1", false)
		assert.True(t, client.IsNotFound(err, client.ResourceImage))
		assert.False(t, client.IsNotFound(err, client.ResourceOIDCClient))
		_, err = c.GetClientLogo(context.Background(), "c1", true)
		assert.True(t, client.IsNotFound(err, client.ResourceImage))
		assert.Len(t, *seen, 2, "a DELETE is never retried; a 404 read is not either")
	})
	t.Run("server failure on delete is not retried", func(t *testing.T) {
		c, seen := clientLogoServer(t, http.StatusServiceUnavailable, "")
		err := c.DeleteClientLogo(context.Background(), "c1", true)
		var status *client.HTTPError
		require.True(t, errors.As(err, &status))
		assert.Len(t, *seen, 1)
	})
}

func TestClientLogoTypes(t *testing.T) {
	extensions := client.ClientLogoExtensions()
	assert.True(t, sort.StringsAreSorted(extensions))
	assert.Equal(t, []string{"avif", "gif", "heic", "ico", "jpeg", "jpg", "png", "svg", "webp"}, extensions)
	for extension, want := range map[string]string{".PNG": "image/png", "jpg": "image/jpeg", ".svg": "image/svg+xml"} {
		got, ok := client.ClientLogoMediaType(extension)
		assert.True(t, ok, extension)
		assert.Equal(t, want, got)
	}
	for _, extension := range []string{"", ".", "bmp", ".tiff", "png.exe"} {
		_, ok := client.ClientLogoMediaType(extension)
		assert.False(t, ok, extension)
	}
}
