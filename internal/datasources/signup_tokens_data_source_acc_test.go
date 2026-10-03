//go:build acc
// +build acc

package datasources_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// The data source lists the tokens the fixture holds, with their limits and
// groups, and never their values (Pocket ID's list carries them).
func TestAccSignupTokensDataSource_listsTokens(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := client.NewClient(os.Getenv("POCKETID_BASE_URL"), os.Getenv("POCKETID_API_TOKEN"), false, 30)
	require.NoError(t, err)

	group, err := c.CreateUserGroup(ctx, &client.UserGroupCreateRequest{Name: "tf-acc-signup-ds", FriendlyName: "tf-acc-signup-ds"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteUserGroup(context.Background(), group.ID) })

	plain, err := c.CreateSignupToken(ctx, &client.SignupTokenCreateRequest{TTL: "2h", UsageLimit: 1})
	require.NoError(t, err)
	grouped, err := c.CreateSignupToken(ctx, &client.SignupTokenCreateRequest{TTL: "2h", UsageLimit: 4, UserGroupIDs: []string{group.ID}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = c.DeleteSignupToken(context.Background(), plain.ID)
		_ = c.DeleteSignupToken(context.Background(), grouped.ID)
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "pocketid" {}

data "pocketid_signup_tokens" "all" {}

output "token_ids" {
  value = [for t in data.pocketid_signup_tokens.all.tokens : t.id]
}
`,
			Check: func(s *terraform.State) error {
				attrs := s.RootModule().Resources["data.pocketid_signup_tokens.all"].Primary.Attributes
				for key, value := range attrs {
					if value == plain.Token || value == grouped.Token {
						return fmt.Errorf("attribute %s holds a token value", key)
					}
				}
				found := map[string]string{}
				for key, value := range attrs {
					if strings.HasPrefix(key, "tokens.") && strings.HasSuffix(key, ".id") {
						found[value] = strings.TrimSuffix(key, ".id")
					}
				}
				for id, want := range map[string]*client.SignupToken{plain.ID: plain, grouped.ID: grouped} {
					prefix, ok := found[id]
					if !ok {
						return fmt.Errorf("signup token %s is not listed", id)
					}
					if _, present := attrs[prefix+".token"]; present {
						return fmt.Errorf("the data source exposes a token value for %s", id)
					}
					if attrs[prefix+".usage_limit"] != fmt.Sprint(want.UsageLimit) || attrs[prefix+".usage_count"] != "0" {
						return fmt.Errorf("unexpected limits for %s", id)
					}
					if attrs[prefix+".expires_at"] == "" || attrs[prefix+".created_at"] == "" {
						return fmt.Errorf("missing times for %s", id)
					}
					wantGroups := fmt.Sprint(len(want.UserGroups))
					if attrs[prefix+".user_group_ids.#"] != wantGroups {
						return fmt.Errorf("expected %s groups on %s", wantGroups, id)
					}
				}
				return nil
			},
		}},
	})
}
