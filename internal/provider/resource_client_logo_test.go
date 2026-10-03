//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func testAccClientLogoHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func testAccClientLogoWrite(t *testing.T, file string, content []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(file, content, 0o600))
}

func testAccClientLogoConfig(name, lightSource, darkSource string) string {
	config := testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "app" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
}
`, name)
	if lightSource != "" {
		config += fmt.Sprintf(`
resource "pocketid_client_logo" "light" {
  client_id = pocketid_client.app.id
  source    = %q
}
`, lightSource)
	}
	if darkSource != "" {
		config += fmt.Sprintf(`
resource "pocketid_client_logo" "dark" {
  client_id = pocketid_client.app.id
  variant   = "dark"
  source    = %q
}
`, darkSource)
	}
	return config
}

// testAccClientLogoServer checks the client's logo flags and, for an SVG
// (served as uploaded), that Pocket ID serves wantDark as the dark logo.
func testAccClientLogoServer(clientID *string, wantLight, wantDark bool, darkContent func() []byte) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, err := testClient()
		if err != nil {
			return err
		}
		current, err := c.GetClient(context.Background(), *clientID)
		if err != nil {
			return err
		}
		if current.HasLogo != wantLight || current.HasDarkLogo != wantDark {
			return fmt.Errorf("client reports light %t dark %t, want %t %t", current.HasLogo, current.HasDarkLogo, wantLight, wantDark)
		}
		if darkContent != nil {
			served, err := c.GetClientLogo(context.Background(), *clientID, false)
			if err != nil {
				return err
			}
			if !bytes.Equal(served, darkContent()) {
				return fmt.Errorf("Pocket ID serves another dark logo than the file")
			}
		}
		return nil
	}
}

// Upload, change the file, detect a logo replaced and one removed outside
// Terraform, import, and remove.
func TestAccResourceClientLogo_lifecycle(t *testing.T) {
	dir := t.TempDir()
	lightFile, darkFile := filepath.Join(dir, "light.png"), filepath.Join(dir, "dark.svg")
	lightV1, lightV2 := testAccClientLogoPNG(t, 20), testAccClientLogoPNG(t, 200)
	dark := testAccClientLogoSVG("dark v1")
	testAccClientLogoWrite(t, lightFile, lightV1)
	testAccClientLogoWrite(t, darkFile, dark)
	name := "tf-acc-logo-" + acctest.RandString(6)
	config := testAccClientLogoConfig(name, lightFile, darkFile)
	var clientID string
	darkNow := func() []byte { return dark }
	c, err := testClient()
	require.NoError(t, err)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client_logo.light", "variant", "light"),
					resource.TestCheckResourceAttr("pocketid_client_logo.light", "sha256", testAccClientLogoHash(lightV1)),
					resource.TestCheckResourceAttr("pocketid_client_logo.dark", "sha256", testAccClientLogoHash(dark)),
					resource.TestCheckResourceAttrPair("pocketid_client_logo.light", "client_id", "pocketid_client.app", "id"),
					testAccClientSecretResCapture("pocketid_client.app", "id", &clientID),
					func(s *terraform.State) error {
						if got, want := s.RootModule().Resources["pocketid_client_logo.dark"].Primary.ID, clientID+"/dark"; got != want {
							return fmt.Errorf("id %s, want %s", got, want)
						}
						return nil
					},
					testAccClientLogoServer(&clientID, true, true, darkNow),
				),
			},
			{
				// A changed file is uploaded again; the other logo is untouched.
				PreConfig: func() { testAccClientLogoWrite(t, lightFile, lightV2) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pocketid_client_logo.light", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pocketid_client_logo.dark", plancheck.ResourceActionNoop),
						plancheck.ExpectResourceAction("pocketid_client.app", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client_logo.light", "sha256", testAccClientLogoHash(lightV2)),
					testAccClientLogoServer(&clientID, true, true, darkNow),
				),
			},
			{
				// Replaced outside Terraform: detected through the served image.
				PreConfig: func() {
					require.NoError(t, c.UploadClientLogo(context.Background(), clientID, false, "svg", testAccClientLogoSVG("someone else's")))
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pocketid_client_logo.dark", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pocketid_client_logo.light", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client_logo.dark", "sha256", testAccClientLogoHash(dark)),
					testAccClientLogoServer(&clientID, true, true, darkNow),
				),
			},
			{
				// Removed outside Terraform: uploaded again.
				PreConfig: func() { require.NoError(t, c.DeleteClientLogo(context.Background(), clientID, true)) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pocketid_client_logo.light", plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction("pocketid_client_logo.dark", plancheck.ResourceActionNoop),
					},
				},
				Check: testAccClientLogoServer(&clientID, true, true, darkNow),
			},
			{
				ResourceName:            "pocketid_client_logo.light",
				ImportState:             true,
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return clientID + "/light", nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"source", "sha256"},
			},
			{
				ResourceName:            "pocketid_client_logo.dark",
				ImportState:             true,
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return clientID + "/dark", nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"source"},
			},
			{
				// Destroying the dark logo leaves the light one.
				Config: testAccClientLogoConfig(name, lightFile, ""),
				Check:  testAccClientLogoServer(&clientID, true, false, nil),
			},
			{
				Config: testAccClientLogoConfig(name, "", ""),
				Check:  testAccClientLogoServer(&clientID, false, false, nil),
			},
		},
	})
}

// Files Pocket ID would refuse are refused at plan, before any upload.
func TestAccResourceClientLogo_refusedAtPlan(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.svg")
	testAccClientLogoWrite(t, big, bytes.Repeat([]byte{'x'}, client.ClientLogoMaxBytes+1))
	name := "tf-acc-logo-refused-" + acctest.RandString(6)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccClientLogoConfig(name, filepath.Join(dir, "logo.bmp"), ""),
				ExpectError: regexp.MustCompile(`Unsupported logo file type`),
			},
			{
				Config:      testAccClientLogoConfig(name, big, ""),
				ExpectError: regexp.MustCompile(`larger than`),
			},
		},
	})
}
