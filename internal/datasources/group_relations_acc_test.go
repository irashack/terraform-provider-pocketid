//go:build acc
// +build acc

package datasources_test

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The group and user data sources report claims, members, allowed clients and
// user count as the server holds them. The groups list builds members and
// allowed clients from the user and client lists (2.15 and later) or one group
// at a time (2.14); both must agree with the group's own record.
func TestAccGroupDataSources_ClaimsMembersAndClients(t *testing.T) {
	testAccPreCheck(t)
	prefix := acctest.RandomWithPrefix("tf-acc-rel")

	groupID := b2AccCreate(t, "user-groups", map[string]string{"name": prefix + "-ops", "friendlyName": "Ops"})
	emptyID := b2AccCreate(t, "user-groups", map[string]string{"name": prefix + "-empty", "friendlyName": "Empty"})
	user1 := b2AccCreate(t, "users", map[string]any{"username": prefix + "a", "email": prefix + "a@example.com", "firstName": "A", "lastName": "A", "displayName": "A A"})
	user2 := b2AccCreate(t, "users", map[string]any{"username": prefix + "b", "email": prefix + "b@example.com", "firstName": "B", "lastName": "B", "displayName": "B B"})
	clientID := b2AccCreate(t, "oidc/clients", map[string]any{"name": prefix + "-client", "callbackURLs": []string{"https://example.com/callback"}, "isGroupRestricted": true})

	for _, call := range []struct {
		path string
		body any
	}{
		{"/api/user-groups/" + groupID + "/users", map[string]any{"userIds": []string{user1, user2}}},
		{"/api/oidc/clients/" + clientID + "/allowed-user-groups", map[string]any{"userGroupIds": []string{groupID}}},
		{"/api/custom-claims/user-group/" + groupID, []map[string]string{{"key": "tier", "value": "gold"}}},
		{"/api/custom-claims/user/" + user1, []map[string]string{{"key": "dept", "value": "ops"}, {"key": "shift", "value": "night"}}},
	} {
		if status := b2AccAPI(t, "PUT", call.path, call.body, nil); status != http.StatusOK {
			t.Fatalf("PUT %s answered %d", strings.Split(call.path, "/")[2], status)
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "pocketid_group" "by_id" {
  id = %[1]q
}

data "pocketid_group" "by_name" {
  name = %[2]q
}

data "pocketid_group" "empty" {
  id = %[3]q
}

data "pocketid_groups" "all" {}

data "pocketid_user" "first" {
  id = %[4]q
}

data "pocketid_users" "all" {}
`, groupID, prefix+"-ops", emptyID, user1),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.pocketid_group.by_id", "user_count", "2"),
				resource.TestCheckResourceAttr("data.pocketid_group.by_id", "custom_claims.tier", "gold"),
				resource.TestCheckTypeSetElemAttr("data.pocketid_group.by_id", "member_ids.*", user1),
				resource.TestCheckTypeSetElemAttr("data.pocketid_group.by_id", "member_ids.*", user2),
				resource.TestCheckTypeSetElemAttr("data.pocketid_group.by_id", "allowed_client_ids.*", clientID),
				// Found by name, the group reads the same.
				resource.TestCheckResourceAttr("data.pocketid_group.by_name", "id", groupID),
				resource.TestCheckResourceAttr("data.pocketid_group.by_name", "user_count", "2"),
				resource.TestCheckTypeSetElemAttr("data.pocketid_group.by_name", "allowed_client_ids.*", clientID),
				// A group with nothing attached has empty collections.
				resource.TestCheckResourceAttr("data.pocketid_group.empty", "user_count", "0"),
				resource.TestCheckResourceAttr("data.pocketid_group.empty", "member_ids.#", "0"),
				resource.TestCheckResourceAttr("data.pocketid_group.empty", "allowed_client_ids.#", "0"),
				resource.TestCheckResourceAttr("data.pocketid_group.empty", "custom_claims.%", "0"),
				// The list agrees with the group's own record.
				testAccCheckListedGroup("data.pocketid_groups.all", groupID, "2", []string{user1, user2}, []string{clientID}, "gold"),
				testAccCheckListedGroup("data.pocketid_groups.all", emptyID, "0", nil, nil, ""),
				resource.TestCheckResourceAttr("data.pocketid_user.first", "custom_claims.dept", "ops"),
				resource.TestCheckResourceAttr("data.pocketid_user.first", "custom_claims.shift", "night"),
				testAccCheckListedUserClaim("data.pocketid_users.all", user1, "dept", "ops"),
				testAccCheckListedUserClaim("data.pocketid_users.all", user2, "", ""),
			),
		}},
	})
}

// testAccCheckListedGroup checks one element of a pocketid_groups data
// source by the group's ID.
func testAccCheckListedGroup(address, id, userCount string, members, clients []string, tier string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccDataSourceAttributes(s, address)
		if err != nil {
			return err
		}
		prefix, err := testAccListElement(attrs, "groups", id)
		if err != nil {
			return err
		}
		if got := attrs[prefix+"user_count"]; got != userCount {
			return fmt.Errorf("group %s lists user_count %s, want %s", id, got, userCount)
		}
		if got := testAccListedValues(attrs, prefix+"member_ids."); !equalSorted(got, members) {
			return fmt.Errorf("group %s lists %d members, want %d", id, len(got), len(members))
		}
		if got := testAccListedValues(attrs, prefix+"allowed_client_ids."); !equalSorted(got, clients) {
			return fmt.Errorf("group %s lists %d clients, want %d", id, len(got), len(clients))
		}
		if got := attrs[prefix+"custom_claims.tier"]; got != tier {
			return fmt.Errorf("group %s lists a tier claim that is not the expected one", id)
		}
		return nil
	}
}

func testAccCheckListedUserClaim(address, id, key, value string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccDataSourceAttributes(s, address)
		if err != nil {
			return err
		}
		prefix, err := testAccListElement(attrs, "users", id)
		if err != nil {
			return err
		}
		if key == "" {
			if got := attrs[prefix+"custom_claims.%"]; got != "0" {
				return fmt.Errorf("user %s lists %s claims, want none", id, got)
			}
			return nil
		}
		if got := attrs[prefix+"custom_claims."+key]; got != value {
			return fmt.Errorf("user %s lists a %s claim that is not the expected one", id, key)
		}
		return nil
	}
}

func testAccDataSourceAttributes(s *terraform.State, address string) (map[string]string, error) {
	rs, ok := s.RootModule().Resources[address]
	if !ok {
		return nil, fmt.Errorf("%s is not in the state", address)
	}
	return rs.Primary.Attributes, nil
}

// testAccListElement returns the flat-state prefix ("groups.3.") of the element
// of list whose id is id.
func testAccListElement(attrs map[string]string, list, id string) (string, error) {
	for key, value := range attrs {
		if strings.HasPrefix(key, list+".") && strings.HasSuffix(key, ".id") && value == id {
			return strings.TrimSuffix(key, "id"), nil
		}
	}
	return "", fmt.Errorf("%s has no element with id %s", list, id)
}

// testAccListedValues returns the values of the set or list under prefix
// ("groups.3.member_ids."), without its count.
func testAccListedValues(attrs map[string]string, prefix string) []string {
	var values []string
	for key, value := range attrs {
		if strings.HasPrefix(key, prefix) && !strings.HasSuffix(key, "#") {
			values = append(values, value)
		}
	}
	return values
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
