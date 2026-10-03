//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
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

func testAccAppImageSVG(label string) []byte {
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><title>%s</title></svg>`, label))
}

func testAccAppImagePNG(t *testing.T, shade uint8) []byte {
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
	// An eXIf chunk after IHDR (8-byte signature, 25-byte IHDR chunk): a
	// big-endian TIFF header and one IFD entry, Orientation = 1. Pocket ID
	// zeroes EXIF data on upload, so it serves different bytes.
	exif := []byte{'M', 'M', 0, 0x2a, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 0}
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(exif)))
	body := append([]byte("eXIf"), exif...)
	chunk = append(chunk, body...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))
	encoded := buf.Bytes()
	withExif := append(append(append([]byte{}, encoded[:33]...), chunk...), encoded[33:]...)
	if _, err := png.Decode(bytes.NewReader(withExif)); err != nil {
		t.Fatal(err)
	}
	return withExif
}

func testAccAppImageWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testAccAppImageHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// testAccAppImageServed checks what Pocket ID serves for an uploaded image:
// want nil means no uploaded image (image_not_found).
func testAccAppImageServed(kind client.ApplicationImage, want []byte) resource.TestCheckFunc {
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

// testAccAppImageContentType checks the media type Pocket ID serves at path.
func testAccAppImageContentType(path, want string) resource.TestCheckFunc {
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

func testAccAppImageConfig(kind, source string) string {
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
	first, second, outside := testAccAppImageSVG("first-"+acctest.RandString(6)), testAccAppImageSVG("second-"+acctest.RandString(6)), testAccAppImageSVG("outside")
	testAccAppImageWriteFile(t, source, first)
	ctx := context.Background()
	c, err := testClient()
	if err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccAppImageServed(client.ApplicationImageLogoDark, nil),
		Steps: []resource.TestStep{
			{
				Config: testAccAppImageConfig("logo_dark", source),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", "logo_dark"),
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccAppImageHash(first)),
					testAccAppImageServed(client.ApplicationImageLogoDark, first),
				),
			},
			{
				// The file's content changes: uploaded again.
				PreConfig: func() { testAccAppImageWriteFile(t, source, second) },
				Config:    testAccAppImageConfig("logo_dark", source),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccAppImageHash(second)),
					testAccAppImageServed(client.ApplicationImageLogoDark, second),
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
				Config: testAccAppImageConfig("logo_dark", source),
				Check:  testAccAppImageServed(client.ApplicationImageLogoDark, second),
			},
			{
				// Replaced outside Terraform: uploaded again.
				PreConfig: func() {
					if err := c.UploadApplicationImage(ctx, client.ApplicationImageLogoDark, "svg", outside); err != nil {
						t.Fatal(err)
					}
				},
				Config: testAccAppImageConfig("logo_dark", source),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccAppImageHash(second)),
					testAccAppImageServed(client.ApplicationImageLogoDark, second),
				),
			},
			{
				// The same bytes under another extension: uploaded again,
				// and served with the new type.
				PreConfig: func() { testAccAppImageWriteFile(t, renamed, second) },
				Config:    testAccAppImageConfig("logo_dark", renamed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccAppImageHash(second)),
					testAccAppImageServed(client.ApplicationImageLogoDark, second),
					testAccAppImageContentType("/api/application-images/logo?light=false&default=false", "image/png"),
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
	testAccAppImageWriteFile(t, favicon, testAccAppImagePNG(t, byte(acctest.RandIntRange(0, 255))))
	testAccAppImageWriteFile(t, email, testAccAppImagePNG(t, byte(acctest.RandIntRange(0, 255))))
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
			if err := testAccAppImageServed(client.ApplicationImageFavicon, faviconServed)(s); err != nil {
				return err
			}
			return testAccAppImageServed(client.ApplicationImageEmailLogo, emailServed)(s)
		},
		Steps: []resource.TestStep{
			{
				Config:      testAccAppImageConfig("favicon", filepath.Join(dir, "favicon.jpg")),
				ExpectError: regexp.MustCompile(`Unsupported image file type(?s:.*)accepts only ico,\s+png,\s+svg\s+for\s+the\s+favicon`),
				PlanOnly:    true,
			},
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("pocketid_application_image.favicon", "sha256"),
					record,
					func(*terraform.State) error {
						content, err := os.ReadFile(email)
						if err != nil {
							return err
						}
						if testAccAppImageHash(emailServed) == testAccAppImageHash(content) {
							return fmt.Errorf("the e-mail logo was served unchanged: the fixture's metadata was not stripped")
						}
						return nil
					},
				),
			},
			{
				// Served bytes differ from the file, yet a refresh plans
				// nothing.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// Destroying the background removes it; Pocket ID does not put its bundled
// background back.
func TestAccResourceApplicationImage_background(t *testing.T) {
	source := filepath.Join(t.TempDir(), "background.svg")
	content := testAccAppImageSVG("background-" + acctest.RandString(6))
	testAccAppImageWriteFile(t, source, content)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccAppImageServed(client.ApplicationImageBackground, nil),
		Steps: []resource.TestStep{
			{
				Config: testAccAppImageConfig("background", source),
				Check:  testAccAppImageServed(client.ApplicationImageBackground, content),
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

// Pocket ID strips the EXIF data of a PNG it receives, so it serves other
// bytes than the file's. The plan stays empty across saved refreshes, and
// the baseline recorded after the upload (in private state) survives them:
// an image replaced outside Terraform afterwards is still found and
// uploaded again.
func TestAccResourceApplicationImage_metadataStripped(t *testing.T) {
	resourceName := "pocketid_application_image.test"
	source := filepath.Join(t.TempDir(), "avatar.png")
	content := testAccAppImagePNG(t, byte(acctest.RandIntRange(0, 255)))
	testAccAppImageWriteFile(t, source, content)
	c, err := testClient()
	if err != nil {
		t.Fatal(err)
	}
	var served []byte
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccAppImageServed(client.ApplicationImageDefaultProfilePicture, nil),
		Steps: []resource.TestStep{
			{
				Config: testAccAppImageConfig("default_profile_picture", source),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "sha256", testAccAppImageHash(content)),
					func(*terraform.State) error {
						var err error
						if served, err = c.GetApplicationImage(context.Background(), client.ApplicationImageDefaultProfilePicture); err != nil {
							return err
						}
						if testAccAppImageHash(served) == testAccAppImageHash(content) {
							return fmt.Errorf("the uploaded PNG was served unchanged: the fixture's metadata was not stripped")
						}
						return nil
					},
				),
			},
			// Two refreshes that are saved (a PlanOnly step's refresh is
			// not), each followed by an empty plan: Read must carry the
			// baseline in private state forward, or the replacement below
			// goes unnoticed.
			{RefreshState: true, Check: resource.TestCheckResourceAttr(resourceName, "sha256", testAccAppImageHash(content))},
			{RefreshState: true, Check: resource.TestCheckResourceAttr(resourceName, "sha256", testAccAppImageHash(content))},
			{
				// Replaced outside Terraform: found against the recorded
				// baseline, and uploaded again.
				PreConfig: func() {
					if err := c.UploadApplicationImage(context.Background(), client.ApplicationImageDefaultProfilePicture, "svg", testAccAppImageSVG("outside")); err != nil {
						t.Fatal(err)
					}
				},
				Config: testAccAppImageConfig("default_profile_picture", source),
				Check: func(s *terraform.State) error {
					return testAccAppImageServed(client.ApplicationImageDefaultProfilePicture, served)(s)
				},
			},
			{Config: testAccAppImageConfig("default_profile_picture", source), PlanOnly: true},
		},
	})
}
