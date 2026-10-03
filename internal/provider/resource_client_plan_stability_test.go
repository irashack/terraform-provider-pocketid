//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func testAccGroupSetConfig(name, groups string) string {
	attribute := ""
	if groups != "omit" {
		attribute = "  allowed_user_groups = " + groups + "\n"
	}
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_group" "a" {
  name          = "%[1]s-a"
  friendly_name = "%[1]s-a"
}

resource "pocketid_group" "b" {
  name          = "%[1]s-b"
  friendly_name = "%[1]s-b"
}

resource "pocketid_group" "c" {
  name          = "%[1]s-c"
  friendly_name = "%[1]s-c"
}

resource "pocketid_client" "test" {
  name          = %[1]q
  callback_urls = ["https://example.invalid/callback"]
%[2]s}
`, name, attribute)
}

// testAccCheckServerGroups compares the client's allowed groups on the server
// with the IDs of the named group resources.
func testAccCheckServerGroups(names ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		var want []string
		for _, name := range names {
			want = append(want, s.RootModule().Resources["pocketid_group."+name].Primary.ID)
		}
		var got struct {
			AllowedUserGroups []struct {
				ID string `json:"id"`
			} `json:"allowedUserGroups"`
		}
		id := s.RootModule().Resources["pocketid_client.test"].Primary.ID
		if status, err := testAccAPI("GET", "/api/oidc/clients/"+id, nil, &got); err != nil || status != http.StatusOK {
			return fmt.Errorf("client read failed: HTTP %d %v", status, err)
		}
		var ids []string
		for _, g := range got.AllowedUserGroups {
			ids = append(ids, g.ID)
		}
		sort.Strings(ids)
		sort.Strings(want)
		if strings.Join(ids, ",") != strings.Join(want, ",") {
			return fmt.Errorf("server's allowed groups are %v, want %v", ids, want)
		}
		return nil
	}
}

// allowed_user_groups is a set: order never shows a change, and omitted and
// [] are both stable.
func TestAccResourceClient_allowedUserGroupsSet(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-gset")
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupSetConfig(name, "[pocketid_group.b.id, pocketid_group.a.id]"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "allowed_user_groups.#", "2"),
					resource.TestCheckTypeSetElemAttrPair("pocketid_client.test", "allowed_user_groups.*", "pocketid_group.a", "id"),
					resource.TestCheckTypeSetElemAttrPair("pocketid_client.test", "allowed_user_groups.*", "pocketid_group.b", "id"),
					testAccCheckServerGroups("a", "b"),
				),
			},
			{Config: testAccGroupSetConfig(name, "[pocketid_group.a.id, pocketid_group.b.id]"), ConfigPlanChecks: empty},
			{Config: testAccGroupSetConfig(name, "sort([pocketid_group.b.id, pocketid_group.a.id])"), ConfigPlanChecks: empty},
			{ResourceName: "pocketid_client.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"client_secret"}},
			{Config: testAccGroupSetConfig(name, "[pocketid_group.c.id, pocketid_group.a.id]"), Check: testAccCheckServerGroups("a", "c")},
			{Config: testAccGroupSetConfig(name, "[]"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("pocketid_client.test", "allowed_user_groups.#", "0"),
				testAccCheckServerGroups(),
			)},
			{Config: testAccGroupSetConfig(name, "[]"), ConfigPlanChecks: empty},
			{Config: testAccGroupSetConfig(name, "omit"), Check: resource.TestCheckNoResourceAttr("pocketid_client.test", "allowed_user_groups.#")},
			{Config: testAccGroupSetConfig(name, "omit"), ConfigPlanChecks: empty},
		},
	})
}

func testAccLaunchURLConfig(name, extra string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
%s}
`, name, extra)
}

// An omitted launch_url is left as the server has it: an unrelated update
// neither clears it nor shows it as "known after apply"; "" removes it. An
// explicit empty logout_callback_urls is stable, and has_logo stays known.
func TestAccResourceClient_launchURLUnmanaged(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-launch")
	const outside = "https://outside.example.invalid/"
	var id string
	serverLaunchURL := func(want string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			id = s.RootModule().Resources["pocketid_client.test"].Primary.ID
			var got struct {
				LaunchURL *string `json:"launchURL"`
			}
			if status, err := testAccAPI("GET", "/api/oidc/clients/"+id, nil, &got); err != nil || status != http.StatusOK {
				return fmt.Errorf("client read failed: HTTP %d %v", status, err)
			}
			value := ""
			if got.LaunchURL != nil {
				value = *got.LaunchURL
			}
			if value != want {
				return fmt.Errorf("server launch URL is %q, want %q", value, want)
			}
			return nil
		}
	}
	known := func(attribute string, value knownvalue.Check) plancheck.PlanCheck {
		return plancheck.ExpectKnownValue("pocketid_client.test", tfjsonpath.New(attribute), value)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: testAccLaunchURLConfig(name, "  logout_callback_urls = []\n"), Check: serverLaunchURL("")},
			{
				PreConfig: func() {
					if err := testAccModifyClient(id, func(req *client.OIDCClientCreateRequest) {
						launch := outside
						req.LaunchURL = &launch
					}); err != nil {
						t.Fatalf("setting a launch URL outside Terraform: %v", err)
					}
				},
				Config: testAccLaunchURLConfig(name+"-renamed", "  logout_callback_urls = []\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("pocketid_client.test", plancheck.ResourceActionUpdate),
					known("launch_url", knownvalue.StringExact(outside)),
					known("has_logo", knownvalue.Bool(false)),
					known("client_secret_id", knownvalue.NotNull()),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					serverLaunchURL(outside),
					resource.TestCheckResourceAttr("pocketid_client.test", "launch_url", outside),
					resource.TestCheckResourceAttr("pocketid_client.test", "logout_callback_urls.#", "0"),
				),
			},
			{
				Config: testAccLaunchURLConfig(name+"-renamed", "  launch_url = \"\"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					serverLaunchURL(""),
					resource.TestCheckResourceAttr("pocketid_client.test", "launch_url", ""),
					resource.TestCheckNoResourceAttr("pocketid_client.test", "logout_callback_urls.#"),
				),
			},
			{
				Config: testAccLaunchURLConfig(name+"-renamed", "  launch_url = \"https://configured.example.invalid/\"\n"),
				Check:  serverLaunchURL("https://configured.example.invalid/"),
			},
			// Omitting it again leaves it as it is.
			{
				Config: testAccLaunchURLConfig(name, ""),
				Check:  serverLaunchURL("https://configured.example.invalid/"),
			},
		},
	})
}
