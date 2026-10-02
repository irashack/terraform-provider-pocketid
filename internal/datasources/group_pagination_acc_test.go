//go:build acc
// +build acc

package datasources_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// More groups than one page (Pocket ID answers 20 by default, and the provider
// asks for 100): the list and both lookups must see all of them. The last
// group created sits on the last page of any list.
func TestAccGroupDataSources_MoreThanOnePage(t *testing.T) {
	testAccPreCheck(t)
	prefix := acctest.RandomWithPrefix("tf-acc-pages")
	const total = 105
	ids := make([]string, total)
	for i := range ids {
		name := fmt.Sprintf("%s-%03d", prefix, i)
		ids[i] = b2AccCreate(t, "user-groups", map[string]string{"name": name, "friendlyName": name})
	}
	lastName := fmt.Sprintf("%s-%03d", prefix, total-1)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "pocketid_groups" "all" {}

data "pocketid_group" "by_id" {
  id = %q
}

data "pocketid_group" "by_name" {
  name = %q
}
`, ids[total-1], lastName),
			Check: resource.ComposeAggregateTestCheckFunc(
				testAccCheckGroupListHasAll("data.pocketid_groups.all", ids),
				resource.TestCheckResourceAttr("data.pocketid_group.by_id", "name", lastName),
				resource.TestCheckResourceAttr("data.pocketid_group.by_name", "id", ids[total-1]),
				resource.TestCheckResourceAttr("data.pocketid_group.by_name", "friendly_name", lastName),
			),
		}},
	})
}

// testAccCheckGroupListHasAll requires every ID in want to be among the
// "groups" of a pocketid_groups data source.
func testAccCheckGroupListHasAll(address string, want []string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("%s is not in the state", address)
		}
		have := map[string]bool{}
		var count int
		if _, err := fmt.Sscanf(rs.Primary.Attributes["groups.#"], "%d", &count); err != nil {
			return fmt.Errorf("%s has no group count", address)
		}
		for i := 0; i < count; i++ {
			have[rs.Primary.Attributes[fmt.Sprintf("groups.%d.id", i)]] = true
		}
		var missing int
		for _, id := range want {
			if !have[id] {
				missing++
			}
		}
		if missing > 0 {
			return fmt.Errorf("%s lists %d groups and is missing %d of the %d that exist", address, count, missing, len(want))
		}
		return nil
	}
}
