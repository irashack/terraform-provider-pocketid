//go:build acc
// +build acc

package datasources_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func testAccAPIDataSourceClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.NewClient(os.Getenv("POCKETID_BASE_URL"), os.Getenv("POCKETID_API_TOKEN"), false, 30)
	require.NoError(t, err)
	return c
}

func TestAccAPIDataSources_lookups(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc-apids")
	uri := "https://" + rName + ".example/api"
	config := fmt.Sprintf(`
resource "pocketid_api" "test" {
  name               = %[1]q
  resource           = %[2]q
  allow_cimd_clients = true
  permissions = {
    read = {
      name                     = "Read"
      description              = "Read items"
      allowed_for_cimd_clients = true
    }
    write = { name = "Write" }
  }
}

data "pocketid_api" "by_id" {
  id = pocketid_api.test.id
}

data "pocketid_api" "by_resource" {
  resource = pocketid_api.test.resource
}

data "pocketid_apis" "all" {
  depends_on = [pocketid_api.test]
}
`, rName, uri)

	pairs := func(data string) resource.TestCheckFunc {
		var checks []resource.TestCheckFunc
		for _, attribute := range []string{"id", "name", "resource", "created_at", "allow_cimd_clients", "permissions.%",
			"permissions.read.id", "permissions.read.description", "permissions.read.allowed_for_cimd_clients", "permissions.write.id"} {
			checks = append(checks, resource.TestCheckResourceAttrPair(data, attribute, "pocketid_api.test", attribute))
		}
		checks = append(checks, resource.TestCheckNoResourceAttr(data, "permissions.write.description"))
		return resource.ComposeAggregateTestCheckFunc(checks...)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					pairs("data.pocketid_api.by_id"),
					pairs("data.pocketid_api.by_resource"),
					func(s *terraform.State) error {
						id := s.RootModule().Resources["pocketid_api.test"].Primary.ID
						list := s.RootModule().Resources["data.pocketid_apis.all"].Primary.Attributes
						count, _ := strconv.Atoi(list["apis.#"])
						for i := 0; i < count; i++ {
							if list[fmt.Sprintf("apis.%d.id", i)] == id {
								if list[fmt.Sprintf("apis.%d.permissions.read.name", i)] != "Read" {
									return fmt.Errorf("pocketid_apis lists the API without its permissions")
								}
								return nil
							}
						}
						return fmt.Errorf("pocketid_apis does not list the API")
					},
				),
			},
			{
				Config:      config + `data "pocketid_api" "missing" { resource = "https://missing.example/api" }`,
				ExpectError: regexp.MustCompile(`API\s+not\s+found`),
			},
		},
	})
}

func TestAccAPIDataSource_exactlyOneLookup(t *testing.T) {
	for name, body := range map[string]string{
		"neither": ``,
		"both":    `id = "00000000-0000-4000-8000-000000000001"` + "\n" + `resource = "https://x.example/api"`,
	} {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      "data \"pocketid_api\" \"test\" {\n" + body + "\n}\n",
						ExpectError: regexp.MustCompile(`Exactly\s+one\s+of\s+these\s+attributes\s+must\s+be\s+configured`),
					},
				},
			})
		})
	}
}

// pocketid_apis reads every page: with more APIs than fit on one page (the
// server's maximum is 100), every one of them is listed.
func TestAccAPIsDataSource_complete(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c := testAccAPIDataSourceClient(t)
	prefix := acctest.RandomWithPrefix("tf-acc-apis")
	created := map[string]bool{}
	for i := 0; i < 101; i++ {
		api, err := c.CreateAPI(ctx, &client.APICreateRequest{Name: fmt.Sprintf("bulk %d", i), Resource: fmt.Sprintf("urn:%s:%d", prefix, i)})
		require.NoError(t, err)
		created[api.ID] = true
		t.Cleanup(func() {
			_ = c.DeleteAPI(context.Background(), api.ID)
			time.Sleep(10 * time.Millisecond)
		})
		time.Sleep(10 * time.Millisecond) // stay well under the server's request rate limit
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "pocketid_apis" "all" {}`,
				Check: func(s *terraform.State) error {
					list := s.RootModule().Resources["data.pocketid_apis.all"].Primary.Attributes
					count, _ := strconv.Atoi(list["apis.#"])
					seen := 0
					for i := 0; i < count; i++ {
						if created[list[fmt.Sprintf("apis.%d.id", i)]] {
							seen++
						}
					}
					if seen != len(created) {
						return fmt.Errorf("pocketid_apis listed %d of the %d APIs created", seen, len(created))
					}
					return nil
				},
			},
		},
	})
}
