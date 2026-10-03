//go:build acc
// +build acc

package datasources_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// The server's search returns more than one page of candidates and the group
// named exactly is on the second: the groups are listed in creation order and
// the exact name is created last, after enough look-alikes to fill the first
// page (the provider asks for 100 a page). A lookup that read only the first
// page, or lost its search term on the second, would not find it.
func TestAccGroupDataSource_ByNameBeyondTheFirstSearchPage(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-srch")
	for i := 0; i < 101; i++ {
		decoy := fmt.Sprintf("%s-%03d", name, i)
		b2AccCreate(t, "user-groups", map[string]string{"name": decoy, "friendlyName": decoy})
	}
	// Creation time has a resolution of a second: wait, so that this group is
	// strictly the newest and therefore last in the search's order.
	time.Sleep(1100 * time.Millisecond)
	exactID := b2AccCreate(t, "user-groups", map[string]string{"name": name, "friendlyName": "Exact"})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "pocketid_group" "exact" {
  name = %q
}
`, name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.pocketid_group.exact", "id", exactID),
				resource.TestCheckResourceAttr("data.pocketid_group.exact", "name", name),
				resource.TestCheckResourceAttr("data.pocketid_group.exact", "friendly_name", "Exact"),
			),
		}},
	})
}

// "_" and "%" in a group's name are wildcards in the server's search, so it
// returns look-alikes with the group (and letters in the other case). The
// lookup returns exactly the group named, and not a look-alike.
func TestAccGroupDataSource_ByNameWithWildcardCharacters(t *testing.T) {
	testAccPreCheck(t)
	base := acctest.RandomWithPrefix("tf-acc-wc")
	create := func(name string) string {
		return b2AccCreate(t, "user-groups", map[string]string{"name": name, "friendlyName": "wildcards"})
	}
	// Look-alikes first, then the groups looked up.
	create(base + "-1")
	create(base + "x1")
	create(strings.ToUpper(base) + "_1")
	create(base + "-percent-1")
	underscoreID := create(base + "_1")
	percentID := create(base + "%1")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "pocketid_group" "underscore" {
  name = %q
}

data "pocketid_group" "percent" {
  name = %q
}
`, base+"_1", base+"%1"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.pocketid_group.underscore", "id", underscoreID),
				resource.TestCheckResourceAttr("data.pocketid_group.underscore", "name", base+"_1"),
				resource.TestCheckResourceAttr("data.pocketid_group.percent", "id", percentID),
				resource.TestCheckResourceAttr("data.pocketid_group.percent", "name", base+"%1"),
			),
		}},
	})
}
