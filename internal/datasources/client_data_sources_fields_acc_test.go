//go:build acc
// +build acc

package datasources_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// onePixelPNG is a valid 1x1 transparent PNG.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR4nGNgAAIAAAUAAXpeqz8AAAAASUVORK5CYII="

// testAccUploadDarkLogo uploads a dark-mode logo for a client directly.
func testAccUploadDarkLogo(id string) error {
	image, _ := base64.StdEncoding.DecodeString(onePixelPNG)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "logo.png")
	if err != nil {
		return err
	}
	_, _ = part.Write(image)
	_ = form.Close()
	req, err := http.NewRequest("POST", os.Getenv("POCKETID_BASE_URL")+"/api/oidc/clients/"+id+"/logo?light=false", &body)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", os.Getenv("POCKETID_API_TOKEN"))
	req.Header.Set("Content-Type", form.FormDataContentType())
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("logo upload failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		return fmt.Errorf("logo upload returned HTTP %d", response.StatusCode)
	}
	return nil
}

// Both client data sources report the settings the resource manages, the
// client's federated identities, its group restriction and its secrets'
// metadata (never a value). The dark logo is uploaded directly, to show
// both endpoints report has_dark_logo.
func TestAccClientDataSources_allFields(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-dsfields")
	resources := fmt.Sprintf(`
resource "pocketid_group" "a" {
  name          = "%[1]s-a"
  friendly_name = "%[1]s-a"
}

resource "pocketid_client" "test" {
  name                           = %[1]q
  description                    = "described"
  callback_urls                  = ["https://example.invalid/callback"]
  skip_consent                   = true
  access_token_duration_minutes  = 30
  refresh_token_duration_minutes = 600
  allowed_user_groups            = [pocketid_group.a.id]
  requires_pushed_authorization_requests = true

  federated_identities = [
    { issuer = "https://issuer.example.invalid", subject = "workload" },
  ]
}
`, name)
	dataSources := `
data "pocketid_client" "test" {
  id = pocketid_client.test.id
}

data "pocketid_clients" "all" {
  depends_on = [pocketid_client.test]
}
`
	checkSecret := func(s *terraform.State) error {
		managed := s.RootModule().Resources["pocketid_client.test"].Primary.Attributes
		fetched := s.RootModule().Resources["data.pocketid_client.test"].Primary.Attributes
		if fetched["secrets.#"] != "1" {
			return fmt.Errorf("expected one secret, got %s", fetched["secrets.#"])
		}
		if fetched["secrets.0.id"] != managed["client_secret_id"] || !strings.HasPrefix(managed["client_secret"], fetched["secrets.0.prefix"]) || fetched["secrets.0.prefix"] == "" {
			return fmt.Errorf("the listed secret is not the client's")
		}
		for key, value := range fetched {
			if strings.HasPrefix(key, "secrets.") && value == managed["client_secret"] {
				return fmt.Errorf("a secret value reached the data source")
			}
		}
		return nil
	}
	ds := "data.pocketid_client.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: resources,
				Check: func(s *terraform.State) error {
					return testAccUploadDarkLogo(s.RootModule().Resources["pocketid_client.test"].Primary.ID)
				},
			},
			{
				Config: resources + dataSources,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ds, "description", "described"),
					resource.TestCheckResourceAttr(ds, "skip_consent", "true"),
					resource.TestCheckResourceAttr(ds, "access_token_duration_minutes", "30"),
					resource.TestCheckResourceAttr(ds, "refresh_token_duration_minutes", "600"),
					resource.TestCheckResourceAttr(ds, "requires_pushed_authorization_requests", "true"),
					resource.TestCheckResourceAttr(ds, "is_group_restricted", "true"),
					resource.TestCheckResourceAttr(ds, "allowed_user_groups.#", "1"),
					resource.TestCheckTypeSetElemAttrPair(ds, "allowed_user_groups.*", "pocketid_group.a", "id"),
					resource.TestCheckResourceAttr(ds, "client_type", "standard"),
					resource.TestCheckResourceAttr(ds, "has_dark_logo", "true"),
					resource.TestCheckResourceAttr(ds, "has_logo", "false"),
					resource.TestCheckResourceAttr(ds, "pkce_supported", "false"),
					resource.TestCheckResourceAttr(ds, "federated_identities.#", "1"),
					resource.TestCheckResourceAttr(ds, "federated_identities.0.issuer", "https://issuer.example.invalid"),
					resource.TestCheckResourceAttr(ds, "federated_identities.0.subject", "workload"),
					resource.TestCheckResourceAttr(ds, "federated_identities.0.replay_protection", "true"),
					checkSecret,
					testAccCheckListedClient("pocketid_client.test", "description", "described"),
					testAccCheckListedClient("pocketid_client.test", "is_group_restricted", "true"),
					testAccCheckListedClient("pocketid_client.test", "has_dark_logo", "true"),
					testAccCheckListedClient("pocketid_client.test", "client_type", "standard"),
					testAccCheckListedClient("pocketid_client.test", "secrets.#", "1"),
					testAccCheckListedClient("pocketid_client.test", "federated_identities.#", "1"),
					// The managed resource picks the dark logo up on refresh.
					resource.TestCheckResourceAttr("pocketid_client.test", "has_dark_logo", "true"),
				),
			},
		},
	})
}
