//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func testAccClientGenerateSecretConfig(name, generate string) string {
	attribute := ""
	if generate != "" {
		attribute = "  generate_secret = " + generate + "\n"
	}
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
%s}

resource "pocketid_client" "without" {
  name            = "%s-without"
  callback_urls   = ["https://example.invalid/callback"]
  generate_secret = false
}
`, name, attribute, name)
}

// testAccCheckHeldSecret checks the client's secrets on the server: with a
// secret in state, exactly one secret exists, it is the one client_secret_id
// names, and its prefix is the stored value's; without one, the client has no
// secret at all.
func testAccCheckHeldSecret(resourceName string, held bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := s.RootModule().Resources[resourceName]
		if rs == nil {
			return fmt.Errorf("missing %s", resourceName)
		}
		secrets, err := testAccClientSecrets(rs.Primary.ID)
		if err != nil {
			return err
		}
		value, id := rs.Primary.Attributes["client_secret"], rs.Primary.Attributes["client_secret_id"]
		if !held {
			if value != "" || id != "" {
				return fmt.Errorf("%s still records a secret", resourceName)
			}
			if len(secrets) != 0 {
				return fmt.Errorf("%s: expected no secret on the server, got %d", resourceName, len(secrets))
			}
			return nil
		}
		if len(secrets) != 1 {
			return fmt.Errorf("%s: expected exactly one secret on the server, got %d", resourceName, len(secrets))
		}
		if id == "" || secrets[0].ID != id {
			return fmt.Errorf("%s: client_secret_id does not name the server's secret %s", resourceName, secrets[0].ID)
		}
		if value == "" || !strings.HasPrefix(value, secrets[0].Prefix) || secrets[0].Prefix == "" {
			return fmt.Errorf("%s: the server's secret is not the one in state", resourceName)
		}
		return nil
	}
}

// generate_secret changes in place: false revokes the secret this resource
// generated, true generates a new one, and the client is never replaced. A
// client created with generate_secret = false holds no secret at all, not
// even the one Pocket ID 2.17 creates by itself.
func TestAccResourceClient_generateSecretInPlace(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-gensecret")
	var id, first string
	sameClient := func(s *terraform.State) error {
		rs := s.RootModule().Resources["pocketid_client.test"]
		if id == "" {
			id, first = rs.Primary.ID, rs.Primary.Attributes["client_secret_id"]
		}
		if rs.Primary.ID != id {
			return fmt.Errorf("the client was replaced")
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccClientGenerateSecretConfig(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "generate_secret", "true"),
					testAccCheckHeldSecret("pocketid_client.test", true),
					testAccCheckHeldSecret("pocketid_client.without", false),
					sameClient,
				),
			},
			{
				Config: testAccClientGenerateSecretConfig(name, "false"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "generate_secret", "false"),
					resource.TestCheckNoResourceAttr("pocketid_client.test", "client_secret"),
					resource.TestCheckNoResourceAttr("pocketid_client.test", "client_secret_id"),
					testAccCheckHeldSecret("pocketid_client.test", false),
					sameClient,
				),
			},
			{
				Config: testAccClientGenerateSecretConfig(name, "true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckHeldSecret("pocketid_client.test", true),
					sameClient,
					func(s *terraform.State) error {
						if s.RootModule().Resources["pocketid_client.test"].Primary.Attributes["client_secret_id"] == first {
							return fmt.Errorf("no new secret was generated")
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "pocketid_client.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"client_secret", "client_secret_id"},
			},
		},
	})
}

func testAccClientPublicConfig(name string, public bool) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
  is_public     = %t
}
`, name, public)
}

// is_public changes in place: becoming confidential generates the secret
// this resource holds, becoming public revokes it.
func TestAccResourceClient_publicFlip(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-flip")
	var id string
	sameClient := func(s *terraform.State) error {
		rs := s.RootModule().Resources["pocketid_client.test"]
		if id == "" {
			id = rs.Primary.ID
		}
		if rs.Primary.ID != id {
			return fmt.Errorf("the client was replaced")
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: testAccClientPublicConfig(name, true), Check: resource.ComposeAggregateTestCheckFunc(testAccCheckHeldSecret("pocketid_client.test", false), sameClient)},
			{Config: testAccClientPublicConfig(name, false), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("pocketid_client.test", "is_public", "false"),
				testAccCheckHeldSecret("pocketid_client.test", true), sameClient,
			)},
			{Config: testAccClientPublicConfig(name, true), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("pocketid_client.test", "is_public", "true"),
				testAccCheckHeldSecret("pocketid_client.test", false), sameClient,
			)},
		},
	})
}
