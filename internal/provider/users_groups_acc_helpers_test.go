//go:build acc
// +build acc

package provider_test

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccDeleteOutsideTerraform deletes the object behind resourceName
// through the API, as an administrator would in the web UI. apiPath is the
// collection, such as "/api/users".
func testAccDeleteOutsideTerraform(resourceName, apiPath string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		status, err := testAccAPI("DELETE", apiPath+"/"+rs.Primary.ID, nil, nil)
		if err != nil {
			return err
		}
		if status != 204 {
			return fmt.Errorf("deleting %s outside Terraform returned HTTP %d", resourceName, status)
		}
		return nil
	}
}

// testAccCheckExistsOnServer requires the object behind resourceName to exist.
func testAccCheckExistsOnServer(resourceName, apiPath string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		status, err := testAccAPI("GET", apiPath+"/"+rs.Primary.ID, nil, nil)
		if err != nil {
			return err
		}
		if status != 200 {
			return fmt.Errorf("%s is not on the server: HTTP %d", resourceName, status)
		}
		return nil
	}
}
