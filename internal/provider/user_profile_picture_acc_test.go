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
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// ppAccImage encodes a picture whose content depends on seed.
func ppAccImage(t *testing.T, w, h int, seed uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x) + seed, uint8(y) * seed, seed, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the test image: %v", err)
	}
	return buf.Bytes()
}

func ppAccDigest(b []byte) string {
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

// ppAccServed returns the digest of the picture the server serves for the user.
func ppAccServed(userID string) (string, error) {
	c, err := testClient()
	if err != nil {
		return "", err
	}
	picture, err := c.GetUserProfilePicture(context.Background(), userID)
	if err != nil {
		return "", err
	}
	return ppAccDigest(picture), nil
}

func ppAccConfig(userID, source string) string {
	return fmt.Sprintf(`
resource "pocketid_user_profile_picture" "test" {
  user_id = %q
  source  = %q
}
`, userID, source)
}

// ppAccCheckServed checks whether the server serves its default picture, and
// that the served picture is none of differsFrom.
func ppAccCheckServed(userID string, wantDefault bool, defaultDigest string, differsFrom ...*string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		served, err := ppAccServed(userID)
		if err != nil {
			return err
		}
		if wantDefault != (served == defaultDigest) {
			return fmt.Errorf("the server serves the default picture: %v, want %v", served == defaultDigest, wantDefault)
		}
		for _, other := range differsFrom {
			if other != nil && *other == served {
				return fmt.Errorf("the served picture did not change")
			}
		}
		return nil
	}
}

// The picture follows the file through create, update and drift, and the
// default is back after destroy. The server never serves the file's own bytes
// (it stores a 300x300 PNG), so each check is against the served picture.
func TestAccResourceUserProfilePicture_Lifecycle(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-pp")
	userID := gmAccUser(t, name)
	defaultDigest, err := ppAccServed(userID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "me.png")
	first := ppAccImage(t, 120, 80, 3)
	if err := os.WriteFile(file, first, 0o600); err != nil {
		t.Fatal(err)
	}
	var servedAfterFirst string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return ppAccCheckServed(userID, true, defaultDigest)(s) },
		Steps: []resource.TestStep{
			{
				Config: ppAccConfig(userID, file),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user_profile_picture.test", "id", userID),
					resource.TestCheckResourceAttr("pocketid_user_profile_picture.test", "sha256", ppAccDigest(first)),
					ppAccCheckServed(userID, false, defaultDigest),
					func(*terraform.State) error {
						var err error
						servedAfterFirst, err = ppAccServed(userID)
						return err
					},
					resource.TestCheckResourceAttrWith("pocketid_user_profile_picture.test", "stored_sha256", func(value string) error {
						if value != servedAfterFirst {
							return fmt.Errorf("stored_sha256 is not the digest of the served picture")
						}
						return nil
					}),
				),
			},
			{
				// Nothing changed: an empty plan, so the stored digest matches what is served.
				Config:   ppAccConfig(userID, file),
				PlanOnly: true,
			},
			{
				// A new file content is uploaded again.
				PreConfig: func() {
					if err := os.WriteFile(file, ppAccImage(t, 120, 80, 9), 0o600); err != nil {
						t.Fatal(err)
					}
				},
				Config: ppAccConfig(userID, file),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user_profile_picture.test", "sha256", ppAccDigest(ppAccImage(t, 120, 80, 9))),
					ppAccCheckServed(userID, false, defaultDigest, &servedAfterFirst),
				),
			},
			{
				// The picture is removed outside Terraform: the plan shows it.
				PreConfig: func() {
					c, err := testClient()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.ResetUserProfilePicture(context.Background(), userID); err != nil {
						t.Fatalf("removing the picture out of band: %v", err)
					}
				},
				Config:             ppAccConfig(userID, file),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Applying uploads the file again.
				Config: ppAccConfig(userID, file),
				Check:  ppAccCheckServed(userID, false, defaultDigest),
			},
			{
				// The picture is replaced outside Terraform: the plan shows that too.
				PreConfig: func() {
					c, err := testClient()
					if err != nil {
						t.Fatal(err)
					}
					err = c.UploadUserProfilePicture(context.Background(), userID, client.MultipartFile{FileName: "other.png", ContentType: "image/png", Content: ppAccImage(t, 50, 50, 77)})
					if err != nil {
						t.Fatalf("replacing the picture out of band: %v", err)
					}
				},
				Config:             ppAccConfig(userID, file),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: ppAccConfig(userID, file),
				Check:  resource.TestCheckResourceAttr("pocketid_user_profile_picture.test", "sha256", ppAccDigest(ppAccImage(t, 120, 80, 9))),
			},
		},
	})
}

