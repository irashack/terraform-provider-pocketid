//go:build acc
// +build acc

package datasources_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccUsersDataSource_displayNameAndEmailVerified(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc-test")
	email := fmt.Sprintf("%s@example.com", rName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUsersDataSourceConfig_list(rName, email),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.pocketid_users.test", "users.#"),
				),
			},
		},
	})
}

func testAccUsersDataSourceConfig_list(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username       = %[1]q
  email          = %[2]q
  first_name     = "Test"
  last_name      = "User"
  email_verified = true
}

data "pocketid_users" "test" {
  depends_on = [pocketid_user.test]
}
`, username, email)
}
