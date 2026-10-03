//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

const signupTokenResourceName = "pocketid_signup_token.test"

type signupTokenView struct {
	ID         string `json:"id"`
	UsageLimit int    `json:"usageLimit"`
	UsageCount int    `json:"usageCount"`
	UserGroups []struct {
		ID string `json:"id"`
	} `json:"userGroups"`
}

// testAccSignupTokensOnServer lists the tokens the fixture holds (the list
// carries each token's value; only the decoded metadata is returned).
func testAccSignupTokensOnServer() ([]signupTokenView, error) {
	var page struct {
		Data []signupTokenView `json:"data"`
	}
	status, err := testAccAPI("GET", "/api/signup-tokens?pagination%5Blimit%5D=100", nil, &page)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("listing the signup tokens answered HTTP %d", status)
	}
	return page.Data, nil
}

func testAccSignupTokenOnServer(id string) (*signupTokenView, error) {
	tokens, err := testAccSignupTokensOnServer()
	if err != nil {
		return nil, err
	}
	for i := range tokens {
		if tokens[i].ID == id {
			return &tokens[i], nil
		}
	}
	return nil, nil
}

// testAccCheckSignupTokenDestroyed fails when a token the test created still
// exists on the server after the destroy.
func testAccCheckSignupTokensDestroyed(ids *[]string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		for _, id := range *ids {
			token, err := testAccSignupTokenOnServer(id)
			if err != nil {
				return err
			}
			if token != nil {
				return fmt.Errorf("signup token %s still exists after the destroy", id)
			}
		}
		return nil
	}
}

func testAccRememberSignupToken(ids *[]string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[signupTokenResourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", signupTokenResourceName)
		}
		*ids = append(*ids, rs.Primary.ID)
		return nil
	}
}

func testAccSignupTokenConfig(name, extra string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_group" "test" {
  name          = %[1]q
  friendly_name = %[1]q
}

resource "pocketid_signup_token" "test" {
  %[2]s
}
`, name, extra)
}

// testAccSignUp registers a user with the token the way a new person would,
// and returns the new user's ID.
func testAccSignUp(t *testing.T, token, username string) string {
	t.Helper()
	var user struct {
		ID         string `json:"id"`
		UserGroups []struct {
			ID string `json:"id"`
		} `json:"userGroups"`
	}
	status, err := testAccAPI("POST", "/api/signup", map[string]any{
		"username": username, "firstName": "Signup", "lastName": "Test", "email": username + "@example.com", "token": token,
	}, &user)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	t.Cleanup(func() { _, _ = testAccAPI("DELETE", "/api/users/"+user.ID, nil, nil) })
	return user.ID
}

// A token is created with its groups, limits and secret, reports its use,
// and is replaced (and the old one deleted) when an input changes.
func TestAccResourceSignupToken_lifecycle(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-signup")
	var ids []string
	var tokenValue string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckSignupTokensDestroyed(&ids),
		Steps: []resource.TestStep{
			{
				Config: testAccSignupTokenConfig(name, `
  ttl            = "24h"
  usage_limit    = 3
  user_group_ids = [pocketid_group.test.id]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(signupTokenResourceName, "ttl", "24h"),
					resource.TestCheckResourceAttr(signupTokenResourceName, "usage_limit", "3"),
					resource.TestCheckResourceAttr(signupTokenResourceName, "usage_count", "0"),
					resource.TestCheckResourceAttr(signupTokenResourceName, "expired", "false"),
					resource.TestCheckResourceAttr(signupTokenResourceName, "user_group_ids.#", "1"),
					resource.TestCheckResourceAttrSet(signupTokenResourceName, "token"),
					resource.TestCheckResourceAttrSet(signupTokenResourceName, "expires_at"),
					resource.TestCheckResourceAttrSet(signupTokenResourceName, "created_at"),
					testAccRememberSignupToken(&ids),
					func(s *terraform.State) error {
						rs := s.RootModule().Resources[signupTokenResourceName]
						tokenValue = rs.Primary.Attributes["token"]
						onServer, err := testAccSignupTokenOnServer(rs.Primary.ID)
						if err != nil {
							return err
						}
						if onServer == nil || onServer.UsageLimit != 3 || len(onServer.UserGroups) != 1 {
							return fmt.Errorf("the server does not hold the token as configured")
						}
						return nil
					},
				),
			},
			{
				// Somebody registers with the token: the next refresh shows the use.
				PreConfig: func() { testAccSignUp(t, tokenValue, "tfacc-"+acctest.RandString(8)) },
				Config: testAccSignupTokenConfig(name, `
  ttl            = "24h"
  usage_limit    = 3
  user_group_ids = [pocketid_group.test.id]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(signupTokenResourceName, "usage_count", "1"),
					resource.TestCheckResourceAttr(signupTokenResourceName, "expired", "false"),
				),
			},
			{
				// A changed input replaces the token: new ID, old token gone.
				Config: testAccSignupTokenConfig(name, `
  ttl            = "24h"
  usage_limit    = 5
  user_group_ids = [pocketid_group.test.id]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(signupTokenResourceName, "usage_limit", "5"),
					resource.TestCheckResourceAttr(signupTokenResourceName, "usage_count", "0"),
					testAccRememberSignupToken(&ids),
					func(*terraform.State) error {
						old, err := testAccSignupTokenOnServer(ids[0])
						if err != nil {
							return err
						}
						if old != nil {
							return fmt.Errorf("the replaced token still exists")
						}
						return nil
					},
				),
			},
		},
	})
}

