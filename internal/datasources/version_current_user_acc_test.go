//go:build acc
// +build acc

package datasources_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// The version the data source reports is the fixture's, and the API key can
// read /users/me on every supported version: it names the key's owner, an
// administrator.
func TestAccVersionAndCurrentUserDataSources(t *testing.T) {
	testAccPreCheck(t)
	var me struct {
		ID      string `json:"id"`
		IsAdmin bool   `json:"isAdmin"`
	}
	if status := b2AccAPI(t, "GET", "/api/users/me", nil, &me); status != 200 || me.ID == "" {
		t.Fatalf("GET /api/users/me with the API key answered %d", status)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "pocketid_version" "server" {}

data "pocketid_current_user" "me" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.pocketid_version.server", "version", os.Getenv("POCKETID_TEST_VERSION")),
				resource.TestCheckResourceAttr("data.pocketid_current_user.me", "id", me.ID),
				resource.TestCheckResourceAttr("data.pocketid_current_user.me", "is_admin", fmt.Sprint(me.IsAdmin)),
				resource.TestCheckResourceAttrSet("data.pocketid_current_user.me", "username"),
			),
		}},
	})
}
