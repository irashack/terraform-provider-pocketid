package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// profilePictureCachingProxy is a cache in front of origin: it keeps every
// answer for 15 minutes, as Pocket ID's Cache-Control allows, and decides what
// a request is keyed by and whether it honors the request's no-cache.
func profilePictureCachingProxy(t *testing.T, origin *httptest.Server, keyByPathOnly, honorNoCache bool) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	cached := map[string][]byte{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.RequestURI()
		if keyByPathOnly {
			key = r.URL.Path
		}
		bypass := honorNoCache && strings.Contains(r.Header.Get("Cache-Control"), "no-cache")
		mu.Lock()
		hit, ok := cached[key]
		mu.Unlock()
		if ok && !bypass {
			_, _ = w.Write(hit)
			return
		}
		response, err := http.Get(origin.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, "origin unreachable", http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		mu.Lock()
		cached[key] = body
		mu.Unlock()
		_, _ = w.Write(body)
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

// The picture read after an upload must be the new one, however a cache
// between the provider and Pocket ID works: one keyed by the full URL that
// ignores request headers is defeated by the per-call nocache parameter, and one
// that ignores the query string but honors Cache-Control: no-cache by the
// header. Each cache below would serve the first picture again if either
// measure were missing.
func TestClient_GetUserProfilePicture_IsNotServedFromACache(t *testing.T) {
	cases := map[string]struct{ keyByPathOnly, honorNoCache bool }{
		"cache keyed by the full URL, headers ignored":   {false, false},
		"cache keyed by the path, honoring no-cache":     {true, true},
		"cache keyed by the full URL, honoring no-cache": {false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			picture := "picture-before-the-upload"
			var queries []string
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				queries = append(queries, r.URL.RawQuery)
				w.Header().Set("Cache-Control", "public, max-age=900, stale-while-revalidate=3600")
				_, _ = w.Write([]byte(picture))
			}))
			defer origin.Close()
			proxy := profilePictureCachingProxy(t, origin, tc.keyByPathOnly, tc.honorNoCache)
			c, err := client.NewClient(proxy.URL, "test-token", false, 30)
			require.NoError(t, err)

			first, err := c.GetUserProfilePicture(context.Background(), profilePictureUser)
			require.NoError(t, err)
			assert.Equal(t, "picture-before-the-upload", string(first))

			mu.Lock()
			picture = "picture-after-the-upload"
			mu.Unlock()
			second, err := c.GetUserProfilePicture(context.Background(), profilePictureUser)
			require.NoError(t, err)
			assert.Equal(t, "picture-after-the-upload", string(second), "a cached answer would be the old picture")

			mu.Lock()
			defer mu.Unlock()
			require.Len(t, queries, 2)
			assert.NotEqual(t, queries[0], queries[1], "each call asks for a URL no cache has seen")
			assert.Contains(t, queries[0], "nocache=")
		})
	}
}

// The request carries the revalidation headers, and only on this read.
func TestClient_GetUserProfilePicture_AsksCachesToRevalidate(t *testing.T) {
	var cacheControl, pragma []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cacheControl = append(cacheControl, r.Header.Get("Cache-Control"))
		pragma = append(pragma, r.Header.Get("Pragma"))
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("png"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	_, err = c.GetUserProfilePicture(context.Background(), profilePictureUser)
	require.NoError(t, err)
	require.NoError(t, c.ResetUserProfilePicture(context.Background(), profilePictureUser))
	assert.Equal(t, []string{"no-cache", ""}, cacheControl)
	assert.Equal(t, []string{"no-cache", ""}, pragma)
}