// A file Pocket ID cannot use is refused at plan time, on every version: not
// an image, too many pixels, and one that is not there when the change is
// applied.
func TestAccResourceUserProfilePicture_UnusableFiles(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-pp")
	userID := gmAccUser(t, name)
	dir := t.TempDir()
	notImage := filepath.Join(dir, "notes.png")
	if err := os.WriteFile(notImage, []byte("this is not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooBig := filepath.Join(dir, "big.png")
	if err := os.WriteFile(tooBig, ppAccImage(t, 4001, 4000, 0), 0o600); err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ppAccConfig(userID, notImage), PlanOnly: true, ExpectError: regexp.MustCompile(`not an image Pocket ID can read`)},
			{Config: ppAccConfig(userID, tooBig), PlanOnly: true, ExpectError: regexp.MustCompile(`16000000`)},
			{Config: ppAccConfig(userID, filepath.Join(dir, "absent.png")), ExpectError: regexp.MustCompile(`does not exist`)},
		},
	})
}

// A user that is deleted outside Terraform takes the picture resource with it
// at the next refresh; destroying it afterwards is not an error.
func TestAccResourceUserProfilePicture_UserDeletedOutsideTerraform(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-pp")
	userID := gmAccUser(t, name)
	file := filepath.Join(t.TempDir(), "me.png")
	if err := os.WriteFile(file, ppAccImage(t, 60, 60, 4), 0o600); err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: ppAccConfig(userID, file), Check: ppAccCheckServed(userID, false, "")},
			{
				PreConfig: func() {
					if status, err := testAccAPI("DELETE", "/api/users/"+userID, nil, nil); err != nil || status != 204 {
						t.Fatalf("deleting the user answered %d", status)
					}
				},
				Config:             ppAccConfig(userID, file),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// A picture that someone else uploaded after the provider's own is not removed
// by destroy: the destroy stops, the replacement is still served, and applying
// the configuration again restores the file's picture so that destroy succeeds.
func TestAccResourceUserProfilePicture_DestroyKeepsAReplacementPicture(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-pp")
	userID := gmAccUser(t, name)
	defaultDigest, err := ppAccServed(userID)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "me.png")
	if err := os.WriteFile(file, ppAccImage(t, 120, 80, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	var replacement string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return ppAccCheckServed(userID, true, defaultDigest)(s) },
		Steps: []resource.TestStep{
			{
				Config: ppAccConfig(userID, file),
				Check:  ppAccCheckServed(userID, false, defaultDigest),
			},
			{
				PreConfig: func() {
					c, err := testClient()
					if err != nil {
						t.Fatal(err)
					}
					err = c.UploadUserProfilePicture(context.Background(), userID, client.MultipartFile{FileName: "other.png", ContentType: "image/png", Content: ppAccImage(t, 50, 50, 77)})
					if err != nil {
						t.Fatalf("replacing the picture out of band: %v", err)
					}
					if replacement, err = ppAccServed(userID); err != nil {
						t.Fatal(err)
					}
				},
				Config:      ppAccConfig(userID, file),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`not the one this resource uploaded`),
			},
			{
				// The refused destroy changed nothing on the server.
				PreConfig: func() {
					served, err := ppAccServed(userID)
					if err != nil {
						t.Fatal(err)
					}
					if served != replacement {
						t.Fatal("the destroy that was refused still changed the user's picture")
					}
				},
				Config: ppAccConfig(userID, file),
				Check:  ppAccCheckServed(userID, false, defaultDigest, &replacement),
			},
		},
	})
}
