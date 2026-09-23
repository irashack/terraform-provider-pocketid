//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// createTestUser creates a user directly through the API, bypassing the
// pocketid_user resource entirely. pocketid_user's "groups" attribute is
// authoritative over a user's full group list (including resetting it to
// empty when "groups" is left unset in configuration - see
// TestAccResourceUser_withGroups's "Remove all groups" step), so a user
// managed by a pocketid_user resource fights any pocketid_group_membership
// resource pointed at it: the very next unrelated pocketid_user apply plans to
// clear every membership pocketid_group_membership added. These tests target
// a user pocketid_user never touches, which is the supported combination
// documented on pocketid_group_membership.
func createTestUser(t *testing.T, username string) string {
	t.Helper()

	c, err := testClient()
	if err != nil {
		t.Fatalf("failed to create test client: %s", err)
	}

	user, err := c.CreateUser(&client.UserCreateRequest{
		Username: username,
		Email:    username + "@example.com",
	})
	if err != nil {
		t.Fatalf("failed to create test user %s: %s", username, err)
	}

	t.Cleanup(func() {
		_ = c.DeleteUser(user.ID)
	})

	return user.ID
}

func TestAccResourceGroupMembership_basic(t *testing.T) {
	testAccPreCheck(t)
	resourceName := "pocketid_group_membership.test"
	rName := acctest.RandomWithPrefix("tf-acc-test")
	userID := createTestUser(t, rName+"-user")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccResourceGroupMembershipConfig_basic(rName, userID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrPair(resourceName, "group_id", "pocketid_group.test", "id"),
					resource.TestCheckResourceAttr(resourceName, "user_id", userID),
					testAccCheckGroupMembershipExists("pocketid_group.test", userID),
				),
			},
			// ImportState testing. The resource's id ("<group_id>/<user_id>") is
			// already the expected import identifier.
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccResourceGroupMembership_deleteRemovesOnlyThatMember(t *testing.T) {
	testAccPreCheck(t)
	resourceName := "pocketid_group_membership.test"
	rName := acctest.RandomWithPrefix("tf-acc-test")
	userID := createTestUser(t, rName+"-user")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceGroupMembershipConfig_basic(rName, userID),
				Check:  resource.TestCheckResourceAttrSet(resourceName, "id"),
			},
			// Removing the resource from configuration must delete only this
			// (group, user) pair.
			{
				Config: testAccResourceGroupMembershipConfig_groupOnly(rName),
				Check:  testAccCheckGroupMembershipAbsent("pocketid_group.test", userID),
			},
		},
	})
}

// TestAccResourceGroupMembership_nonAuthoritative proves that this resource
// manages exactly one (group, user) pair: removing one Terraform-managed
// membership from a group must not disturb another Terraform-managed
// membership of the same group.
func TestAccResourceGroupMembership_nonAuthoritative(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-test")
	userOneID := createTestUser(t, rName+"-one")
	userTwoID := createTestUser(t, rName+"-two")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Add both users to the group.
			{
				Config: testAccResourceGroupMembershipConfig_twoMembers(rName, userOneID, userTwoID),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckGroupMembershipExists("pocketid_group.test", userOneID),
					testAccCheckGroupMembershipExists("pocketid_group.test", userTwoID),
				),
			},
			// Remove only the second membership resource; the first must survive
			// untouched, proving Delete does not replace the group's full member list.
			{
				Config: testAccResourceGroupMembershipConfig_oneMember(rName, userOneID),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckGroupMembershipExists("pocketid_group.test", userOneID),
					testAccCheckGroupMembershipAbsent("pocketid_group.test", userTwoID),
				),
			},
		},
	})
}

// TestAccResourceGroupMembership_preservesUnmanagedMember proves the same
// non-authoritative contract against a member added directly through the API
// after the managed membership already exists, mirroring an external
// onboarding broker adding other members to a group Terraform partially
// manages.
func TestAccResourceGroupMembership_preservesUnmanagedMember(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-test")
	managedUserID := createTestUser(t, rName+"-managed")
	unmanagedUserID := createTestUser(t, rName+"-unmanaged")
	var groupID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create the managed membership, then add a second member directly
			// through the API, outside Terraform entirely.
			{
				Config: testAccResourceGroupMembershipConfig_basic(rName, managedUserID),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckGroupMembershipExists("pocketid_group.test", managedUserID),
					func(s *terraform.State) error {
						groupRS, ok := s.RootModule().Resources["pocketid_group.test"]
						if !ok {
							return fmt.Errorf("not found: pocketid_group.test")
						}
						groupID = groupRS.Primary.ID

						c, err := testClient()
						if err != nil {
							return err
						}
						return c.AddUserToGroup(unmanagedUserID, groupID)
					},
				),
			},
			// Destroy the Terraform-managed membership. The out-of-band member must
			// remain, proving Delete removes only the one pair it manages.
			{
				Config: testAccResourceGroupMembershipConfig_groupOnly(rName),
				Check: func(s *terraform.State) error {
					c, err := testClient()
					if err != nil {
						return err
					}
					has, err := c.UserHasGroupMembership(unmanagedUserID, groupID)
					if err != nil {
						return err
					}
					if !has {
						return fmt.Errorf(
							"expected unmanaged user %s to remain a member of group %s after the managed membership was deleted",
							unmanagedUserID, groupID,
						)
					}
					return nil
				},
			},
		},
	})
}

