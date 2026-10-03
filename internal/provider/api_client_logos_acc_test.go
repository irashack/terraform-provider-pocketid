//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// testAccClientLogoPNG returns a small PNG filled with one colour.
func testAccClientLogoPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: shade, G: 255 - shade, B: 64, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func testAccClientLogoSVG(label string) []byte {
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><title>%s</title><rect width="8" height="8"/></svg>`, label))
}

// testAccClientLogoRawUpload posts a multipart logo the client would refuse
// to send, to observe the server's own limits. The response body is not read.
func testAccClientLogoRawUpload(t *testing.T, clientID, fileName string, content []byte) int {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req, err := http.NewRequest("POST", os.Getenv("POCKETID_BASE_URL")+"/api/oidc/clients/"+clientID+"/logo?light=true", &body)
	require.NoError(t, err)
	req.Header.Set("X-API-KEY", os.Getenv("POCKETID_API_TOKEN"))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = response.Body.Close()
	return response.StatusCode
}

// What pocketid_client_logo relies on: GET of a client reports hasLogo and
// hasDarkLogo; asked for a dark logo the client lacks, Pocket ID serves the
// light one; it serves an SVG as uploaded; a missing logo is image_not_found;
// the 2 MiB limit covers the whole request and the type comes from the
// extension.
func TestAccAPI_clientLogos(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)
	created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
		Name: "tf-acc-logo-api-" + acctest.RandString(6), CallbackURLs: []string{"https://example.com/callback"},
		IsPublic: true, PkceEnabled: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), created.ID) })

	flags := func() (bool, bool) {
		current, err := c.GetClient(ctx, created.ID)
		require.NoError(t, err)
		return current.HasLogo, current.HasDarkLogo
	}
	_, err = c.GetClientLogo(ctx, created.ID, true)
	assert.True(t, client.IsNotFound(err, client.ResourceImage), "no light logo yet")
	assert.True(t, client.IsNotFound(c.DeleteClientLogo(ctx, created.ID, false), client.ResourceImage), "no dark logo yet")

	require.NoError(t, c.UploadClientLogo(ctx, created.ID, true, "png", testAccClientLogoPNG(t, 10)))
	light, dark := flags()
	assert.True(t, light)
	assert.False(t, dark, "GET reports hasDarkLogo")
	servedLight, err := c.GetClientLogo(ctx, created.ID, true)
	require.NoError(t, err)
	fallback, err := c.GetClientLogo(ctx, created.ID, false)
	require.NoError(t, err)
	assert.Equal(t, servedLight, fallback, "without a dark logo the light one is served")

	svg := testAccClientLogoSVG("dark")
	require.NoError(t, c.UploadClientLogo(ctx, created.ID, false, "svg", svg))
	light, dark = flags()
	assert.True(t, light)
	assert.True(t, dark, "GET reports hasDarkLogo")
	servedDark, err := c.GetClientLogo(ctx, created.ID, false)
	require.NoError(t, err)
	assert.Equal(t, svg, servedDark, "an SVG is served as uploaded")

	require.NoError(t, c.UploadClientLogo(ctx, created.ID, true, "jpg", bytes.Repeat([]byte{'x'}, client.ClientLogoMaxBytes)), "the largest file the client sends fits")
	assert.Equal(t, http.StatusRequestEntityTooLarge, testAccClientLogoRawUpload(t, created.ID, "logo.svg", bytes.Repeat([]byte{'x'}, 2<<20)), "the limit covers the whole request")
	assert.Equal(t, http.StatusUnsupportedMediaType, testAccClientLogoRawUpload(t, created.ID, "logo.bmp", []byte("x")))

	require.NoError(t, c.DeleteClientLogo(ctx, created.ID, false))
	light, dark = flags()
	assert.True(t, light, "deleting the dark logo keeps the light one")
	assert.False(t, dark)
	assert.True(t, client.IsNotFound(c.DeleteClientLogo(ctx, created.ID, false), client.ResourceImage))
	require.NoError(t, c.DeleteClientLogo(ctx, created.ID, true))
	light, _ = flags()
	assert.False(t, light)

	const missing = "tf-acc-missing-logo-client"
	assert.True(t, client.IsNotFound(c.DeleteClientLogo(ctx, missing, true), client.ResourceOIDCClient))
	_, err = c.GetClientLogo(ctx, missing, true)
	assert.True(t, client.IsNotFound(err, client.ResourceOIDCClient))
}
