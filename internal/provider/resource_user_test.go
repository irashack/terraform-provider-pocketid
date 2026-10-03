//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccResourceUser_basic(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccResourceUserConfig_basic("test-user", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "test-user"),
					resource.TestCheckResourceAttr(resourceName, "email", "test@example.com"),
					resource.TestCheckResourceAttr(resourceName, "first_name", "Test"),
					resource.TestCheckResourceAttr(resourceName, "last_name", "User"),
					resource.TestCheckResourceAttr(resourceName, "is_admin", "false"),
					resource.TestCheckResourceAttr(resourceName, "disabled", "false"),
					resource.TestCheckResourceAttrSet(resourceName, "id"),
				),
			},
			// ImportState testing
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Update and Read testing
			{
				Config: testAccResourceUserConfig_basic("test-user", "updated@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email", "updated@example.com"),
				),
			},
		},
	})
}

func TestAccResourceUser_customClaims(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with custom claims
			{
				Config: testAccResourceUserConfig_customClaims("test-claims-user", "claims@example.com", map[string]string{
					"department": "engineering",
					"level":      "senior",
				}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "test-claims-user"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.%", "2"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.department", "engineering"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.level", "senior"),
				),
			},
			// ImportState testing
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Update claims (change value, remove one, add one)
			{
				Config: testAccResourceUserConfig_customClaims("test-claims-user", "claims@example.com", map[string]string{
					"department": "platform",
					"location":   "remote",
				}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "custom_claims.%", "2"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.department", "platform"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.location", "remote"),
					resource.TestCheckNoResourceAttr(resourceName, "custom_claims.level"),
				),
			},
			// Clear claims
			{
				Config: testAccResourceUserConfig_basic("test-claims-user", "claims@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "custom_claims.%"),
				),
			},
		},
	})
}

func TestAccResourceUser_displayNameDefault(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user without display_name - should default to "Test User"
			{
				Config: testAccResourceUserConfig_displayNameDefault("test-display-name", "testdisplayname@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "test-display-name"),
					resource.TestCheckResourceAttr(resourceName, "first_name", "Test"),
					resource.TestCheckResourceAttr(resourceName, "last_name", "User"),
					resource.TestCheckResourceAttr(resourceName, "display_name", "Test User"),
				),
			},
		},
	})
}

func TestAccResourceUser_withGroups(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user with multiple groups
			{
				Config: testAccResourceUserConfig_withGroups("test-user", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "test-user"),
					resource.TestCheckResourceAttr(resourceName, "email", "test@example.com"),
					resource.TestCheckResourceAttr(resourceName, "groups.#", "2"),
					testAccCheckUserGroupsSet(resourceName),
				),
			},
			// Verify no changes on re-apply (groups ordering should not matter)
			{
				Config:   testAccResourceUserConfig_withGroups("test-user", "test@example.com"),
				PlanOnly: true,
			},
			// Update groups
			{
				Config: testAccResourceUserConfig_withSingleGroup("test-user", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "groups.#", "1"),
				),
			},
			// Remove all groups
			{
				Config: testAccResourceUserConfig_basic("test-user", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "groups.#", "0"),
				),
			},
		},
	})
}

func TestAccResourceUser_disabled(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user directly as disabled (supported by the API).
			{
				Config: testAccResourceUserConfig_disabled("disabled-user", "disabled@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "disabled-user"),
					resource.TestCheckResourceAttr(resourceName, "disabled", "true"),
				),
			},
			// Enable user again
			{
				Config: testAccResourceUserConfig_basic("disabled-user", "disabled@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "disabled", "false"),
				),
			},
		},
	})
}

// testAccCheckUserGroupsSet verifies that groups are stored as a set (unordered)
func testAccCheckUserGroupsSet(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}

		// Get all group attributes
		groupCount := 0
		groups := make(map[string]bool)
		groupsRegex := regexp.MustCompile(`^groups\.\d+$`)
		for key, value := range rs.Primary.Attributes {
			if groupsRegex.MatchString(key) {
				groups[value] = true
				groupCount++
			}
		}

		// Verify we have the expected number of unique groups
		if len(groups) != groupCount {
			return fmt.Errorf("duplicate groups found in state")
		}

		return nil
	}
}

func testAccResourceUserConfig_basic(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
}
`, username, email)
}

func testAccResourceUserConfig_customClaims(username, email string, claims map[string]string) string {
	var claimLines string
	for k, v := range claims {
		claimLines += fmt.Sprintf("\n    %q = %q", k, v)
	}
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
  custom_claims = {%[3]s
  }
}
`, username, email, claimLines)
}

