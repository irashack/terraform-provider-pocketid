//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/stretchr/testify/require"
)

func TestAccResourceScimServiceProvider_basic(t *testing.T) {
	resourceName := "pocketid_scim_service_provider.test"
	rName := acctest.RandomWithPrefix("tf-acc-test")
	clientName := rName + "-client"
	endpoint := "https://scim.example.com/v2"
	token := "test-bearer-token"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing, including round-trip of the token.
			{
				Config: testAccResourceScimServiceProviderConfig_basic(clientName, endpoint, token),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "endpoint", endpoint),
					resource.TestCheckResourceAttr(resourceName, "token", token),
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrSet(resourceName, "client_id"),
					resource.TestCheckResourceAttrSet(resourceName, "created_at"),
					testAccCheckScimServiceProviderExists(resourceName),
				),
			},
			// ImportState testing using the OIDC client ID.
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				// Import is keyed on the OIDC client ID, not the SCIM config ID,
				// so the import ID must be the client_id attribute value.
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources[resourceName]
					if !ok {
						return "", fmt.Errorf("Not found: %s", resourceName)
					}
					return rs.Primary.Attributes["client_id"], nil
				},
				// The token is returned by the API on read, so it round-trips
				// and is verified against the imported state.
				// pocket-id returns created_at with nanosecond precision on
				// create but truncated to seconds on GET, so it cannot be
				// verified byte-for-byte after import.
				ImportStateVerifyIgnore: []string{"created_at"},
			},
			// Update and Read testing.
			{
				Config: testAccResourceScimServiceProviderConfig_basic(clientName, endpoint+"/updated", token),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "endpoint", endpoint+"/updated"),
					resource.TestCheckResourceAttr(resourceName, "token", token),
				),
			},
		},
	})
}

func testAccCheckScimServiceProviderExists(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("No SCIM service provider ID is set")
		}

		return nil
	}
}

func testAccResourceScimServiceProviderConfig_basic(clientName, endpoint, token string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %[1]q
  callback_urls = ["https://example.com/callback"]
}

resource "pocketid_scim_service_provider" "test" {
  client_id = pocketid_client.test.id
  endpoint  = %[2]q
  token     = %[3]q
}
`, clientName, endpoint, token)
}

// scimServerView is what the fixture's Pocket ID holds for a client's SCIM
// service provider; the token is the decrypted value the server returns.
type scimServerView struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

func testAccScimServerView(clientID string) (scimServerView, error) {
	var view scimServerView
	status, err := testAccAPI("GET", "/api/oidc/clients/"+clientID+"/scim-service-provider", nil, &view)
	if err != nil {
		return view, err
	}
	if status != http.StatusOK {
		return view, fmt.Errorf("reading the SCIM service provider answered HTTP %d", status)
	}
	return view, nil
}

// testAccScimPutOutside replaces the SCIM service provider the way an
// administrator would outside Terraform.
func testAccScimPutOutside(t *testing.T, clientID, endpoint, token string) {
	t.Helper()
	view, err := testAccScimServerView(clientID)
	require.NoError(t, err)
	status, err := testAccAPI("PUT", "/api/scim/service-provider/"+view.ID,
		map[string]string{"endpoint": endpoint, "token": token, "oidcClientId": clientID}, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
}

// testAccScimCheckServerToken compares the token the server holds with want.
func testAccScimCheckServerToken(resourceName, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}
		view, err := testAccScimServerView(rs.Primary.Attributes["client_id"])
		if err != nil {
			return err
		}
		if view.Token != want {
			return fmt.Errorf("the server holds a different token than expected (lengths %d and %d)", len(view.Token), len(want))
		}
		return nil
	}
}

func testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint, tokenLine string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %[1]q
  callback_urls = ["https://example.com/callback"]
}

resource "pocketid_scim_service_provider" "test" {
  client_id = pocketid_client.test.id
  endpoint  = %[2]q
  %[3]s
}
`, clientName, endpoint, tokenLine)
}

