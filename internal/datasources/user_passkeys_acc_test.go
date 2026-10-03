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

// A user without passkeys reads as an empty list, and a user that does not
// exist is reported as such. (Registering a passkey takes a WebAuthn
// authenticator, so the fixture cannot create one: what a passkey looks like
// is covered by the unit tests, from Pocket ID's own response shape.)
func TestAccUserPasskeysDataSource_NoPasskeysAndMissingUser(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-pk")
	userID := b2AccCreate(t, "users", map[string]any{"username": name, "email": name + "@example.com", "firstName": "P", "lastName": "K", "displayName": "P K"})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
data "pocketid_user_passkeys" "none" {
  user_id = %q
}
`, userID),
				Check: resource.TestCheckResourceAttr("data.pocketid_user_passkeys.none", "passkeys.#", "0"),
			},
			{
				Config: `
data "pocketid_user_passkeys" "missing" {
  user_id = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
}
`,
				ExpectError: regexp.MustCompile(`No user found`),
			},
		},
	})
}
