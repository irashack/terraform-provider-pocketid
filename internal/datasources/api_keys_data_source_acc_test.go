//go:build acc
// +build acc

package datasources_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// The fixture's own key (named provider-fixture, valid for a day, never given
// a description) is the only key its owner has. The data source lists it with
// its times and no key value, and the documented check expression tells a
// key that lasts the margin from one that does not.
func TestAccAPIKeysDataSource_listsTheProvidersOwnKey(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			// The outputs use timestamp(), which differs on every plan.
			ExpectNonEmptyPlan: true,
			Config: `
provider "pocketid" {}

data "pocketid_api_keys" "mine" {}

output "valid_for_12h" {
  value = ` + `length([
    for key in data.pocketid_api_keys.mine.keys : key
    if key.name == "provider-fixture" && timecmp(key.expires_at, timeadd(timestamp(), "12h")) > 0
  ]) > 0` + `
}

output "valid_for_48h" {
  value = ` + `length([
    for key in data.pocketid_api_keys.mine.keys : key
    if key.name == "provider-fixture" && timecmp(key.expires_at, timeadd(timestamp(), "48h")) > 0
  ]) > 0` + `
}

# A failing check only warns; the apply still succeeds.
check "management_key_expiry" {
  assert {
    condition     = length([for key in data.pocketid_api_keys.mine.keys : key if key.name == "provider-fixture" && timecmp(key.expires_at, timeadd(timestamp(), "48h")) > 0]) > 0
    error_message = "The management key expires within 48 hours."
  }
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.pocketid_api_keys.mine", "keys.#", "1"),
				resource.TestCheckResourceAttr("data.pocketid_api_keys.mine", "keys.0.name", "provider-fixture"),
				resource.TestCheckResourceAttrSet("data.pocketid_api_keys.mine", "keys.0.id"),
				resource.TestCheckResourceAttrSet("data.pocketid_api_keys.mine", "keys.0.expires_at"),
				resource.TestCheckResourceAttrSet("data.pocketid_api_keys.mine", "keys.0.created_at"),
				// The request that read the list used the key.
				resource.TestCheckResourceAttrSet("data.pocketid_api_keys.mine", "keys.0.last_used_at"),
				resource.TestCheckNoResourceAttr("data.pocketid_api_keys.mine", "keys.0.description"),
				resource.TestCheckNoResourceAttr("data.pocketid_api_keys.mine", "keys.0.key"),
				resource.TestCheckOutput("valid_for_12h", "true"),
				resource.TestCheckOutput("valid_for_48h", "false"),
			),
		}},
	})
}
