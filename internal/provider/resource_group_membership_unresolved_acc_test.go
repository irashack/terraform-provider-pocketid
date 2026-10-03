//go:build acc
// +build acc

package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// An addition whose answer is lost is not forgotten by a refresh that runs
// before the request lands: the pair stays in state (tainted), a replacement
// that cannot see the membership is refused, and once the request has landed
// the next refresh sees it and clears the condition, so the membership can be
// replaced and removed normally.
func TestAccResourceGroupMembership_pendingAdditionSurvivesRefresh(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-test")
	userID := createTestUser(t, rName+"-user")
	c, err := testClient()
	if err != nil {
		t.Fatal(err)
	}
	group, err := c.CreateUserGroup(context.Background(), &client.UserGroupCreateRequest{Name: rName + "-group", FriendlyName: "Pending membership"})
	if err != nil {
		t.Fatalf("could not create the group: %s", err)
	}
	t.Cleanup(func() { _ = c.DeleteUserGroup(context.Background(), group.ID) })

	proxyURL, release := testAccHoldingProxy(t, func(r *http.Request) bool {
		return r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/api/users/"+userID+"/user-groups")
	})
	config := fmt.Sprintf(`
provider "pocketid" {
  base_url = %q
}

resource "pocketid_group_membership" "test" {
  group_id = %q
  user_id  = %q
}
`, proxyURL, group.ID, userID)
	inGroup := func(want bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			user, err := c.GetUser(context.Background(), userID)
			if err != nil {
				return err
			}
			for _, id := range user.GroupIDs() {
				if id == group.ID {
					if !want {
						return fmt.Errorf("the user is in the group")
					}
					return nil
				}
			}
			if want {
				return fmt.Errorf("the user is not in the group")
			}
			return nil
		}
	}
	replace := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pocketid_group_membership.test", plancheck.ResourceActionReplace)}}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The PUT is received but not applied, and answered 502.
				Config:      config,
				ExpectError: regexp.MustCompile(`Group membership result uncertain`),
			},
			{
				// A refresh before it lands sees the old group list.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_group_membership.test", "unresolved_creation", "true"),
					inGroup(false),
				),
			},
			{
				// The replacement of the tainted resource cannot record a
				// removal it cannot see.
				Config:           config,
				ConfigPlanChecks: replace,
				ExpectError:      regexp.MustCompile(`Group membership creation unresolved`),
			},
			{
				// The held request lands after the refreshes.
				PreConfig:          release,
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pocketid_group_membership.test", "unresolved_creation"),
					inGroup(true),
				),
			},
			{
				// The membership is tracked: the tainted resource is replaced
				// (removed and added again) and ends in the group.
				Config:           config,
				ConfigPlanChecks: replace,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pocketid_group_membership.test", "unresolved_creation"),
					inGroup(true),
				),
			},
			{Config: config, PlanOnly: true},
		},
	})
}
