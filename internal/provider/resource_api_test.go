//go:build acc
// +build acc

package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// testAccResourceAPIIDs captures an API's ID and its permission IDs from
// state, so later steps can compare them.
type testAccResourceAPIIDs struct {
	api         string
	permissions map[string]string
}

func (ids *testAccResourceAPIIDs) capture(name string, keys ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s not in state", name)
		}
		ids.api = rs.Primary.ID
		ids.permissions = map[string]string{}
		for _, key := range keys {
			ids.permissions[key] = rs.Primary.Attributes["permissions."+key+".id"]
			if ids.permissions[key] == "" {
				return fmt.Errorf("permission %s has no ID in state", key)
			}
		}
		return nil
	}
}

// same checks that the API and the given permissions still have the IDs
// captured earlier.
func (ids *testAccResourceAPIIDs) same(name string, keys ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := s.RootModule().Resources[name]
		if rs.Primary.ID != ids.api {
			return fmt.Errorf("API ID changed")
		}
		for _, key := range keys {
			if got := rs.Primary.Attributes["permissions."+key+".id"]; got != ids.permissions[key] {
				return fmt.Errorf("permission %s changed ID", key)
			}
		}
		return nil
	}
}

func testAccResourceAPIResource(suffix string) string {
	return "https://tf-acc-" + suffix + ".example/api"
}

// testAccResourceAPIClient creates a confidential OIDC client directly, so
// a grant can be written without the grant resource.
func testAccResourceAPIClient(t *testing.T, name string) string {
	t.Helper()
	c, err := testClient()
	require.NoError(t, err)
	created, err := c.CreateClient(context.Background(), &client.OIDCClientCreateRequest{
		Name: name, CallbackURLs: []string{"https://example.com/callback"}, IsPublic: false,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), created.ID) })
	return created.ID
}

func TestAccResourceAPI_basic(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc-api")
	uri := testAccResourceAPIResource(acctest.RandString(8))
	name := "pocketid_api.test"
	var ids testAccResourceAPIIDs

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + fmt.Sprintf(`
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
    write = {
      name = "Write"
    }
  }
}
`, rName, uri),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "name", rName),
					resource.TestCheckResourceAttr(name, "resource", uri),
					resource.TestCheckResourceAttr(name, "allow_cimd_clients", "true"),
					resource.TestCheckResourceAttr(name, "permissions.%", "2"),
					resource.TestCheckResourceAttr(name, "permissions.read.description", "Read items"),
					resource.TestCheckResourceAttr(name, "permissions.read.allowed_for_cimd_clients", "true"),
					resource.TestCheckResourceAttr(name, "permissions.write.allowed_for_cimd_clients", "false"),
					resource.TestCheckNoResourceAttr(name, "permissions.write.description"),
					resource.TestCheckResourceAttrSet(name, "created_at"),
					ids.capture(name, "read", "write"),
					testAccResourceAPIServerCIMD(&ids, true, []string{"read"}),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Renaming the API, editing a permission and switching CIMD access
			// off happen in place and keep every permission ID.
			{
				Config: testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = "%[1]s-renamed"
  resource = %[2]q
  permissions = {
    read = {
      name        = "Read everything"
      description = "Read all items"
    }
    write = {
      name        = "Write"
      description = "Write items"
    }
  }
}
`, rName, uri),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(name, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "name", rName+"-renamed"),
					resource.TestCheckResourceAttr(name, "allow_cimd_clients", "false"),
					resource.TestCheckResourceAttr(name, "permissions.read.allowed_for_cimd_clients", "false"),
					resource.TestCheckResourceAttr(name, "permissions.write.description", "Write items"),
					ids.same(name, "read", "write"),
					testAccResourceAPIServerCIMD(&ids, false, nil),
				),
			},
			// Removing every permission empties the server's list.
			{
				Config: testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = "%[1]s-renamed"
  resource = %[2]q
}
`, rName, uri),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "permissions.%", "0"),
					ids.same(name),
				),
			},
		},
	})
}

// testAccResourceAPIServerCIMD checks the CIMD access the server holds.
func testAccResourceAPIServerCIMD(ids *testAccResourceAPIIDs, enabled bool, keys []string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, err := testClient()
		if err != nil {
			return err
		}
		api, err := c.GetAPI(context.Background(), ids.api)
		if err != nil {
			return err
		}
		if api.AllowCIMDClients != enabled {
			return fmt.Errorf("server allowCimdClients is %t", api.AllowCIMDClients)
		}
		var flagged []string
		for _, p := range api.Permissions {
			if p.AllowedForCIMDClients {
				flagged = append(flagged, p.Key)
			}
		}
		if strings.Join(flagged, ",") != strings.Join(keys, ",") {
			return fmt.Errorf("server CIMD permissions are %v, want %v", flagged, keys)
		}
		return nil
	}
}

