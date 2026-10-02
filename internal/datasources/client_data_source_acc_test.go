//go:build acc
// +build acc

package datasources_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccCheckListedClient checks one attribute of the client with the given
// resource's ID inside the pocketid_clients list; want "" means absent.
func testAccCheckListedClient(resourceName, attribute, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		managed := s.RootModule().Resources[resourceName]
		listed := s.RootModule().Resources["data.pocketid_clients.all"]
		if managed == nil || listed == nil {
			return fmt.Errorf("missing state for %s or the client list", resourceName)
		}
		for key, value := range listed.Primary.Attributes {
			if !strings.HasPrefix(key, "clients.") || !strings.HasSuffix(key, ".id") || value != managed.Primary.ID {
				continue
			}
			prefix := strings.TrimSuffix(key, "id")
			got, ok := listed.Primary.Attributes[prefix+attribute]
			if want == "" && (!ok || got == "") {
				return nil
			}
			if got != want {
				return fmt.Errorf("listed %s is %q, want %q", attribute, got, want)
			}
			return nil
		}
		return fmt.Errorf("client %s is not in the list", managed.Primary.ID)
	}
}

func TestAccClientDataSources_backchannelLogoutURL(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-backchannel")
	const url = "https://rp.example.invalid/backchannel-logout"
	attribute, want := fmt.Sprintf("  backchannel_logout_url = %q\n", url), url
	if !testAccServerAtLeast(t, "2.17.0") {
		// An older server has no such setting; both data sources report none.
		attribute, want = "", ""
	}
	config := fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
%s}

data "pocketid_client" "test" {
  id = pocketid_client.test.id
}

data "pocketid_clients" "all" {
  depends_on = [pocketid_client.test]
}
`, name, attribute)

	check := resource.TestCheckNoResourceAttr("data.pocketid_client.test", "backchannel_logout_url")
	if want != "" {
		check = resource.TestCheckResourceAttr("data.pocketid_client.test", "backchannel_logout_url", want)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					check,
					testAccCheckListedClient("pocketid_client.test", "backchannel_logout_url", want),
				),
			},
		},
	})
}
