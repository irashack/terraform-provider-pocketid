//go:build acc
// +build acc

package provider_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

// usersGroupsPatchAppConfig changes the given application configuration
// keys, as an administrator would in Pocket ID's settings, and restores the
// whole configuration when the test ends.
func usersGroupsPatchAppConfig(t *testing.T, changes map[string]string) {
	t.Helper()
	var all []struct{ Key, Value string }
	status, err := testAccAPI("GET", "/api/application-configuration/all", nil, &all)
	require.NoError(t, err)
	require.Equal(t, 200, status)
	original := map[string]string{}
	for _, v := range all {
		original[v.Key] = v.Value
	}
	changed := map[string]string{}
	for k, v := range original {
		changed[k] = v
	}
	for k, v := range changes {
		changed[k] = v
	}
	status, err = testAccAPI("PUT", "/api/application-configuration", changed, nil)
	require.NoError(t, err)
	require.Equal(t, 200, status, "changing the application configuration")
	t.Cleanup(func() {
		status, err := testAccAPI("PUT", "/api/application-configuration", original, nil)
		if err != nil || status != 200 {
			t.Errorf("restoring the application configuration returned %d (%v)", status, err)
		}
	})
}

// usersGroupsSetSignupDefaults makes every user created through the API from
// now on receive groupID and the claim dept=default.
func usersGroupsSetSignupDefaults(t *testing.T, groupID string) {
	t.Helper()
	ids, _ := json.Marshal([]string{groupID})
	usersGroupsPatchAppConfig(t, map[string]string{
		"signupDefaultUserGroupIDs": string(ids),
		"signupDefaultCustomClaims": `[{"key":"dept","value":"default"}]`,
	})
}

// usersGroupsCheckServerUser compares the groups and claims Pocket ID holds
// for the user in state with the expected ones (group resource addresses).
func usersGroupsCheckServerUser(resourceName string, groupResources []string, claims map[string]string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		var wantGroups []string
		for _, name := range groupResources {
			group, ok := s.RootModule().Resources[name]
			if !ok {
				return fmt.Errorf("resource not found: %s", name)
			}
			wantGroups = append(wantGroups, group.Primary.ID)
		}
		var user struct {
			UserGroups   []struct{ ID string }
			CustomClaims []struct{ Key, Value string }
		}
		status, err := testAccAPI("GET", "/api/users/"+rs.Primary.ID, nil, &user)
		if err != nil || status != 200 {
			return fmt.Errorf("reading the user returned %d (%v)", status, err)
		}
		var gotGroups []string
		for _, g := range user.UserGroups {
			gotGroups = append(gotGroups, g.ID)
		}
		slices.Sort(gotGroups)
		slices.Sort(wantGroups)
		if !slices.Equal(gotGroups, wantGroups) {
			return fmt.Errorf("the server holds groups %v, want %v", gotGroups, wantGroups)
		}
		gotClaims := map[string]string{}
		for _, c := range user.CustomClaims {
			gotClaims[c.Key] = c.Value
		}
		if fmt.Sprint(gotClaims) != fmt.Sprint(claims) {
			return fmt.Errorf("the server holds claims %v, want %v", gotClaims, claims)
		}
		return nil
	}
}