// A token that expires is purged by Pocket ID. It stays in state, marked
// expired, and a plan stays empty; the destroy still succeeds. The test
// framework asserts the empty plan after every step.
func TestAccResourceSignupToken_expiredTokenStaysInState(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-signup-exp")
	config := testAccSignupTokenConfig(name, `
  ttl            = "3s"
  user_group_ids = [pocketid_group.test.id]`)
	var ids []string
	var firstID, firstToken string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckSignupTokensDestroyed(&ids),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(signupTokenResourceName, "usage_limit", "1"),
					testAccRememberSignupToken(&ids),
					func(s *terraform.State) error {
						rs := s.RootModule().Resources[signupTokenResourceName]
						firstID, firstToken = rs.Primary.ID, rs.Primary.Attributes["token"]
						return nil
					},
				),
			},
			{
				// Wait until Pocket ID has purged the token.
				PreConfig: func() {
					deadline := time.Now().Add(30 * time.Second)
					for time.Now().Before(deadline) {
						token, err := testAccSignupTokenOnServer(firstID)
						require.NoError(t, err)
						if token == nil {
							return
						}
						time.Sleep(500 * time.Millisecond)
					}
					t.Fatal("Pocket ID still lists the token after its lifetime")
				},
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(signupTokenResourceName, "expired", "true"),
					func(s *terraform.State) error {
						rs := s.RootModule().Resources[signupTokenResourceName]
						if rs.Primary.ID != firstID || rs.Primary.Attributes["token"] != firstToken {
							return fmt.Errorf("the expired token was replaced")
						}
						return nil
					},
				),
			},
			{
				// And it stays that way.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// A token an administrator deletes is indistinguishable from an expired one
// and is treated the same: kept in state as expired, nothing recreated.
func TestAccResourceSignupToken_deletedOutsideTerraform(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-signup-del")
	config := testAccSignupTokenConfig(name, ``)
	var ids []string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckSignupTokensDestroyed(&ids),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(signupTokenResourceName, "ttl", "1h"),
					resource.TestCheckResourceAttr(signupTokenResourceName, "usage_limit", "1"),
					resource.TestCheckNoResourceAttr(signupTokenResourceName, "user_group_ids.#"),
					testAccRememberSignupToken(&ids),
				),
			},
			{
				PreConfig: func() {
					status, err := testAccAPI("DELETE", "/api/signup-tokens/"+ids[0], nil, nil)
					require.NoError(t, err)
					require.Equal(t, http.StatusNoContent, status)
				},
				Config: config,
				Check:  resource.TestCheckResourceAttr(signupTokenResourceName, "expired", "true"),
			},
		},
	})
}

// Pocket ID silently ignores a group ID that names no group; the provider
// reports it, keeps the token in state as tainted, and the destroy removes it.
func TestAccResourceSignupToken_unknownGroupIsReported(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-signup-grp")
	var before int

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			tokens, err := testAccSignupTokensOnServer()
			require.NoError(t, err)
			before = len(tokens)
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			tokens, err := testAccSignupTokensOnServer()
			if err != nil {
				return err
			}
			if len(tokens) != before {
				return fmt.Errorf("%d signup tokens exist after the test, %d before it", len(tokens), before)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: testAccSignupTokenConfig(name, `
  user_group_ids = [pocketid_group.test.id, "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"]`),
				ExpectError: regexp.MustCompile(`(?s)did not create the signup token as requested.*0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f`),
			},
		},
	})
}

func TestAccResourceSignupToken_invalidInputs(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-signup-bad")
	cases := map[string]string{
		`ttl            = "1s"`:           `Invalid ttl`,
		`ttl            = "745h"`:         `Invalid ttl`,
		`ttl            = "tomorrow"`:     `Invalid ttl`,
		`usage_limit    = 0`:              `usage_limit`,
		`usage_limit    = 101`:            `usage_limit`,
		`user_group_ids = ["not-a-uuid"]`: `UUID`,
		`user_group_ids = ["0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f/../x"]`: `UUID`,
	}
	for extra, want := range cases {
		t.Run(extra, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testAccSignupTokenConfig(name, extra),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(want),
				}},
			})
		})
	}
}
