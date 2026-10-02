//go:build acc
// +build acc

package datasources_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccGroupDataSource_LookupByID(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc-test")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// First create a group
			{
				Config: testAccGroupDataSourceConfig_CreateGroup(rName, "Test Group"),
			},
			// Then look it up by ID
			{
				Config: testAccGroupDataSourceConfig_LookupByID(rName, "Test Group"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.pocketid_group.test", "name", rName),
					resource.TestCheckResourceAttr("data.pocketid_group.test", "friendly_name", "Test Group"),
					resource.TestCheckResourceAttrSet("data.pocketid_group.test", "id"),
					resource.TestCheckResourceAttrSet("data.pocketid_group.test", "created_at"),
					// Group is not LDAP-managed, so ldap_id is null.
					resource.TestCheckNoResourceAttr("data.pocketid_group.test", "ldap_id"),
				),
			},
		},
	})
}

func TestAccGroupDataSource_LookupByName(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc-test")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// First create a group
			{
				Config: testAccGroupDataSourceConfig_CreateGroup(rName, "Test Group By Name"),
			},
			// Then look it up by name
			{
				Config: testAccGroupDataSourceConfig_LookupByName(rName, "Test Group By Name"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.pocketid_group.test", "name", rName),
					resource.TestCheckResourceAttr("data.pocketid_group.test", "friendly_name", "Test Group By Name"),
					resource.TestCheckResourceAttrSet("data.pocketid_group.test", "id"),
				),
			},
		},
	})
}

func TestAccGroupDataSource_ErrorWhenNoIdentifier(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccGroupDataSourceConfig_NoIdentifier(),
				ExpectError: regexp.MustCompile(`Either 'id' or 'name' must be provided`),
			},
		},
	})
}

func TestAccGroupDataSource_ErrorWhenNotFound(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccGroupDataSourceConfig_NotFound(),
				ExpectError: regexp.MustCompile(`No group found`),
			},
		},
	})
}

func testAccGroupDataSourceConfig_CreateGroup(name, friendlyName string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = %[1]q
  friendly_name = %[2]q
}
`, name, friendlyName)
}

func testAccGroupDataSourceConfig_LookupByID(name, friendlyName string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = %[1]q
  friendly_name = %[2]q
}

data "pocketid_group" "test" {
  id = pocketid_group.test.id
}
`, name, friendlyName)
}

func testAccGroupDataSourceConfig_LookupByName(name, friendlyName string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = %[1]q
  friendly_name = %[2]q
}

data "pocketid_group" "test" {
  name = pocketid_group.test.name
}
`, name, friendlyName)
}

func testAccGroupDataSourceConfig_NoIdentifier() string {
	return `
data "pocketid_group" "test" {
  # Neither ID nor name provided
}
`
}

func testAccGroupDataSourceConfig_NotFound() string {
	return `
data "pocketid_group" "test" {
  name = "non_existent_group_12345"
}
`
}