// Editing one permission keeps every client's grant of every permission,
// because Pocket ID keeps a permission's ID while its key stays. Removing a
// permission deletes its grants, and the access its last grant implied.
func TestAccResourceAPI_permissionEditsKeepGrants(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-api")
	uri := testAccResourceAPIResource(acctest.RandString(8))
	name := "pocketid_api.test"
	clientID := testAccResourceAPIClient(t, rName)
	var ids testAccResourceAPIIDs
	config := func(writeDescription string, withWrite bool) string {
		write := ""
		if withWrite {
			write = fmt.Sprintf(`
    write = {
      name        = "Write"
      description = %q
    }`, writeDescription)
		}
		return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = %[1]q
  resource = %[2]q
  permissions = {
    read = {
      name = "Read"
    }%[3]s
  }
}
`, rName, uri, write)
	}
	grant := func(wantClientIDs []string, wantClientAccess bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			c, err := testClient()
			if err != nil {
				return err
			}
			got, err := c.FindClientAPIGrant(context.Background(), clientID, ids.api)
			if err != nil {
				return err
			}
			if got == nil {
				return fmt.Errorf("the client has no grant on the API")
			}
			if !got.UserDelegatedAccess || strings.Join(got.UserDelegatedPermissionIDs, ",") != ids.permissions["read"] {
				return fmt.Errorf("user-delegated grant changed")
			}
			if got.ClientAccess != wantClientAccess || strings.Join(got.ClientPermissionIDs, ",") != strings.Join(wantClientIDs, ",") {
				return fmt.Errorf("client grant is access=%t with %d permissions", got.ClientAccess, len(got.ClientPermissionIDs))
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("first", true),
				Check:  ids.capture(name, "read", "write"),
			},
			{
				PreConfig: func() {
					c, err := testClient()
					require.NoError(t, err)
					_, err = c.SetAPIClientAccess(context.Background(), ids.api, clientID, client.APIClientGrant{
						UserDelegatedPermissionIDs: []string{ids.permissions["read"]},
						ClientPermissionIDs:        []string{ids.permissions["write"]},
					})
					require.NoError(t, err)
				},
				Config: config("second", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "permissions.write.description", "second"),
					ids.same(name, "read", "write"),
					func(s *terraform.State) error { return grant([]string{ids.permissions["write"]}, true)(s) },
				),
			},
			{
				Config: config("", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					ids.same(name, "read"),
					grant(nil, false),
				),
			},
		},
	})
}

// Deleting an API removes every client's grant on it, including access
// granted without any permission.
func TestAccResourceAPI_deleteRemovesGrants(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-api")
	uri := testAccResourceAPIResource(acctest.RandString(8))
	clientID := testAccResourceAPIClient(t, rName)
	var ids testAccResourceAPIIDs

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api" "test" {
  name        = %[1]q
  resource    = %[2]q
  permissions = { read = { name = "Read" } }
}
`, rName, uri),
				Check: ids.capture("pocketid_api.test", "read"),
			},
			{
				PreConfig: func() {
					c, err := testClient()
					require.NoError(t, err)
					_, err = c.SetAPIClientAccess(context.Background(), ids.api, clientID, client.APIClientGrant{
						UserDelegatedPermissionIDs: []string{ids.permissions["read"]},
						ClientAccess:               true,
					})
					require.NoError(t, err)
					grants, err := c.ListClientAPIGrants(context.Background(), clientID)
					require.NoError(t, err)
					require.Len(t, grants, 1)
				},
				Config: testAccProviderConfig(),
				Check: func(*terraform.State) error {
					c, err := testClient()
					if err != nil {
						return err
					}
					grants, err := c.ListClientAPIGrants(context.Background(), clientID)
					if err != nil {
						return err
					}
					if len(grants) != 0 {
						return fmt.Errorf("the client still reaches %d API(s) after the API was deleted", len(grants))
					}
					return nil
				},
			},
		},
	})
}

// An API deleted outside Terraform is dropped from state on refresh and
// created again; changing the resource identifier replaces the API.
func TestAccResourceAPI_disappearsAndReplace(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc-api")
	suffix := acctest.RandString(8)
	name := "pocketid_api.test"
	var first, second testAccResourceAPIIDs
	config := func(uri string) string {
		return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = %q
  resource = %q
}
`, rName, uri)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(testAccResourceAPIResource(suffix)),
				Check:  first.capture(name),
			},
			{
				PreConfig: func() {
					c, err := testClient()
					require.NoError(t, err)
					require.NoError(t, c.DeleteAPI(context.Background(), first.api))
				},
				Config: config(testAccResourceAPIResource(suffix)),
				Check: resource.ComposeAggregateTestCheckFunc(
					second.capture(name),
					func(*terraform.State) error {
						if second.api == first.api {
							return fmt.Errorf("the API was not created again")
						}
						return nil
					},
				),
			},
			{
				Config: config(testAccResourceAPIResource(suffix) + "/v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(name, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "resource", testAccResourceAPIResource(suffix)+"/v2"),
					func(s *terraform.State) error {
						if s.RootModule().Resources[name].Primary.ID == second.api {
							return fmt.Errorf("the API was not replaced")
						}
						return nil
					},
				),
			},
		},
	})
}

// An identifier another API holds is refused before anything is written.
func TestAccResourceAPI_existingResourceRefused(t *testing.T) {
	testAccPreCheck(t)
	uri := testAccResourceAPIResource(acctest.RandString(8))
	c, err := testClient()
	require.NoError(t, err)
	existing, err := c.CreateAPI(context.Background(), &client.APICreateRequest{Name: "existing", Resource: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteAPI(context.Background(), existing.ID) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = "duplicate"
  resource = %q
}
`, uri),
				ExpectError: regexp.MustCompile(`already exists`),
			},
		},
	})
}

// Values Pocket ID would refuse or change are refused at plan time.
func TestAccResourceAPI_planTimeValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		resource, key, want string
	}{
		"trailing slash": {"https://tf-acc.example/api/", "read", `must not end with a slash`},
		"relative":       {"api.example.com", "read", `absolute URI`},
		"reserved key":   {"https://tf-acc.example/api", "openid", `reserved by Pocket ID`},
		"space in key":   {"https://tf-acc.example/api", "read all", `valid in an OAuth scope`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api" "test" {
  name        = "invalid"
  resource    = %q
  permissions = { %q = { name = "P" } }
}
`, tc.resource, tc.key),
						PlanOnly:    true,
						ExpectError: regexp.MustCompile(tc.want),
					},
				},
			})
		})
	}
}
