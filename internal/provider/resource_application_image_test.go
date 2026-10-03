//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func testAccSVG(label string) []byte {
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><title>%s</title></svg>`, label))
}

func testAccPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			img.Set(x, y, color.RGBA{R: shade, G: 0x40, B: 0x80, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testAccWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testAccHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// testAccImageServed checks what Pocket ID serves for an uploaded image:
// want nil means no uploaded image (image_not_found).
func testAccImageServed(kind client.ApplicationImage, want []byte) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, err := testClient()
		if err != nil {
			return err
		}
		got, err := c.GetApplicationImage(context.Background(), kind)
		if want == nil {
			if client.IsNotFound(err, client.ResourceImage) {
				return nil
			}
			return fmt.Errorf("%s: want no uploaded image, got err=%v and %d bytes", kind, err, len(got))
		}
		if err != nil {
			return fmt.Errorf("%s: %w", kind, err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("%s: Pocket ID serves %d bytes that differ from the expected %d", kind, len(got), len(want))
		}
		return nil
	}
}

// testAccImageContentType checks the media type Pocket ID serves at path.
func testAccImageContentType(path, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		req, err := http.NewRequest(http.MethodGet, os.Getenv("POCKETID_BASE_URL")+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-API-KEY", os.Getenv("POCKETID_API_TOKEN"))
		req.Header.Set("Cache-Control", "no-cache")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("GET %s failed", path)
		}
		defer func() { _ = response.Body.Close() }()
		if got := response.Header.Get("Content-Type"); got != want {
			return fmt.Errorf("GET %s: Content-Type %q, want %q", path, got, want)
		}
		return nil
	}
}

func testAccImageConfig(kind, source string) string {
	return fmt.Sprintf(`
resource "pocketid_application_image" "test" {
  kind   = %q
  source = %q
}
`, kind, source)
}

// A logo: upload, a changed file, removal and replacement outside Terraform
// (each planned and repaired), import, and destroy removing it.
func TestAccResourceApplicationImage_logo(t *testing.T) {
	resourceName := "pocketid_application_image.test"
	source := filepath.Join(t.TempDir(), "logo.svg")
	renamed := filepath.Join(filepath.Dir(source), "logo.png")
	first, second, outside := testAccSVG("first-"+acctest.RandString(6)), testAccSVG("second-"+acctest.RandString(6)), testAccSVG("outside")
	testAccWriteFile(t, source, first)
	ctx := context.Background()
	c, err := testClient()
	if err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccImageServed(client.ApplicationImageLogoDark, nil),
		Steps: []resource.TestStep{
			{
				Config: testAccImageConfig("logo_dark", source),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", "logo_dark"),
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccHash(first)),
					testAccImageServed(client.ApplicationImageLogoDark, first),
				),
			},
			{
				// The file's content changes: uploaded again.
				PreConfig: func() { testAccWriteFile(t, source, second) },
				Config:    testAccImageConfig("logo_dark", source),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccHash(second)),
					testAccImageServed(client.ApplicationImageLogoDark, second),
				),
			},
			{
				// Removed outside Terraform: the refresh drops it and the
				// apply uploads it again.
				PreConfig: func() {
					if err := c.DeleteApplicationImage(ctx, client.ApplicationImageLogoDark); err != nil {
						t.Fatal(err)
					}
				},
				Config: testAccImageConfig("logo_dark", source),
				Check:  testAccImageServed(client.ApplicationImageLogoDark, second),
			},
			{
				// Replaced outside Terraform: uploaded again.
				PreConfig: func() {
					if err := c.UploadApplicationImage(ctx, client.ApplicationImageLogoDark, "svg", outside); err != nil {
						t.Fatal(err)
					}
				},
				Config: testAccImageConfig("logo_dark", source),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccHash(second)),
					testAccImageServed(client.ApplicationImageLogoDark, second),
				),
			},
			{
				// The same bytes under another extension: uploaded again,
				// and served with the new type.
				PreConfig: func() { testAccWriteFile(t, renamed, second) },
				Config:    testAccImageConfig("logo_dark", renamed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccHash(second)),
					testAccImageServed(client.ApplicationImageLogoDark, second),
					testAccImageContentType("/api/application-images/logo?light=false&default=false", "image/png"),
				),
			},
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateId:           "logo_dark",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"source"},
			},
		},
	})
}

// The favicon and e-mail logo cannot be removed: destroying them leaves the
// uploaded images. PNG files (whose metadata Pocket ID strips) plan empty
// after the upload. A file type the image does not accept fails the plan.
func TestAccResourceApplicationImage_notRemovable(t *testing.T) {
	dir := t.TempDir()
	favicon, email := filepath.Join(dir, "favicon.png"), filepath.Join(dir, "email.png")
	testAccWriteFile(t, favicon, testAccPNG(t, byte(acctest.RandIntRange(0, 255))))
	testAccWriteFile(t, email, testAccPNG(t, byte(acctest.RandIntRange(0, 255))))
	c, err := testClient()
	if err != nil {
		t.Fatal(err)
	}
	var faviconServed, emailServed []byte
	record := func(*terraform.State) error {
		var err error
		if faviconServed, err = c.GetApplicationImage(context.Background(), client.ApplicationImageFavicon); err != nil {
			return err
		}
		emailServed, err = c.GetApplicationImage(context.Background(), client.ApplicationImageEmailLogo)
		return err
	}
	config := fmt.Sprintf(`
