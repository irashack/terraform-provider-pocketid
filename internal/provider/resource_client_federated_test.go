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
	// An RSA key carrying "alg", to prove re-encoding keeps optional members.
	testAccPublicJWK3 = `{ "kid": "acc-key-3", "kty": "RSA", "alg": "RS256", "use": "sig", "e": "AQAB", "n": "sIPHqPy041TwYYbGzl5NlxnztjKPOU_4ebbrgnzymmwHsgpY2akPR_v7GBXq2yPWAfROPtUr8toGjkRH_ziqyKlCgRYsE7SeLeloHeqT3bam-rzdnaOfufBwC_ucXZHHcNbxG0eZ5gjtH6kCMG196clznrDp6VpoXruIfFYqJ2SiKI5DqnN7MFHKXyv9H4sIT12CGd2U5YuD45FxcdKChVfX4Zg8k2J2ajNJD2XqCIsAczTfGmoSi8sSTcrd32_mUEP2U5pywiipx9F6lGibJXEKLrZQje6oyGV4yPOgAZzkUooHC2uzum__bbGfHw-04OrrFrWCHj-mYcZvu8X20Q" }`
	testAccPublicJWK2 = `{ "kid": "acc-key-2", "kty": "EC", "crv": "P-256", "x": "m2wurk4shfFqJEUlpHs2GZHmmdhlOueqM-uyDdxjHpc", "y": "D6fLveG4tLP5RE6asPlhYsOYFbvnai3RYjfex0OEXs4" }`
)

// testAccServerAtLeast selects version-specific assertions. A missing or
// malformed POCKETID_TEST_VERSION fails the test: comparing it would read as
// "older" and silently skip the newer server's checks.
func testAccServerAtLeast(t *testing.T, version string) bool {
	t.Helper()
	running := "v" + os.Getenv("POCKETID_TEST_VERSION")
	if !semver.IsValid(running) {
		t.Fatalf("POCKETID_TEST_VERSION must be the fixture's Pocket ID version, got %q", os.Getenv("POCKETID_TEST_VERSION"))
	}
	return semver.Compare(running, "v"+version) >= 0
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

	if !testAccServerAtLeast(t, "2.15.0") {
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
				Config: testAccFederatedClient(identity(testAccPublicJWK1, testAccPublicJWK3)),
				Check:  resource.TestCheckResourceAttr(resourceName, "federated_identities.0.public_keys.#", "2"),
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
			{
				Config:      testAccFederatedClient(fmt.Sprintf(`    { issuer = "https://keys.example.com", public_keys = [%q, %q] },`, testAccPublicJWK1, testAccPublicJWK1)),
				ExpectError: regexp.MustCompile(`Duplicate federated identity public key ID`),
			},
		},
	})
}

// A value that is configured but not known until apply must be left to the
// configuration. Planning a default over it contradicts the final plan.
func TestAccResourceClient_federatedReplayProtectionKnownAfterApply(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "terraform_data" "flag" {
  input = false
}
` + testAccFederatedClient(`    { issuer = "https://deferred.example.com", subject = "deferred", replay_protection = terraform_data.flag.output },`),
			Check: resource.TestCheckResourceAttr("pocketid_client.test", "federated_identities.0.replay_protection", "false"),
		}},
	})
}

// Pocket ID accepts identities that share issuer, subject and audience. Each
// must keep its own setting when the configuration omits it.
func TestAccResourceClient_federatedReplayProtectionDuplicateIdentities(t *testing.T) {
	resourceName := "pocketid_client.test"
	identity := func(jwks, replay string) string {
		return fmt.Sprintf(`    { issuer = "https://twin.example.com", subject = "twin", jwks = "https://twin.example.com/%s.json"%s },`, jwks, replay)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccFederatedClient(identity("a", ", replay_protection = false"), identity("b", ", replay_protection = true")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "federated_identities.0.replay_protection", "false"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.1.replay_protection", "true"),
				),
			},
			{
				Config: strings.Replace(testAccFederatedClient(identity("a", ""), identity("b", "")), `"fed-settings"`, `"fed-twins-renamed"`, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", "fed-twins-renamed"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.0.replay_protection", "false"),
					resource.TestCheckResourceAttr(resourceName, "federated_identities.1.replay_protection", "true"),
				),
			},
		},
	})
}

// A null element must be refused at plan time on every server version: dropped
// silently it would shrink the list after the mutation and skip the version gate.
func TestAccResourceClient_federatedPublicKeysNullElement(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccFederatedClient(fmt.Sprintf(`    { issuer = "https://keys.example.com", public_keys = [%q, null] },`, testAccPublicJWK1)),
				ExpectError: regexp.MustCompile(`must not be null`),
			},
			{
				Config:      testAccFederatedClient(`    { issuer = "https://keys.example.com", public_keys = [null] },`),
				ExpectError: regexp.MustCompile(`must not be null`),
			},
		},
	})
}
