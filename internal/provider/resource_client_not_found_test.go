//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// A client deleted outside Terraform is removed from state on refresh and
// created again by the next apply, instead of failing every plan.
func TestAccResourceClient_deletedOutsideTerraform(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-gone")
	config := testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %q
  client_id     = %q
  callback_urls = ["https://example.invalid/callback"]
}
`, name, name)
	deleteOutside := func() {
		status, err := testAccAPI("DELETE", "/api/oidc/clients/"+name, nil, nil)
		if err != nil || status != http.StatusNoContent {
			t.Fatalf("deleting the client outside Terraform failed: HTTP %d %v", status, err)
		}
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:          deleteOutside,
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pocketid_client.test", plancheck.ResourceActionCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "id", name),
					func(*terraform.State) error {
						status, err := testAccAPI("GET", "/api/oidc/clients/"+name, nil, nil)
						if err != nil || status != http.StatusOK {
							return fmt.Errorf("the client was not created again: HTTP %d %v", status, err)
						}
						return nil
					},
				),
			},
		},
	})
}