resource "pocketid_application_image" "favicon" {
  kind   = "favicon"
  source = %q
}

resource "pocketid_application_image" "email" {
  kind   = "email_logo"
  source = %q
}
`, favicon, email)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			if err := testAccImageServed(client.ApplicationImageFavicon, faviconServed)(s); err != nil {
				return err
			}
			return testAccImageServed(client.ApplicationImageEmailLogo, emailServed)(s)
		},
		Steps: []resource.TestStep{
			{
				Config:      testAccImageConfig("favicon", filepath.Join(dir, "favicon.jpg")),
				ExpectError: regexp.MustCompile(`Unsupported image file type(?s:.*)accepts only ico,\s+png,\s+svg\s+for\s+the\s+favicon`),
				PlanOnly:    true,
			},
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("pocketid_application_image.favicon", "sha256"),
					record,
				),
			},
		},
	})
}

// Destroying the background removes it; Pocket ID does not put its bundled
// background back.
func TestAccResourceApplicationImage_background(t *testing.T) {
	source := filepath.Join(t.TempDir(), "background.svg")
	content := testAccSVG("background-" + acctest.RandString(6))
	testAccWriteFile(t, source, content)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccImageServed(client.ApplicationImageBackground, nil),
		Steps: []resource.TestStep{
			{
				Config: testAccImageConfig("background", source),
				Check:  testAccImageServed(client.ApplicationImageBackground, content),
			},
		},
	})
}

// What pocketid_application_image relies on: without an uploaded logo, a GET
// with default=false answers image_not_found (2.15.0 and later answer the
// bare GET with a bundled logo; 2.14.0 answers it with image_not_found too),
// while the e-mail logo and favicon are always served, because Pocket ID
// copies its bundled ones into place at startup.
func TestAccAPI_applicationImageDefaults(t *testing.T) {
	testAccPreCheck(t)
	c, err := testClient()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.DeleteApplicationImage(ctx, client.ApplicationImageLogoLight); err != nil && !client.IsNotFound(err, client.ResourceImage) {
		t.Fatal(err)
	}
	if _, err := c.GetApplicationImage(ctx, client.ApplicationImageLogoLight); !client.IsNotFound(err, client.ResourceImage) {
		t.Fatalf("logo with default=false and none uploaded: want image_not_found, got %v", err)
	}
	status, err := testAccAPI("GET", "/api/application-images/logo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 404
	if testAccServerAtLeast(t, "2.15.0") {
		want = 200
	}
	if status != want {
		t.Fatalf("logo without default=false: HTTP %d, want %d", status, want)
	}
	for _, kind := range []client.ApplicationImage{client.ApplicationImageFavicon, client.ApplicationImageEmailLogo} {
		if _, err := c.GetApplicationImage(ctx, kind); err != nil {
			t.Fatalf("%s with default=false: %v", kind, err)
		}
	}
}
