//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func testAccBackchannelClient(name, attributes string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
%s
}
`, name, attributes)
}

// testAccServerBackchannelURL reads the client's back-channel logout URL
// directly from the API.
func testAccServerBackchannelURL(id string) (string, error) {
	var client struct {
		BackchannelLogoutURL string `json:"backchannelLogoutURL"`
	}
	status, err := testAccAPI("GET", "/api/oidc/clients/"+id, nil, &client)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("client read returned HTTP %d", status)
	}
	return client.BackchannelLogoutURL, nil
}

func testAccCheckServerBackchannelURL(resourceName, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := s.RootModule().Resources[resourceName]
		if rs == nil {
			return fmt.Errorf("missing %s in state", resourceName)
		}
		got, err := testAccServerBackchannelURL(rs.Primary.ID)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("server backchannelLogoutURL is %q, want %q", got, want)
		}
		return nil
	}
}

func TestAccResourceClient_backchannelLogoutURL(t *testing.T) {
	resourceName := "pocketid_client.test"
	const first = "https://rp.example.invalid/backchannel-logout"
	// http is allowed for a confidential client; the query is kept verbatim.
	const second = "http://rp.example.invalid:8080/logout?tenant=acc"

	if !testAccServerAtLeast(t, "2.17.0") {
		// An older server would drop the URL and every later plan would show
		// it again; the provider must refuse before creating anything.
		resource.Test(t, resource.TestCase{
			PreCheck:                 func() { testAccPreCheck(t) },
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config:      testAccBackchannelClient("backchannel-old-server", fmt.Sprintf("  backchannel_logout_url = %q", first)),
					ExpectError: regexp.MustCompile(`requires\s+Pocket\s+ID\s+2\.17\.0\s+or\s+later`),
				},
				{
					// Without the attribute nothing changes on an older server.
					Config: testAccBackchannelClient("backchannel-old-server", ""),
					Check:  resource.TestCheckNoResourceAttr(resourceName, "backchannel_logout_url"),
				},
			},
		})
		return
	}

	var clientID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Each step's automatic follow-up plan must be empty, which
				// proves the stored value reads back unchanged.
				Config: testAccBackchannelClient("backchannel", fmt.Sprintf("  backchannel_logout_url = %q", first)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "backchannel_logout_url", first),
					testAccCheckServerBackchannelURL(resourceName, first),
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						clientID = v
						return nil
					}),
				),
			},
			{
				Config: testAccBackchannelClient("backchannel", fmt.Sprintf("  backchannel_logout_url = %q", second)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "backchannel_logout_url", second),
					testAccCheckServerBackchannelURL(resourceName, second),
				),
			},
			{
				// An unrelated change keeps the URL.
				Config: testAccBackchannelClient("backchannel-renamed", fmt.Sprintf("  backchannel_logout_url = %q", second)),
				Check:  testAccCheckServerBackchannelURL(resourceName, second),
			},
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"client_secret"},
			},
			{
				// Removing the attribute clears the URL on the server.
				Config: testAccBackchannelClient("backchannel-renamed", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "backchannel_logout_url"),
					testAccCheckServerBackchannelURL(resourceName, ""),
				),
			},
			{
				// The attribute is authoritative: a URL set outside Terraform
				// shows as a change on the next refreshed plan.
				PreConfig: func() {
					var body map[string]any
					status, err := testAccAPI("GET", "/api/oidc/clients/"+clientID, nil, &body)
					if err != nil || status != http.StatusOK {
						t.Fatalf("client read: HTTP %d %v", status, err)
					}
					body["backchannelLogoutURL"] = "https://outside.example.invalid/logout"
					if status, err := testAccAPI("PUT", "/api/oidc/clients/"+clientID, body, nil); err != nil || status != http.StatusOK {
						t.Fatalf("setting the URL outside Terraform failed: HTTP %d %v", status, err)
					}
				},
				Config:             testAccBackchannelClient("backchannel-renamed", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: testAccBackchannelClient("backchannel-renamed", ""),
				Check:  testAccCheckServerBackchannelURL(resourceName, ""),
			},
		},
	})
}

// Pocket ID requires https for a public client; the provider says so at plan
// time instead of failing the API call.
func TestAccResourceClient_backchannelLogoutURLValidation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccBackchannelClient("backchannel-public-http", "  is_public = true\n  backchannel_logout_url = \"http://rp.example.invalid/logout\""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must\s+use\s+https\s+for\s+a\s+public\s+client`),
			},
			{
				Config:      testAccBackchannelClient("backchannel-fragment", "  backchannel_logout_url = \"https://rp.example.invalid/logout#frag\""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must\s+not\s+contain\s+a\s+fragment`),
			},
			{
				Config:      testAccBackchannelClient("backchannel-relative", "  backchannel_logout_url = \"/logout\""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must\s+be\s+an\s+absolute\s+http\s+or\s+https\s+URL`),
			},
		},
	})
}
