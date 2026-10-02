//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccAPI calls the fixture's API directly. Response bodies are decoded
// into out and never printed.
func testAccAPI(method, path string, body, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, os.Getenv("POCKETID_BASE_URL")+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-API-KEY", os.Getenv("POCKETID_API_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s failed", method, path)
	}
	defer func() { _ = response.Body.Close() }()
	if out != nil && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			return response.StatusCode, fmt.Errorf("%s %s returned an unreadable body", method, path)
		}
	}
	return response.StatusCode, nil
}

type testAccSecretMetadata struct {
	ID     string `json:"id"`
	Prefix string `json:"prefix"`
}

func testAccClientSecrets(id string) ([]testAccSecretMetadata, error) {
	var secrets []testAccSecretMetadata
	status, err := testAccAPI("GET", "/api/oidc/clients/"+id+"/secrets", nil, &secrets)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("secret metadata read returned HTTP %d", status)
	}
	return secrets, nil
}

// Pocket ID 2.17.0 generates a secret of its own for a new confidential client
// (autoCreateOidcClientSecret, on by default) and returns it once. The provider
// revokes it, so the client keeps exactly one secret, and it is the one in
// state. Before 2.17.0 the server generates none and the same holds.
func TestAccResourceClient_onlyProviderSecretAfterCreate(t *testing.T) {
	testAccPreCheck(t)
	if testAccServerAtLeast(t, "2.17.0") {
		// Prove the server does create a secret, so this test is not vacuous.
		var created struct {
			ID            string `json:"id"`
			CreatedSecret *struct {
				ID string `json:"id"`
			} `json:"createdSecret"`
		}
		status, err := testAccAPI("POST", "/api/oidc/clients", map[string]any{
			"name": "auto-secret-oracle", "callbackURLs": []string{"https://example.invalid/callback"},
		}, &created)
		if err != nil || status != http.StatusCreated {
			t.Fatalf("direct client creation failed: HTTP %d %v", status, err)
		}
		if created.CreatedSecret == nil || created.CreatedSecret.ID == "" {
			t.Fatalf("Pocket ID did not create a secret for a confidential client; the fixture default changed")
		}
		if status, err := testAccAPI("DELETE", "/api/oidc/clients/"+created.ID, nil, nil); err != nil || status != http.StatusNoContent {
			t.Fatalf("removing the direct client failed: HTTP %d %v", status, err)
		}
	}

	check := func(s *terraform.State) error {
		confidential := s.RootModule().Resources["pocketid_client.confidential"]
		public := s.RootModule().Resources["pocketid_client.public"]
		if confidential == nil || public == nil {
			return fmt.Errorf("missing client state")
		}
		secrets, err := testAccClientSecrets(confidential.Primary.ID)
		if err != nil {
			return err
		}
		if len(secrets) != 1 {
			return fmt.Errorf("expected exactly one secret on the confidential client, got %d", len(secrets))
		}
		value := confidential.Primary.Attributes["client_secret"]
		if secrets[0].Prefix == "" || !strings.HasPrefix(value, secrets[0].Prefix) {
			return fmt.Errorf("the remaining secret (ID %s) is not the one in state", secrets[0].ID)
		}
		secrets, err = testAccClientSecrets(public.Primary.ID)
		if err != nil {
			return err
		}
		if len(secrets) != 0 {
			return fmt.Errorf("expected no secret on the public client, got %d", len(secrets))
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + `
resource "pocketid_client" "confidential" {
  name          = "single-secret"
  callback_urls = ["https://example.invalid/callback"]
}

resource "pocketid_client" "public" {
  name          = "single-secret-public"
  callback_urls = ["https://example.invalid/callback"]
  is_public     = true
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("pocketid_client.confidential", "client_secret"),
					resource.TestCheckNoResourceAttr("pocketid_client.public", "client_secret"),
					check,
				),
			},
		},
	})
}