// A token an administrator clears outside Terraform is drift: the next plan
// shows it and an apply restores the configured token.
func TestAccResourceScimServiceProvider_tokenClearedOutsideTerraform(t *testing.T) {
	resourceName := "pocketid_scim_service_provider.test"
	clientName := acctest.RandomWithPrefix("tf-acc-scim-drift")
	endpoint := "https://scim.example.com/v2"
	config := testAccResourceScimServiceProviderConfig_basic(clientName, endpoint, "configured-token")
	var clientID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "token", "configured-token"),
					testAccScimCheckServerToken(resourceName, "configured-token"),
					func(s *terraform.State) error {
						clientID = s.RootModule().Resources[resourceName].Primary.Attributes["client_id"]
						return nil
					},
				),
			},
			{
				// The refresh must see the empty token and the plan must repair it.
				PreConfig:          func() { testAccScimPutOutside(t, clientID, endpoint, "") },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "token", "configured-token"),
					testAccScimCheckServerToken(resourceName, "configured-token"),
				),
			},
			{
				// A token changed outside Terraform is drift as well.
				PreConfig:          func() { testAccScimPutOutside(t, clientID, endpoint, "changed-outside") },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// A configuration without a token, and one with an explicitly empty token,
// both mean "no token" and plan empty after every refresh. (Each step also
// asserts the empty plan the test framework runs after its apply.)
func TestAccResourceScimServiceProvider_noTokenDoesNotChurn(t *testing.T) {
	resourceName := "pocketid_scim_service_provider.test"
	clientName := acctest.RandomWithPrefix("tf-acc-scim-notoken")
	endpoint := "https://scim.example.com/v2"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "token"),
					testAccScimCheckServerToken(resourceName, ""),
				),
			},
			{
				Config:   testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint, ""),
				PlanOnly: true,
			},
			{
				// Setting the token to "" is a change from null that the server
				// cannot tell apart; it must apply cleanly and then stay quiet.
				Config: testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint, `token = ""`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "token", ""),
					testAccScimCheckServerToken(resourceName, ""),
				),
			},
			{
				Config:   testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint, `token = ""`),
				PlanOnly: true,
			},
		},
	})
}

// A SCIM service provider deleted outside Terraform plans a new one; before,
// the refresh failed and wedged every plan.
func TestAccResourceScimServiceProvider_deletedOutsideTerraform(t *testing.T) {
	resourceName := "pocketid_scim_service_provider.test"
	clientName := acctest.RandomWithPrefix("tf-acc-scim-gone")
	config := testAccResourceScimServiceProviderConfig_basic(clientName, "https://scim.example.com/v2", "configured-token")
	var firstID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					firstID = s.RootModule().Resources[resourceName].Primary.ID
					return nil
				},
			},
			{
				PreConfig: func() {
					status, err := testAccAPI("DELETE", "/api/scim/service-provider/"+firstID, nil, nil)
					require.NoError(t, err)
					require.Equal(t, http.StatusNoContent, status)
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config,
				Check: func(s *terraform.State) error {
					if s.RootModule().Resources[resourceName].Primary.ID == firstID {
						return fmt.Errorf("the SCIM service provider was not created again")
					}
					return nil
				},
			},
		},
	})
}

// testAccScimStateHoldsNo fails when any attribute of the resource in state
// contains secret: the write-only token must never be stored.
func testAccScimStateHoldsNo(resourceName, secret string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}
		for name, value := range rs.Primary.Attributes {
			if strings.Contains(value, secret) {
				return fmt.Errorf("attribute %s holds the write-only token", name)
			}
		}
		return nil
	}
}

func testAccResourceScimServiceProviderConfig_writeOnly(clientName, endpoint, token, version string) string {
	return testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint,
		fmt.Sprintf("token_wo = %q\n  token_wo_version = %q", token, version))
}

