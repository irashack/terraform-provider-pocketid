package client_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestFindLogoPreset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/oidc/logo-presets", r.URL.Path)
		assert.Equal(t, "jellyfin", r.URL.Query().Get("search"))
		_, _ = fmt.Fprint(w, `[{"name":"Jellyfin Vue","reference":"jellyfin-vue","logoUrl":"https://cdn.example/svg/jellyfin-vue.svg"},`+
			`{"name":"Jellyfin","reference":"jellyfin","logoUrl":"https://cdn.example/svg/jellyfin.svg","darkLogoUrl":"https://cdn.example/svg/jellyfin-light.svg"}]`)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)

	preset, found, err := c.FindLogoPreset(context.Background(), "jellyfin")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "https://cdn.example/svg/jellyfin.svg", preset.LogoURL)
	require.NotNil(t, preset.DarkLogoURL)

	_, err = c.SearchLogoPresets(context.Background(), "has synthetic-token in it")
	assert.ErrorIs(t, err, client.ErrInvalidIdentifier, "a search carrying the key is not sent")
}

func TestDownloadLogo(t *testing.T) {
	var headers []http.Header
	icons := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Clone())
		switch r.URL.Path {
		case "/svg/ok.svg":
			_, _ = fmt.Fprint(w, "<svg/>")
		case "/svg/redirect.svg":
			http.Redirect(w, r, "/svg/ok.svg", http.StatusFound)
		case "/svg/ftp.svg":
			w.Header().Set("Location", "ftp://example.invalid/x.svg")
			w.WriteHeader(http.StatusFound)
		case "/svg/loop.svg":
			http.Redirect(w, r, "/svg/loop.svg", http.StatusFound)
		case "/png/big.png":
			_, _ = w.Write(bytes.Repeat([]byte{'x'}, client.ClientLogoMaxBytes+1))
		case "/svg/empty.svg":
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer icons.Close()
	c, err := client.NewClient("https://pocket.example", "synthetic-token", true, 2)
	require.NoError(t, err)
	ctx := context.Background()

	content, extension, err := c.DownloadLogo(ctx, icons.URL+"/svg/ok.svg")
	require.NoError(t, err)
	assert.Equal(t, "<svg/>", string(content))
	assert.Equal(t, "svg", extension)
	content, _, err = c.DownloadLogo(ctx, icons.URL+"/svg/redirect.svg")
	require.NoError(t, err)
	assert.Equal(t, "<svg/>", string(content))
	for _, h := range headers {
		assert.Empty(t, h.Get("X-API-Key"))
		assert.Empty(t, h.Get("Authorization"))
		assert.Empty(t, h.Get("Cookie"))
	}

	for name, tc := range map[string]struct{ url, want string }{
		"not found":         {icons.URL + "/svg/missing.svg", "HTTP 404"},
		"too large":         {icons.URL + "/png/big.png", "larger than"},
		"empty":             {icons.URL + "/svg/empty.svg", "empty"},
		"redirect to ftp":   {icons.URL + "/svg/ftp.svg", "not http or https"},
		"redirect loop":     {icons.URL + "/svg/loop.svg", "redirects"},
		"not an image type": {icons.URL + "/index.json", "does not name an image type"},
		"not http":          {"file:///etc/passwd.svg", "absolute http or https"},
		"credentials":       {"https://user:pw@cdn.example/x.svg", "without credentials"},
		"carries the key":   {icons.URL + "/svg/synthetic-token.svg", "contains the API key"},
	} {
		_, _, err := c.DownloadLogo(ctx, tc.url)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
}
