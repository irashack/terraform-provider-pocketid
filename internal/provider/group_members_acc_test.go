//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// gmAccCreate creates an object through the API and deletes it when the test
// ends. The objects are created outside Terraform so that these tests exercise
// pocketid_group_members alone.
func gmAccCreate(t *testing.T, collection string, body any) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	status, err := testAccAPI("POST", "/api/"+collection, body, &created)
	if err != nil || status != http.StatusCreated || created.ID == "" {
		t.Fatalf("creating in %s answered %d (%v)", collection, status, err)
	}
	t.Cleanup(func() { _, _ = testAccAPI("DELETE", "/api/"+collection+"/"+created.ID, nil, nil) })
	return created.ID
}

func gmAccUser(t *testing.T, name string) string {
	t.Helper()
	return gmAccCreate(t, "users", map[string]any{"username": name, "email": name + "@example.com", "firstName": "G", "lastName": "M", "displayName": "G M"})
}

// gmAccMembers returns the IDs of the group's members as the server holds
// them.
func gmAccMembers(groupID string) ([]string, error) {
	var group struct {
		Users []struct {
			ID string `json:"id"`
		} `json:"users"`
	}
	status, err := testAccAPI("GET", "/api/user-groups/"+groupID, nil, &group)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("reading the group answered %d", status)
	}
	var ids []string
	for _, user := range group.Users {
		ids = append(ids, user.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func gmAccCheckMembers(groupID string, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := gmAccMembers(groupID)
		if err != nil {
			return err
		}
		want := append([]string(nil), want...)
		sort.Strings(want)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("the group has %d member(s) %v, want %d %v", len(got), got, len(want), want)
		}
		return nil
	}
}

func gmAccConfig(groupID string, userIDs ...string) string {
	list := ""
	for _, id := range userIDs {
		list += fmt.Sprintf("%q, ", id)
	}
	return fmt.Sprintf(`
resource "pocketid_group_members" "test" {
  group_id = %q
  user_ids = [%s]
}
`, groupID, list)
}

// The membership follows user_ids through create, update and an out-of-band
// change, and imports by group ID. Each step is checked against the server.
func TestAccResourceGroupMembers_Lifecycle(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-gm")
	groupID := gmAccCreate(t, "user-groups", map[string]string{"name": name, "friendlyName": name})
	u1, u2, u3 := gmAccUser(t, name+"a"), gmAccUser(t, name+"b"), gmAccUser(t, name+"c")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: gmAccConfig(groupID, u1, u2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_group_members.test", "id", groupID),
					resource.TestCheckResourceAttr("pocketid_group_members.test", "user_ids.#", "2"),
					gmAccCheckMembers(groupID, u1, u2),
				),
			},
			{
				// u1 leaves and u3 joins in one update.
				Config: gmAccConfig(groupID, u2, u3),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_group_members.test", "user_ids.#", "2"),
					gmAccCheckMembers(groupID, u2, u3),
				),
			},
			{
				// Someone is added outside Terraform: the plan shows it.
				PreConfig: func() {
					if status, err := testAccAPI("PUT", "/api/user-groups/"+groupID+"/users", map[string]any{"userIds": []string{u1, u2, u3}}, nil); err != nil || status != http.StatusOK {
						t.Fatalf("adding a member out of band answered %d", status)
					}
				},
				Config:             gmAccConfig(groupID, u2, u3),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Applying removes the outsider again.
				Config: gmAccConfig(groupID, u2, u3),
				Check:  gmAccCheckMembers(groupID, u2, u3),
			},
			{
				ResourceName:      "pocketid_group_members.test",
				ImportState:       true,
				ImportStateId:     groupID,
				ImportStateVerify: true,
			},
		},
	})
}

// Pocket ID drops an ID that names no user and still answers 200. The
// resource turns that into an error and records nothing.
func TestAccResourceGroupMembers_UnknownUserIsAnError(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-gm")
	groupID := gmAccCreate(t, "user-groups", map[string]string{"name": name, "friendlyName": name})
	u1 := gmAccUser(t, name+"a")
	const missing = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      gmAccConfig(groupID, u1, missing),
			ExpectError: regexp.MustCompile(`(?s)Group members differ from the request.*` + missing),
		}},
	})
}

// A group that already has members is not taken over silently: creating the
// resource would remove them, and the plan could not show that. The group is
// left as it was.
func TestAccResourceGroupMembers_RefusesToRemoveMembersThePlanDidNotShow(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-gm")
	groupID := gmAccCreate(t, "user-groups", map[string]string{"name": name, "friendlyName": name})
	existing, wanted := gmAccUser(t, name+"a"), gmAccUser(t, name+"b")
	if status, err := testAccAPI("PUT", "/api/user-groups/"+groupID+"/users", map[string]any{"userIds": []string{existing}}, nil); err != nil || status != http.StatusOK {
		t.Fatalf("seeding a member answered %d", status)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      gmAccConfig(groupID, wanted),
				ExpectError: regexp.MustCompile(`Group has members the plan\s+did not show`),
			},
			{
				// Listing the current member as well is fine.
				Config: gmAccConfig(groupID, existing, wanted),
				Check:  gmAccCheckMembers(groupID, existing, wanted),
			},
		},
	})
}

// Destroying removes the members and leaves the group. (A member who joins
// between the destroy's own refresh and its delete stays; the unit tests cover
// that.)
func TestAccResourceGroupMembers_DestroyEmptiesTheGroupAndKeepsIt(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-gm")
	groupID := gmAccCreate(t, "user-groups", map[string]string{"name": name, "friendlyName": name})
	u1, u2 := gmAccUser(t, name+"a"), gmAccUser(t, name+"b")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             func(*terraform.State) error { return gmAccCheckMembers(groupID)(nil) },
		Steps: []resource.TestStep{{
			Config: gmAccConfig(groupID, u1, u2),
			Check:  gmAccCheckMembers(groupID, u1, u2),
		}},
	})
}

// Deleting the group outside Terraform ends the resource at the next refresh
// instead of wedging every plan.
func TestAccResourceGroupMembers_GroupDeletedOutsideTerraform(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tf-acc-gm")
	groupID := gmAccCreate(t, "user-groups", map[string]string{"name": name, "friendlyName": name})
	u1 := gmAccUser(t, name+"a")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: gmAccConfig(groupID, u1), Check: gmAccCheckMembers(groupID, u1)},
			{
				PreConfig: func() {
					if status, err := testAccAPI("DELETE", "/api/user-groups/"+groupID, nil, nil); err != nil || status != http.StatusNoContent {
						t.Fatalf("deleting the group answered %d", status)
					}
				},
				Config:             gmAccConfig(groupID, u1),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true, // the resource is gone from state, so it would be created again
			},
		},
	})
}
