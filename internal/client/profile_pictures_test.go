package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const profilePictureUser = "55555555-5555-4555-8555-555555555555"

func TestClient_UploadUserProfilePicture_SendsTheFilePartOnce(t *testing.T) {
	var requests atomic.Int32
	var field, name, mediaType, method, path string
	var content []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		method, path = r.Method, r.URL.Path
		require.NoError(t, r.ParseMultipartForm(1<<20))
		for f, headers := range r.MultipartForm.File {
			field, name, mediaType = f, headers[0].Filename, headers[0].Header.Get("Content-Type")
			file, err := headers[0].Open()
			require.NoError(t, err)
			content, _ = io.ReadAll(file)
		}
		w.WriteHeader(http.StatusServiceUnavailable) // a failure is not retried
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = c.UploadUserProfilePicture(context.Background(), profilePictureUser, client.MultipartFile{
		FieldName: "ignored", FileName: "me.png", ContentType: "image/png", Content: []byte("picture-bytes"),
	})
	require.Error(t, err)
	assert.Equal(t, int32(1), requests.Load(), "never repeated")
	assert.Equal(t, http.MethodPut, method)
	assert.Equal(t, "/api/users/"+profilePictureUser+"/profile-picture", path)
	assert.Equal(t, "file", field, "the server reads the field \"file\"")
	assert.Equal(t, "me.png", name)
	assert.Equal(t, "image/png", mediaType)
	assert.Equal(t, "picture-bytes", string(content))
}

func TestClient_ProfilePicture_RefusesBadIdentifiersAndOversizeFiles(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	ctx := context.Background()

	file := client.MultipartFile{FileName: "me.png", Content: []byte("x")}
	require.ErrorIs(t, c.UploadUserProfilePicture(ctx, "../x", file), client.ErrInvalidIdentifier)
	require.ErrorIs(t, c.ResetUserProfilePicture(ctx, "bob"), client.ErrInvalidIdentifier)
	_, err = c.GetUserProfilePicture(ctx, "bob")
	require.ErrorIs(t, err, client.ErrInvalidIdentifier)

	file.Content = make([]byte, client.UserProfilePictureMaxBytes+1)
	require.ErrorIs(t, c.UploadUserProfilePicture(ctx, profilePictureUser, file), client.ErrInvalidUpload)
	assert.Zero(t, requests.Load())
}

func TestClient_ProfilePicture_ResetAndGet(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("png-bytes"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	require.NoError(t, c.ResetUserProfilePicture(context.Background(), profilePictureUser))
	got, err := c.GetUserProfilePicture(context.Background(), profilePictureUser)
	require.NoError(t, err)
	assert.Equal(t, "png-bytes", string(got))
	assert.Equal(t, []string{
		"DELETE /api/users/" + profilePictureUser + "/profile-picture",
		"GET /api/users/" + profilePictureUser + "/profile-picture.png",
	}, seen)
}
