//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func testAccSettingsConfig(name, attributes string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
%s}
`, name, attributes)
}

// testAccCheckServerSettings compares the client's optional settings on the
// server.
func testAccCheckServerSettings(description string, skip bool, access, refresh int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		var got struct {
			Description string `json:"description"`
			SkipConsent bool   `json:"skipConsent"`
			Access      int64  `json:"accessTokenDurationMinutes"`
			Refresh     int64  `json:"refreshTokenDurationMinutes"`
		}
		id := s.RootModule().Resources["pocketid_client.test"].Primary.ID
		if status, err := testAccAPI("GET", "/api/oidc/clients/"+id, nil, &got); err != nil || status != http.StatusOK {
			return fmt.Errorf("client read failed: HTTP %d %v", status, err)
		}
		if got.Description != description || got.SkipConsent != skip || got.Access != access || got.Refresh != refresh {
			return fmt.Errorf("server settings are %+v", got)
		}
		return nil
	}
}

// description, skip_consent and the token lifetimes are unmanaged unless set:
// omitted, the server's values (also ones set in the admin UI) are kept and
// shown; set, they are authoritative.
func TestAccResourceClient_unmanagedUnlessSet(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-settings")
	var id string
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccSettingsConfig(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "description", ""),
					resource.TestCheckResourceAttr("pocketid_client.test", "skip_consent", "false"),
					resource.TestCheckResourceAttr("pocketid_client.test", "access_token_duration_minutes", "60"),
					resource.TestCheckResourceAttr("pocketid_client.test", "refresh_token_duration_minutes", "43200"),
					resource.TestCheckResourceAttr("pocketid_client.test", "client_type", "standard"),
					resource.TestCheckResourceAttr("pocketid_client.test", "has_dark_logo", "false"),
					resource.TestCheckResourceAttr("pocketid_client.test", "pkce_supported", "false"),
					func(s *terraform.State) error {
						id = s.RootModule().Resources["pocketid_client.test"].Primary.ID
						return nil
					},
				),
			},
			{
				PreConfig: func() {
					if err := testAccModifyClient(id, func(req *client.OIDCClientCreateRequest) {
						req.Description, req.SkipConsent = "from the admin UI", true
						req.AccessTokenDurationMinutes, req.RefreshTokenDurationMinutes = 15, 120
					}); err != nil {
						t.Fatalf("changing settings outside Terraform: %v", err)
					}
				},
				Config: testAccSettingsConfig(name+"-renamed", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckServerSettings("from the admin UI", true, 15, 120),
					resource.TestCheckResourceAttr("pocketid_client.test", "description", "from the admin UI"),
					resource.TestCheckResourceAttr("pocketid_client.test", "access_token_duration_minutes", "15"),
				),
			},
			{
				Config: testAccSettingsConfig(name, "  description = \"managed\"\n  skip_consent = false\n  access_token_duration_minutes = 30\n  refresh_token_duration_minutes = 600\n"),
				Check:  testAccCheckServerSettings("managed", false, 30, 600),
			},
			{ResourceName: "pocketid_client.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"client_secret"}},
			{Config: testAccSettingsConfig(name, ""), ConfigPlanChecks: empty},
			{
				Config: testAccSettingsConfig(name, "  description = \"\"\n"),
				Check:  testAccCheckServerSettings("", false, 30, 600),
			},
		},
	})
}

func TestAccResourceClient_tokenDurationValidation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccSettingsConfig("duration", "  access_token_duration_minutes = 525601\n"),
				ExpectError: regexp.MustCompile("between 1 and 525600"),
			},
		},
	})
}