func testAccResourceUserConfig_displayNameDefault(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
}
`, username, email)
}

func testAccResourceUserConfig_withGroups(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test1" {
  name          = "test-group-1"
  friendly_name = "Test group 1"
}

resource "pocketid_group" "test2" {
  name          = "test-group-2"
  friendly_name = "Test group 2"
}

resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"

  groups = [
    pocketid_group.test2.id,
    pocketid_group.test1.id,
  ]
}
`, username, email)
}

func testAccResourceUserConfig_withSingleGroup(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test1" {
  name          = "test-group-1"
  friendly_name = "Test group 1"
}

resource "pocketid_group" "test2" {
  name          = "test-group-2"
  friendly_name = "Test group 2"
}

resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"

  groups = [
    pocketid_group.test1.id,
  ]
}
`, username, email)
}

func testAccResourceUserConfig_disabled(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
  disabled   = true
}
`, username, email)
}

func TestAccResourceUser_adminUser(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create admin user
			{
				Config: testAccResourceUserConfig_admin("admin-user", "admin@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "admin-user"),
					resource.TestCheckResourceAttr(resourceName, "is_admin", "true"),
				),
			},
			// Remove admin privileges
			{
				Config: testAccResourceUserConfig_basic("admin-user", "admin@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "is_admin", "false"),
				),
			},
		},
	})
}

func TestAccResourceUser_withLocale(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user with locale
			{
				Config: testAccResourceUserConfig_withLocale("locale-user", "locale@example.com", "fr-FR"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "locale-user"),
					resource.TestCheckResourceAttr(resourceName, "locale", "fr-FR"),
				),
			},
			// Update locale
			{
				Config: testAccResourceUserConfig_withLocale("locale-user", "locale@example.com", "en-US"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "locale", "en-US"),
				),
			},
		},
	})
}

func TestAccResourceUser_invalidEmail(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccResourceUserConfig_basic("test-user", "invalid-email"),
				ExpectError: regexp.MustCompile("Email must be a valid email address"),
			},
		},
	})
}

func TestAccResourceUser_duplicateUsername(t *testing.T) {
	username := "duplicate-user"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create first user
			{
				Config: testAccResourceUserConfig_duplicate(username, "user1@example.com", "first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user.first", "username", username),
				),
			},
			// Attempt to create duplicate user
			{
				Config:      testAccResourceUserConfig_duplicate(username, "user2@example.com", "second"),
				ExpectError: regexp.MustCompile("HTTP 409: Conflict"),
			},
		},
	})
}

func TestAccResourceUser_updateImmutableField(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user
			{
				Config: testAccResourceUserConfig_basic("original-username", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "original-username"),
				),
			},
			// Attempt to update username (should recreate resource)
			{
				Config: testAccResourceUserConfig_basic("new-username", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "new-username"),
				),
			},
		},
	})
}

func testAccResourceUserConfig_admin(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
  is_admin   = true
}
`, username, email)
}

func testAccResourceUserConfig_withLocale(username, email, locale string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
  locale     = %[3]q
}
`, username, email, locale)
}

func testAccResourceUserConfig_duplicate(username, email, label string) string {
	if label == "first" {
		return fmt.Sprintf(`
resource "pocketid_user" "first" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
}
`, username, email)
	}
	return fmt.Sprintf(`
resource "pocketid_user" "first" {
  username   = %[1]q
  email      = "user1@example.com"
  first_name = "Test"
  last_name  = "User"
}

resource "pocketid_user" "second" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User2"
}
`, username, email)
}

func TestAccResourceUser_emailVerified(t *testing.T) {
	resourceName := "pocketid_user.test"
	username := "emailverified-user"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceUserConfig_emailVerified(username, "emailverified@example.com", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email_verified", "true"),
				),
			},
			{
				Config: testAccResourceUserConfig_emailVerified(username, "emailverified@example.com", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email_verified", "false"),
				),
			},
		},
	})
}

func testAccResourceUserConfig_emailVerified(username, email string, verified bool) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username       = %[1]q
  email          = %[2]q
  first_name     = "Test"
  last_name      = "User"
  email_verified = %[3]t
}
`, username, email, verified)
}

// A user deleted outside Terraform leaves state on the next refresh and is
// created again, instead of making every plan fail.
func TestAccResourceUser_deletedOutsideTerraform(t *testing.T) {
	resourceName := "pocketid_user.test"
	config := testAccResourceUserConfig_basic("deleted-outside", "deleted-outside@example.com")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				Check:              testAccDeleteOutsideTerraform(resourceName, "/api/users"),
				ExpectNonEmptyPlan: true,
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config,
				Check:  testAccCheckExistsOnServer(resourceName, "/api/users"),
			},
		},
	})
}

// Pocket ID ignores a group ID that names no group. A user whose groups
// include one must fail and name it, never be recorded with that group.
func TestAccResourceUser_missingGroup(t *testing.T) {
	const missingGroupID = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "pocketid_user" "test" {
  username = "missing-group-user"
  email    = "missing-group-user@example.com"
  groups   = [%q]
}
`, missingGroupID),
				ExpectError: regexp.MustCompile(`(?s)not\s+in\s+group\(s\)\s+` + missingGroupID),
			},
		},
	})
}