// The write-only token reaches the server, never the state, and survives an
// update that does not send it again: Pocket ID's PUT clears a token it is
// not sent, so the provider must send back the one it holds. Needs Terraform
// or OpenTofu 1.11 or later.
func TestAccResourceScimServiceProvider_writeOnlyToken(t *testing.T) {
	resourceName := "pocketid_scim_service_provider.test"
	clientName := acctest.RandomWithPrefix("tf-acc-scim-wo")
	endpoint := "https://scim.example.com/v2"
	var clientID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{
				Config: testAccResourceScimServiceProviderConfig_writeOnly(clientName, endpoint, "wo-token-1", "1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "token"),
					resource.TestCheckNoResourceAttr(resourceName, "token_wo"),
					resource.TestCheckResourceAttr(resourceName, "token_wo_version", "1"),
					testAccScimCheckServerToken(resourceName, "wo-token-1"),
					testAccScimStateHoldsNo(resourceName, "wo-token-1"),
					func(s *terraform.State) error {
						clientID = s.RootModule().Resources[resourceName].Primary.Attributes["client_id"]
						return nil
					},
				),
			},
			{
				// An unrelated update, same version: the server keeps its token.
				Config: testAccResourceScimServiceProviderConfig_writeOnly(clientName, endpoint+"/updated", "wo-token-1", "1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "endpoint", endpoint+"/updated"),
					testAccScimCheckServerToken(resourceName, "wo-token-1"),
					testAccScimStateHoldsNo(resourceName, "wo-token-1"),
				),
			},
			{
				// A different value under the same version is not sent.
				Config: testAccResourceScimServiceProviderConfig_writeOnly(clientName, endpoint+"/again", "not-sent", "1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "endpoint", endpoint+"/again"),
					testAccScimCheckServerToken(resourceName, "wo-token-1"),
				),
			},
			{
				// A new version sends the value again.
				Config: testAccResourceScimServiceProviderConfig_writeOnly(clientName, endpoint+"/again", "wo-token-2", "2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "token_wo_version", "2"),
					testAccScimCheckServerToken(resourceName, "wo-token-2"),
					testAccScimStateHoldsNo(resourceName, "wo-token-2"),
				),
			},
			{
				// The state holds no token, so a change outside Terraform is
				// not detected; the plan stays empty (documented).
				PreConfig:          func() { testAccScimPutOutside(t, clientID, endpoint+"/again", "changed-outside") },
				Config:             testAccResourceScimServiceProviderConfig_writeOnly(clientName, endpoint+"/again", "wo-token-2", "2"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				// Bumping the version repairs it.
				Config: testAccResourceScimServiceProviderConfig_writeOnly(clientName, endpoint+"/again", "wo-token-2", "3"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccScimCheckServerToken(resourceName, "wo-token-2"),
					testAccScimStateHoldsNo(resourceName, "wo-token-2"),
				),
			},
			{
				// Moving to the plain attribute stores the token again.
				Config: testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint+"/again", `token = "plain-token"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "token", "plain-token"),
					resource.TestCheckNoResourceAttr(resourceName, "token_wo_version"),
					testAccScimCheckServerToken(resourceName, "plain-token"),
				),
			},
			{
				// And back: the plain token leaves the state, the server keeps it.
				Config: testAccResourceScimServiceProviderConfig_writeOnly(clientName, endpoint+"/again", "plain-token", "4"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "token"),
					testAccScimCheckServerToken(resourceName, "plain-token"),
					testAccScimStateHoldsNo(resourceName, "plain-token"),
				),
			},
		},
	})
}

func TestAccResourceScimServiceProvider_writeOnlyConfigurationRules(t *testing.T) {
	clientName := acctest.RandomWithPrefix("tf-acc-scim-wo-rules")
	endpoint := "https://scim.example.com/v2"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{
				Config: testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint,
					"token = \"a\"\n  token_wo = \"b\"\n  token_wo_version = \"1\""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)token.*token_wo|token_wo.*token`),
			},
			{
				Config:      testAccResourceScimServiceProviderConfig_tokenLine(clientName, endpoint, `token_wo = "b"`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`token_wo_version`),
			},
		},
	})
}
