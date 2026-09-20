//go:build acc
// +build acc

package provider_test

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"golang.org/x/mod/semver"
)

// Two public EC keys, written the way a person would: padded, and with members
// in a different order from the one Pocket ID stores after re-encoding them.
const (
	testAccPublicJWK1 = `{ "kid": "acc-key-1", "kty": "EC", "crv": "P-256", "use": "sig", "x": "ScFVPMb2zxk2ZDS5IJu91DBAzf4L7bKikkOXdV6I4_w", "y": "yJAFYZTNNfNKrBFfEnzqepcQkSEfyWOyr0l5U3l5aTM" }`
	testAccPublicJWK2 = `{ "kid": "acc-key-2", "kty": "EC", "crv": "P-256", "x": "m2wurk4shfFqJEUlpHs2GZHmmdhlOueqM-uyDdxjHpc", "y": "D6fLveG4tLP5RE6asPlhYsOYFbvnai3RYjfex0OEXs4" }`
)

func testAccServerAtLeast(version string) bool {
	return semver.Compare("v"+os.Getenv("POCKETID_TEST_VERSION"), "v"+version) >= 0
}

func testAccFederatedClient(identities ...string) string {
	return fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = "fed-settings"
  callback_urls = ["https://example.com/callback"]

  federated_identities = [
%s
  ]
}
`, strings.Join(identities, "\n"))
}

// An omitted replay_protection must never change an identity as a side effect:
// Pocket ID replaces the whole identity list on every client update.
func TestAccResourceClient_federatedReplayProtection(t *testing.T) {
	resourceName := "pocketid_client.test"
	first := func(replay string) string {
		return fmt.Sprintf(`    { issuer = "https://first.example.com", subject = "first"%s },`, replay)
	}
	const inserted = `    { issuer = "https://inserted.example.com", subject = "inserted" },`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccFederatedClient(first(", replay_protection = false")),
				Check:  resource.TestCheckResourceAttr(resourceName, "federated_identities.0.replay_protection", "false"),
			},
			{
				// Omitted: the existing identity keeps false. A new identity placed
				// ahead of it gets the admin UI default and must not inherit by index.
				Config: testAccFederatedClient(inserted, first("")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "federated_identities.#", "2"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.0.issuer", "https://inserted.example.com"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.0.replay_protection", "true"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.1.issuer", "https://first.example.com"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.1.replay_protection", "false"),
				),
			},
			{
				// An unrelated client change leaves both settings alone.
				Config: strings.Replace(testAccFederatedClient(inserted, first("")), `"fed-settings"`, `"fed-settings-renamed"`, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", "fed-settings-renamed"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.0.replay_protection", "true"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.1.replay_protection", "false"),
				),
			},
			{
				Config: testAccFederatedClient(inserted, first(", replay_protection = true")),
				Check:  resource.TestCheckResourceAttr(resourceName, "federated_identities.1.replay_protection", "true"),
			},
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"client_secret"},
			},
		},
	})
}

func TestAccResourceClient_federatedPublicKeys(t *testing.T) {
	resourceName := "pocketid_client.test"
	identity := func(keys ...string) string {
		quoted := make([]string, len(keys))
		for i, key := range keys {
			quoted[i] = fmt.Sprintf("%q", key)
		}
		return fmt.Sprintf(`    { issuer = "https://keys.example.com", subject = "keys", public_keys = [%s] },`, strings.Join(quoted, ", "))
	}

	if !testAccServerAtLeast("2.15.0") {
		// Older servers would drop the keys silently; the provider must refuse
		// before creating anything.
		resource.Test(t, resource.TestCase{
			PreCheck:                 func() { testAccPreCheck(t) },
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      testAccFederatedClient(identity(testAccPublicJWK1)),
				ExpectError: regexp.MustCompile(`requires Pocket ID 2\.15\.0 or\s+later`),
			}},
		})
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The post-apply empty-plan check of each step proves the
				// server's re-encoded keys do not read as drift.
				Config: testAccFederatedClient(identity(testAccPublicJWK1)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "federated_identities.0.public_keys.#", "1"),
					resource.TestCheckNoResourceAttr(resourceName, "federated_identities.0.jwks"),
				),
			},
			{
				// An unrelated change must carry the keys through the update.
				Config: strings.Replace(testAccFederatedClient(identity(testAccPublicJWK1)), `"fed-settings"`, `"fed-keys-renamed"`, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", "fed-keys-renamed"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.0.public_keys.#", "1"),
				),
			},
			{
				Config: testAccFederatedClient(identity(testAccPublicJWK1, testAccPublicJWK2)),
				Check:  resource.TestCheckResourceAttr(resourceName, "federated_identities.0.public_keys.#", "2"),
			},
			{
				// ImportStateVerify compares text, and an import holds the server's
				// encoding of each key rather than the configured one. Compare the
				// keys as JSON instead.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"client_secret",
					"federated_identities.0.public_keys.0",
					"federated_identities.0.public_keys.1",
				},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected one imported resource, got %d", len(states))
					}
					for i, want := range []string{testAccPublicJWK1, testAccPublicJWK2} {
						got := states[0].Attributes[fmt.Sprintf("federated_identities.0.public_keys.%d", i)]
						var wantKey, gotKey map[string]any
						if err := json.Unmarshal([]byte(want), &wantKey); err != nil {
							return err
						}
						if err := json.Unmarshal([]byte(got), &gotKey); err != nil {
							return fmt.Errorf("imported public key %d is not JSON", i)
						}
						if !reflect.DeepEqual(wantKey, gotKey) {
							return fmt.Errorf("imported public key %d differs from the configured key", i)
						}
					}
					return nil
				},
			},
			{
				Config: testAccFederatedClient(`    { issuer = "https://keys.example.com", subject = "keys" },`),
				Check:  resource.TestCheckNoResourceAttr(resourceName, "federated_identities.0.public_keys.#"),
			},
		},
	})
}

// Plan-time rejections need no server support, so they run on every version.
func TestAccResourceClient_federatedPublicKeysRejected(t *testing.T) {
	const privateJWK = `{"kty":"EC","crv":"P-256","kid":"private","x":"ScFVPMb2zxk2ZDS5IJu91DBAzf4L7bKikkOXdV6I4_w","y":"yJAFYZTNNfNKrBFfEnzqepcQkSEfyWOyr0l5U3l5aTM","d":"placeholder-not-a-real-private-value"}`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccFederatedClient(fmt.Sprintf(`    { issuer = "https://keys.example.com", public_keys = [%q] },`, privateJWK)),
				ExpectError: regexp.MustCompile(`private key material`),
			},
			{
				Config:      testAccFederatedClient(fmt.Sprintf(`    { issuer = "https://keys.example.com", jwks = "https://keys.example.com/jwks.json", public_keys = [%q] },`, testAccPublicJWK1)),
				ExpectError: regexp.MustCompile(`(?s)jwks.*public_keys|public_keys.*jwks`),
			},
		},
	})
}
