//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func testAccClientIDConfig(name, clientID string) string {
	attribute := ""
	if clientID != "" {
		attribute = fmt.Sprintf("  client_id     = %q\n", clientID)
	}
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %q
%s  callback_urls = ["https://example.invalid/callback"]
}
`, name, attribute)
}

func testAccClientExists(id string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		status, err := testAccAPI("GET", "/api/oidc/clients/"+id, nil, nil)
		if err != nil {
			return err
		}
		if (status == http.StatusOK) != want {
			return fmt.Errorf("client %s: HTTP %d, want it to exist: %t", id, status, want)
		}
		return nil
	}
}

// client_id always equals the server's ID; omitting it never plans a change,
// and configuring a different value replaces the client.
func TestAccResourceClient_clientID(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-cid")
	first, second := name+"-a", name+"-b"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Generated: recorded as the server's ID.
			{
				Config: testAccClientIDConfig(name, ""),
				Check:  resource.TestCheckResourceAttrPair("pocketid_client.test", "client_id", "pocketid_client.test", "id"),
			},
			{ResourceName: "pocketid_client.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"client_secret", "client_secret_id"}},
			// Configuring the generated ID changes nothing.
			{
				Config: testAccClientIDConfig(name, "") + "\n",
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			// A fixed ID, then a different one: the client is replaced.
			{
				Config: testAccClientIDConfig(name, first),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "id", first),
					resource.TestCheckResourceAttr("pocketid_client.test", "client_id", first),
				),
			},
			{ResourceName: "pocketid_client.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"client_secret", "client_secret_id"}},
			// Omitting the fixed ID afterwards keeps the client.
			{
				Config: testAccClientIDConfig(name, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("pocketid_client.test", "client_id", first),
			},
			{
				Config: testAccClientIDConfig(name, second),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pocketid_client.test", plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "id", second),
					resource.TestCheckResourceAttr("pocketid_client.test", "client_id", second),
					testAccClientExists(second, true),
					testAccClientExists(first, false),
				),
			},
		},
	})
}

func TestAccResourceClient_clientIDValidation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccClientIDConfig("invalid-client-id", "not valid/"),
				ExpectError: regexp.MustCompile("Invalid client ID"),
			},
		},
	})
}

// A Client ID Metadata Document client (its ID is the document's URL) is
// refused at import with a diagnostic that says why.
func TestAccResourceClient_importCIMDRefused(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        testAccClientIDConfig("cimd-import", ""),
				ResourceName:  "pocketid_client.test",
				ImportState:   true,
				ImportStateId: "https://app.example.invalid/oauth/client-metadata.json",
				ExpectError:   regexp.MustCompile("Client ID Metadata Document"),
			},
		},
	})
}