// A user created while signup defaults are configured holds exactly the
// planned groups and claims, whether they are omitted, explicitly empty or
// set, and each form plans empty afterwards. Omitted names stay null.
func TestAccResourceUser_signupDefaultsReplaced(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-defaults")
	groups := fmt.Sprintf(`
resource "pocketid_group" "default" {
  name          = "%[1]s-default"
  friendly_name = "Signup default"
}
resource "pocketid_group" "other" {
  name          = "%[1]s-other"
  friendly_name = "Other"
}
`, rName)
	user := func(name, extra string) string {
		return groups + fmt.Sprintf(`
resource "pocketid_user" %[1]q {
  username = "%[2]s-%[1]s"
  email    = "%[2]s-%[1]s@example.com"
%[3]s
}
`, name, rName, extra)
	}
	omitted := user("omitted", "")
	empty := user("empty", "  groups = []\n  custom_claims = {}\n")
	set := user("set", "  groups = [pocketid_group.other.id]\n  custom_claims = { team = \"a\" }\n")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groups},
			{
				PreConfig: func() {
					var group struct{ ID string }
					var list struct{ Data []struct{ ID, Name string } }
					status, err := testAccAPI("GET", "/api/user-groups?search="+rName+"-default", nil, &list)
					require.NoError(t, err)
					require.Equal(t, 200, status)
					for _, g := range list.Data {
						if g.Name == rName+"-default" {
							group.ID = g.ID
						}
					}
					require.NotEmpty(t, group.ID)
					usersGroupsSetSignupDefaults(t, group.ID)
				},
				Config: omitted,
				Check: resource.ComposeAggregateTestCheckFunc(
					usersGroupsCheckServerUser("pocketid_user.omitted", nil, map[string]string{}),
					resource.TestCheckNoResourceAttr("pocketid_user.omitted", "groups.#"),
					resource.TestCheckNoResourceAttr("pocketid_user.omitted", "custom_claims.%"),
					resource.TestCheckNoResourceAttr("pocketid_user.omitted", "first_name"),
					resource.TestCheckNoResourceAttr("pocketid_user.omitted", "last_name"),
				),
			},
			{Config: omitted, PlanOnly: true},
			{
				Config: empty,
				Check: resource.ComposeAggregateTestCheckFunc(
					usersGroupsCheckServerUser("pocketid_user.empty", nil, map[string]string{}),
					resource.TestCheckResourceAttr("pocketid_user.empty", "groups.#", "0"),
					resource.TestCheckResourceAttr("pocketid_user.empty", "custom_claims.%", "0"),
				),
			},
			{Config: empty, PlanOnly: true},
			{
				Config: set,
				Check: resource.ComposeAggregateTestCheckFunc(
					usersGroupsCheckServerUser("pocketid_user.set", []string{"pocketid_group.other"}, map[string]string{"team": "a"}),
					resource.TestCheckResourceAttr("pocketid_user.set", "groups.#", "1"),
					resource.TestCheckResourceAttr("pocketid_user.set", "custom_claims.team", "a"),
				),
			},
			{Config: set, PlanOnly: true},
			// Clearing with explicit empty values applies cleanly.
			{
				Config: user("set", "  groups = []\n  custom_claims = {}\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					usersGroupsCheckServerUser("pocketid_user.set", nil, map[string]string{}),
					resource.TestCheckResourceAttr("pocketid_user.set", "groups.#", "0"),
					resource.TestCheckResourceAttr("pocketid_user.set", "custom_claims.%", "0"),
				),
			},
			{Config: user("set", "  groups = []\n  custom_claims = {}\n"), PlanOnly: true},
		},
	})
}

// Group claims: an explicit empty map is stable, and clearing populated
// claims with {} applies cleanly.
func TestAccResourceGroup_customClaimsExplicitEmpty(t *testing.T) {
	resourceName := "pocketid_group.test"
	groupName := acctest.RandomWithPrefix("tf-acc-test") + "-empty-claims"
	config := func(claims string) string {
		return fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = %q
  friendly_name = "Empty claims"
  custom_claims = %s
}
`, groupName, claims)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("{}"),
				Check:  resource.TestCheckResourceAttr(resourceName, "custom_claims.%", "0"),
			},
			{Config: config("{}"), PlanOnly: true},
			{
				Config: config(`{ role = "admin" }`),
				Check:  resource.TestCheckResourceAttr(resourceName, "custom_claims.role", "admin"),
			},
			{
				Config: config("{}"),
				Check:  resource.TestCheckResourceAttr(resourceName, "custom_claims.%", "0"),
			},
			{Config: config("{}"), PlanOnly: true},
		},
	})
}