// Pocket ID's own rules are applied at plan time, before anything is sent.
func TestAccResourceUserGroup_planTimeValidation(t *testing.T) {
	cases := map[string]struct{ config, pattern string }{
		"username": {`resource "pocketid_user" "t" {
  username = "-bad"
  email    = "bad@example.com"
}`, `start and end with a letter or digit`},
		"reserved_claim": {`resource "pocketid_user" "t" {
  username      = "reserved-claim"
  email         = "reserved-claim@example.com"
  custom_claims = { type = "x" }
}`, `claim\s+names\s+Pocket\s+ID\s+reserves`},
		"empty_claim_value": {`resource "pocketid_group" "t" {
  name          = "empty-claim"
  friendly_name = "Empty claim"
  custom_claims = { team = "" }
}`, `custom\s+claim\s+value\s+must\s+not\s+be\s+empty`},
		"group_name": {`resource "pocketid_group" "t" {
  name          = "a"
  friendly_name = "Short name"
}`, `name must be 2 to 255 characters long`},
		"long_first_name": {`resource "pocketid_user" "t" {
  username   = "long-name"
  email      = "long-name@example.com"
  first_name = "` + strings.Repeat("a", 51) + `"
}`, `first_name must be at most 50 characters long`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      tc.config,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.pattern),
				}},
			})
		})
	}
}

// Names are limited in characters, not bytes, as on the server: 50
// two-byte characters are accepted by both.
func TestAccResourceUser_multibyteNames(t *testing.T) {
	name := strings.Repeat("é", 50)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = "multibyte-names"
  email      = "multibyte-names@example.com"
  first_name = %q
  last_name  = "x"
}
`, name),
				Check: resource.TestCheckResourceAttr("pocketid_user.test", "first_name", name),
			},
		},
	})
}

// A user without an email address: refused by default (Pocket ID requires
// one), created and stable once the instance no longer requires it.
func TestAccResourceUser_withoutEmail(t *testing.T) {
	config := `
resource "pocketid_user" "test" {
  username = "no-email-user"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`require_user_email`),
			},
			{
				PreConfig: func() { usersGroupsPatchAppConfig(t, map[string]string{"requireUserEmail": "false"}) },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pocketid_user.test", "email"),
					func(s *terraform.State) error {
						var user struct{ Email *string }
						id := s.RootModule().Resources["pocketid_user.test"].Primary.ID
						if status, err := testAccAPI("GET", "/api/users/"+id, nil, &user); err != nil || status != 200 {
							return fmt.Errorf("reading the user returned %d (%v)", status, err)
						}
						if user.Email != nil {
							return fmt.Errorf("the server holds an email address for the user")
						}
						return nil
					},
				),
			},
			{Config: config, PlanOnly: true},
		},
	})
}

// A user created with a chosen ID keeps it; changing it is a plan-time error
// (replacing a user would delete their passkeys), and leaving it out of the
// configuration later keeps the user as it is.
func TestAccResourceUser_fixedID(t *testing.T) {
	const id = "5f0c8a52-3d4e-4b1a-9c2d-7e6f5a4b3c2d"
	const otherID = "6a1d9b63-4e5f-4c2b-8d3e-8f7a6b5c4d3e"
	config := func(id string) string {
		idLine := ""
		if id != "" {
			idLine = fmt.Sprintf("  id       = %q\n", id)
		}
		return fmt.Sprintf(`
resource "pocketid_user" "test" {
%s  username = "fixed-id-user"
  email    = "fixed-id-user@example.com"
}
`, idLine)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(id),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user.test", "id", id),
					testAccCheckExistsOnServer("pocketid_user.test", "/api/users"),
				),
			},
			{Config: config(id), PlanOnly: true},
			{ResourceName: "pocketid_user.test", ImportState: true, ImportStateVerify: true},
			{Config: config(otherID), PlanOnly: true, ExpectError: regexp.MustCompile(`User ID cannot change`)},
			{Config: config(""), PlanOnly: true},
			// A second resource cannot claim the existing user by its ID.
			{
				Config: config(id) + fmt.Sprintf(`
resource "pocketid_user" "claim" {
  id       = %q
  username = "fixed-id-claim"
  email    = "fixed-id-claim@example.com"
}
`, id),
				ExpectError: regexp.MustCompile(`already exists; import it`),
			},
		},
	})
}
