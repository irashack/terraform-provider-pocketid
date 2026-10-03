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
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccRestrictionConfig(name, attributes string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_group" "a" {
  name          = "%[1]s-a"
  friendly_name = "%[1]s-a"
}

resource "pocketid_client" "test" {
  name          = %[1]q
  callback_urls = ["https://example.invalid/callback"]
%[2]s}
`, name, attributes)
}

// testAccCheckServerRestricted checks the client's restriction on the server.
func testAccCheckServerRestricted(want bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		var got struct {
			Restricted bool `json:"isGroupRestricted"`
		}
		id := s.RootModule().Resources["pocketid_client.test"].Primary.ID
		if status, err := testAccAPI("GET", "/api/oidc/clients/"+id, nil, &got); err != nil || status != http.StatusOK {
			return fmt.Errorf("client read failed: HTTP %d %v", status, err)
		}
		if got.Restricted != want {
			return fmt.Errorf("server isGroupRestricted is %t, want %t", got.Restricted, want)
		}
		return nil
	}
}

// Removing a restricted client's groups leaves it restricted to nobody;
// only is_group_restricted = false opens it. Each state is stable.
func TestAccResourceClient_groupRestriction(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-restrict")
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	restriction := func(want bool) plancheck.PlanCheck {
		return plancheck.ExpectKnownValue("pocketid_client.test", tfjsonpath.New("is_group_restricted"), knownvalue.Bool(want))
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:           testAccRestrictionConfig(name, "  allowed_user_groups = [pocketid_group.a.id]\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{restriction(true)}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "is_group_restricted", "true"),
					testAccCheckServerRestricted(true),
					testAccCheckServerGroups("a"),
				),
			},
			{ResourceName: "pocketid_client.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"client_secret"}},
			// The groups go; the restriction stays: nobody may sign in.
			{
				Config:           testAccRestrictionConfig(name, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{restriction(true)}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "is_group_restricted", "true"),
					testAccCheckServerRestricted(true),
					testAccCheckServerGroups(),
				),
			},
			{Config: testAccRestrictionConfig(name, ""), ConfigPlanChecks: empty},
			{Config: testAccRestrictionConfig(name, "  is_group_restricted = true\n"), ConfigPlanChecks: empty},
			// Opening takes an explicit false.
			{
				Config: testAccRestrictionConfig(name, "  is_group_restricted = false\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "is_group_restricted", "false"),
					testAccCheckServerRestricted(false),
				),
			},
			{Config: testAccRestrictionConfig(name, ""), ConfigPlanChecks: empty},
			// Groups restrict it again.
			{
				Config: testAccRestrictionConfig(name, "  allowed_user_groups = [pocketid_group.a.id]\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckServerRestricted(true),
					testAccCheckServerGroups("a"),
				),
			},
		},
	})
}

// Pocket ID silently drops a group ID that names no group; the apply fails
// and names it instead of recording success.
func TestAccResourceClient_groupRestrictionUnknownGroup(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-nogroup")
	const missing = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: testAccRestrictionConfig(name, "  allowed_user_groups = [pocketid_group.a.id]\n")},
			{
				Config:      testAccRestrictionConfig(name, fmt.Sprintf("  allowed_user_groups = [pocketid_group.a.id, %q]\n", missing)),
				ExpectError: regexp.MustCompile(missing),
			},
			// On create, the new client is rolled back.
			{
				Config: testAccRestrictionConfig(name, "  allowed_user_groups = [pocketid_group.a.id]\n") + fmt.Sprintf(`
resource "pocketid_client" "other" {
  name                = "%s-other"
  callback_urls       = ["https://example.invalid/callback"]
  allowed_user_groups = [%q]
}
`, name, missing),
				ExpectError: regexp.MustCompile("rolled back"),
			},
		},
	})
}

func TestAccResourceClient_groupRestrictionConflict(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccRestrictionConfig("conflict", "  is_group_restricted = false\n  allowed_user_groups = [pocketid_group.a.id]\n"),
				ExpectError: regexp.MustCompile("Conflicting group restriction"),
			},
		},
	})
}