// TestAccResourceGroupMembership_driftDetection proves that Read notices when
// the membership was removed outside Terraform (there is no API event to
// subscribe to) and drops it from state, rather than reporting an empty plan.
func TestAccResourceGroupMembership_driftDetection(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-test")
	userID := createTestUser(t, rName+"-user")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// The framework replans after every step's Check runs, to confirm the
			// applied config leaves an empty plan. This step's Check deliberately
			// removes the membership out from under Terraform, so that replan
			// finds the resource missing and (correctly) proposes recreating it.
			{
				Config: testAccResourceGroupMembershipConfig_basic(rName, userID),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckGroupMembershipExists("pocketid_group.test", userID),
					testAccDriftRemoveGroupMembership("pocketid_group.test", userID),
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccResourceGroupMembership_invalidImportID(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-test")
	userID := createTestUser(t, rName+"-user")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceGroupMembershipConfig_basic(rName, userID),
			},
			{
				ResourceName:  "pocketid_group_membership.test",
				ImportState:   true,
				ImportStateId: "not-a-valid-composite-id",
				ExpectError:   regexp.MustCompile("Unexpected Import Identifier"),
			},
		},
	})
}

func testAccCheckGroupMembershipExists(groupResourceName, userID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		groupRS, ok := s.RootModule().Resources[groupResourceName]
		if !ok {
			return fmt.Errorf("not found: %s", groupResourceName)
		}
		c, err := testClient()
		if err != nil {
			return err
		}
		has, err := c.UserHasGroupMembership(userID, groupRS.Primary.ID)
		if err != nil {
			return err
		}
		if !has {
			return fmt.Errorf("expected user %s to be a member of group %s", userID, groupRS.Primary.ID)
		}
		return nil
	}
}

func testAccCheckGroupMembershipAbsent(groupResourceName, userID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		groupRS, ok := s.RootModule().Resources[groupResourceName]
		if !ok {
			return fmt.Errorf("not found: %s", groupResourceName)
		}
		c, err := testClient()
		if err != nil {
			return err
		}
		has, err := c.UserHasGroupMembership(userID, groupRS.Primary.ID)
		if err != nil {
			return err
		}
		if has {
			return fmt.Errorf("expected user %s to no longer be a member of group %s", userID, groupRS.Primary.ID)
		}
		return nil
	}
}

// testAccDriftRemoveGroupMembership removes a membership directly through the
// API, simulating a change made outside Terraform.
func testAccDriftRemoveGroupMembership(groupResourceName, userID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		groupRS, ok := s.RootModule().Resources[groupResourceName]
		if !ok {
			return fmt.Errorf("not found: %s", groupResourceName)
		}
		c, err := testClient()
		if err != nil {
			return err
		}
		return c.RemoveUserFromGroup(userID, groupRS.Primary.ID)
	}
}

func testAccResourceGroupMembershipConfig_basic(rName, userID string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = "%[1]s-group"
  friendly_name = "Membership Test Group"
}

resource "pocketid_group_membership" "test" {
  group_id = pocketid_group.test.id
  user_id  = %[2]q
}
`, rName, userID)
}

func testAccResourceGroupMembershipConfig_groupOnly(rName string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = "%[1]s-group"
  friendly_name = "Membership Test Group"
}
`, rName)
}

func testAccResourceGroupMembershipConfig_twoMembers(rName, userOneID, userTwoID string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = "%[1]s-group"
  friendly_name = "Membership Test Group"
}

resource "pocketid_group_membership" "one" {
  group_id = pocketid_group.test.id
  user_id  = %[2]q
}

resource "pocketid_group_membership" "two" {
  group_id = pocketid_group.test.id
  user_id  = %[3]q
}
`, rName, userOneID, userTwoID)
}

func testAccResourceGroupMembershipConfig_oneMember(rName, userOneID string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = "%[1]s-group"
  friendly_name = "Membership Test Group"
}

resource "pocketid_group_membership" "one" {
  group_id = pocketid_group.test.id
  user_id  = %[2]q
}
`, rName, userOneID)
}
