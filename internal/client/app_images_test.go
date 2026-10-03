package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestApplicationImageRoutes(t *testing.T) {
	type request struct{ method, uri string }
	var got request
	var uploaded struct {
		field, fileName, contentType string
		content                      []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = request{r.Method, r.URL.RequestURI()}
		if r.Method == http.MethodPut {
			require.NoError(t, r.ParseMultipartForm(1<<20))
			for field, files := range r.MultipartForm.File {
				uploaded.field = field
				uploaded.fileName = files[0].Filename
				uploaded.contentType = files[0].Header.Get("Content-Type")
				f, err := files[0].Open()
				require.NoError(t, err)
				uploaded.content, _ = io.ReadAll(f)
				_ = f.Close()
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte("image-bytes"))
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	ctx := context.Background()

	for _, tc := range []struct {
		image            client.ApplicationImage
		put, get, delete string
		extension        string
		contentType      string
	}{
		{client.ApplicationImageLogoLight, "/api/application-images/logo?light=true", "/api/application-images/logo?light=true&default=false", "/api/application-images/logo?light=true", "svg", "image/svg+xml"},
		{client.ApplicationImageLogoDark, "/api/application-images/logo?light=false", "/api/application-images/logo?light=false&default=false", "/api/application-images/logo?light=false", "png", "image/png"},
		{client.ApplicationImageEmailLogo, "/api/application-images/email", "/api/application-images/email?default=false", "", "jpg", "image/jpeg"},
		{client.ApplicationImageBackground, "/api/application-images/background", "/api/application-images/background?default=false", "/api/application-images/background", "webp", "image/webp"},
		{client.ApplicationImageFavicon, "/api/application-images/favicon", "/api/application-images/favicon?default=false", "", "ico", "image/x-icon"},
		{client.ApplicationImageDefaultProfilePicture, "/api/application-images/default-profile-picture", "/api/application-images/default-profile-picture?default=false", "/api/application-images/default-profile-picture", "jpeg", "image/jpeg"},
	} {
		t.Run(string(tc.image), func(t *testing.T) {
			require.NoError(t, c.UploadApplicationImage(ctx, tc.image, tc.extension, []byte("content")))
			assert.Equal(t, request{http.MethodPut, tc.put}, got)
			assert.Equal(t, "file", uploaded.field)
			assert.Equal(t, "image."+tc.extension, uploaded.fileName)
			assert.Equal(t, tc.contentType, uploaded.contentType)
			assert.Equal(t, []byte("content"), uploaded.content)

			body, err := c.GetApplicationImage(ctx, tc.image)
			require.NoError(t, err)
			assert.Equal(t, []byte("image-bytes"), body)
			assert.Equal(t, request{http.MethodGet, tc.get}, got)

			got = request{}
			err = c.DeleteApplicationImage(ctx, tc.image)
			if tc.delete == "" {
				assert.False(t, tc.image.Deletable())
				assert.ErrorIs(t, err, client.ErrApplicationImageNotDeletable)
				assert.Equal(t, request{}, got, "nothing is sent")
				return
			}
			require.NoError(t, err)
			assert.True(t, tc.image.Deletable())
			assert.Equal(t, request{http.MethodDelete, tc.delete}, got)
		})
	}
	assert.Len(t, client.ApplicationImages(), 6)
}

func TestApplicationImageRefusals(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	ctx := context.Background()

	assert.ErrorIs(t, c.UploadApplicationImage(ctx, client.ApplicationImageEmailLogo, "svg", []byte("x")), client.ErrInvalidUpload, "the e-mail logo is PNG or JPEG")
	assert.ErrorIs(t, c.UploadApplicationImage(ctx, client.ApplicationImageFavicon, "jpg", []byte("x")), client.ErrInvalidUpload, "the favicon is SVG, PNG or ICO")
	assert.ErrorIs(t, c.UploadApplicationImage(ctx, client.ApplicationImageLogoLight, "PNG", []byte("x")), client.ErrInvalidUpload, "extensions are lower case")
	assert.ErrorIs(t, c.UploadApplicationImage(ctx, client.ApplicationImageLogoLight, "bmp", []byte("x")), client.ErrInvalidUpload)
	assert.ErrorIs(t, c.UploadApplicationImage(ctx, client.ApplicationImageLogoLight, "png", make([]byte, client.MaxApplicationImageBytes+1)), client.ErrInvalidUpload)
	assert.ErrorIs(t, c.UploadApplicationImage(ctx, "logo", "png", []byte("x")), client.ErrInvalidIdentifier)
	_, err = c.GetApplicationImage(ctx, "../users")
	assert.ErrorIs(t, err, client.ErrInvalidIdentifier)
	assert.ErrorIs(t, c.DeleteApplicationImage(ctx, "favicon?light=false"), client.ErrInvalidIdentifier)
	assert.Zero(t, requests.Load(), "nothing is sent")
}

// An upload is never retried, even after a server error.
func TestApplicationImageUploadNotRetried(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	err = c.UploadApplicationImage(context.Background(), client.ApplicationImageBackground, "png", []byte("x"))
	var status *client.HTTPError
	require.True(t, errors.As(err, &status))
	assert.Equal(t, http.StatusServiceUnavailable, status.StatusCode)
	assert.Equal(t, int32(1), requests.Load())
}

// A missing custom image is Pocket ID's image_not_found, which IsNotFound
// confirms; the router's missing-endpoint 404 is not.
func TestApplicationImageNotFound(t *testing.T) {
	body := `{"error":"Image not found","code":"image_not_found"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	_, err = c.GetApplicationImage(context.Background(), client.ApplicationImageLogoDark)
	assert.True(t, client.IsNotFound(err, client.ResourceImage))
	assert.True(t, client.IsNotFound(c.DeleteApplicationImage(context.Background(), client.ApplicationImageLogoDark), client.ResourceImage))

	body = `{"error":"API endpoint not found"}`
	_, err = c.GetApplicationImage(context.Background(), client.ApplicationImageLogoDark)
	assert.Error(t, err)
	assert.False(t, client.IsNotFound(err, client.ResourceImage))
}
