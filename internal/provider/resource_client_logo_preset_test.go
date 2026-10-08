//go:build acc
// +build acc

package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/stretchr/testify/require"
)

func testAccClientLogoPresetConfig(name, light, dark string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "app" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
}

resource "pocketid_client_logo" "light" {
  client_id = pocketid_client.app.id
  preset    = %q
}

resource "pocketid_client_logo" "dark" {
  client_id = pocketid_client.app.id
  variant   = "dark"
  preset    = %q
}

data "pocketid_logo_presets" "search" {
  search = "jellyfin"
}
`, name, light, dark)
}

// Logos from the icon library (Pocket ID 2.18.0 and later; the fixture's
// default library is the selfh.st icons on jsDelivr, so this needs internet
// access from the fixture and from the test host): upload both variants,
// detect a logo replaced outside Terraform, switch icons. An older server
// refuses preset at plan time.
func TestAccResourceClientLogo_preset(t *testing.T) {
	name := "tf-acc-logo-preset-" + acctest.RandString(6)
	if !testAccServerAtLeast(t, "2.18.0") {
		resource.Test(t, resource.TestCase{
			PreCheck:                 func() { testAccPreCheck(t) },
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      testAccClientLogoPresetConfig(name, "jellyfin", "jellyfin"),
				ExpectError: regexp.MustCompile(`2\.18\.0`),
			}},
		})
		return
	}
	var clientID string
	c, err := testClient()
	require.NoError(t, err)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:           testAccClientLogoPresetConfig(name, "jellyfin", "jellyfin"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPreRefresh: testAccConverged, PostApplyPostRefresh: testAccConverged},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client_logo.light", "preset", "jellyfin"),
					resource.TestCheckResourceAttrSet("pocketid_client_logo.light", "sha256"),
					resource.TestCheckResourceAttrSet("pocketid_client_logo.dark", "sha256"),
					resource.TestCheckNoResourceAttr("pocketid_client_logo.light", "source"),
					resource.TestCheckTypeSetElemNestedAttrs("data.pocketid_logo_presets.search", "presets.*", map[string]string{"reference": "jellyfin"}),
					testAccClientSecretResCapture("pocketid_client.app", "id", &clientID),
					testAccClientLogoServer(&clientID, true, true, nil),
				),
			},
			{
				// Replaced outside Terraform: uploaded again from the icon.
				PreConfig: func() {
					require.NoError(t, c.UploadClientLogo(context.Background(), clientID, false, "svg", testAccClientLogoSVG("someone else's")))
				},
				Config: testAccClientLogoPresetConfig(name, "jellyfin", "jellyfin"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPreRefresh:  testAccConverged,
					PostApplyPostRefresh: testAccConverged,
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pocketid_client_logo.dark", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pocketid_client_logo.light", plancheck.ResourceActionNoop),
					},
				},
				Check: testAccClientLogoServer(&clientID, true, true, nil),
			},
			{
				// Another icon for the light logo.
				Config: testAccClientLogoPresetConfig(name, "plex", "jellyfin"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPreRefresh:  testAccConverged,
					PostApplyPostRefresh: testAccConverged,
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pocketid_client_logo.light", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pocketid_client_logo.dark", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.TestCheckResourceAttr("pocketid_client_logo.light", "preset", "plex"),
			},
			{
				// A reference the library does not have fails the plan.
				Config:      testAccClientLogoPresetConfig(name, "no-such-icon-anywhere", "jellyfin"),
				ExpectError: regexp.MustCompile(`no icon with the reference`),
			},
		},
	})
}
