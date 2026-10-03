//go:build acc
// +build acc

package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const testAccClientSecretAddr = "pocketid_client_secret.app"

// testAccClientSecretResList reads a client's secrets from the server.
func testAccClientSecretResList(clientID string) (map[string]client.ClientSecretMetadata, error) {
	c, err := testClient()
	if err != nil {
		return nil, err
	}
	secrets, err := c.ListClientSecrets(context.Background(), clientID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]client.ClientSecretMetadata, len(secrets))
	for _, secret := range secrets {
		byID[secret.ID] = secret
	}
	return byID, nil
}

func testAccClientSecretResConfig(name, body string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "app" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
}
%s
`, name, body)
}

func testAccClientSecretResAttrs(s *terraform.State, address string) (map[string]string, error) {
	rs := s.RootModule().Resources[address]
	if rs == nil {
		return nil, fmt.Errorf("%s is not in state", address)
	}
	return rs.Primary.Attributes, nil
}

// testAccClientSecretResCapture records a resource attribute for later steps.
func testAccClientSecretResCapture(address, attribute string, into *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccClientSecretResAttrs(s, address)
		if err != nil {
			return err
		}
		*into = attrs[attribute]
		return nil
	}
}

// testAccClientSecretResOnServer checks the secret in state against the
// server's list, and that every secret in keep is still there.
func testAccClientSecretResOnServer(keep ...*string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccClientSecretResAttrs(s, testAccClientSecretAddr)
		if err != nil {
			return err
		}
		listed, err := testAccClientSecretResList(attrs["client_id"])
		if err != nil {
			return err
		}
		secret, ok := listed[attrs["id"]]
		if !ok {
			return fmt.Errorf("secret %s is not listed on client %s", attrs["id"], attrs["client_id"])
		}
		if err := testAccSameError("the state's prefix of secret "+attrs["id"], secret.Prefix, attrs["prefix"]); err != nil {
			return err
		}
		if fmt.Sprint(secret.IsActive) != attrs["is_active"] {
			return fmt.Errorf("secret %s is listed with is_active %t, state has %s", attrs["id"], secret.IsActive, attrs["is_active"])
		}
		for _, id := range keep {
			if _, ok := listed[*id]; !ok {
				return fmt.Errorf("secret %s, which this resource did not create, is gone", *id)
			}
		}
		return nil
	}
}

func testAccClientSecretResAbsent(clientID, secretID *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		listed, err := testAccClientSecretResList(*clientID)
		if err != nil {
			return err
		}
		if _, ok := listed[*secretID]; ok {
			return fmt.Errorf("secret %s is still listed on client %s", *secretID, *clientID)
		}
		return nil
	}
}

// testAccClientSecretResClientOwn finds the secret pocketid_client holds
// itself (its prefix is the start of client_secret) and records its ID.
func testAccClientSecretResClientOwn(into *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccClientSecretResAttrs(s, "pocketid_client.app")
		if err != nil {
			return err
		}
		listed, err := testAccClientSecretResList(attrs["id"])
		if err != nil {
			return err
		}
		for id, secret := range listed {
			if secret.Prefix != "" && strings.HasPrefix(attrs["client_secret"], secret.Prefix) && id != s.RootModule().Resources[testAccClientSecretAddr].Primary.ID {
				*into = id
				return nil
			}
		}
		return fmt.Errorf("the client's own secret is not listed")
	}
}

func testAccClientSecretResChanged(previous *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccClientSecretResAttrs(s, testAccClientSecretAddr)
		if err != nil {
			return err
		}
		if attrs["id"] == *previous {
			return fmt.Errorf("secret %s was not replaced", *previous)
		}
		return nil
	}
}

func testAccClientSecretResSame(attribute string, previous *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccClientSecretResAttrs(s, testAccClientSecretAddr)
		if err != nil {
			return err
		}
		if attrs[attribute] != *previous {
			return fmt.Errorf("%s changed from %s to %s", attribute, *previous, attrs[attribute])
		}
		return nil
	}
}

// Rotation with create_before_destroy: the new secret is created before the
// old one is revoked; afterwards the old one is gone, the new one listed, and
// the client (with its own secret) untouched. Then import.
func TestAccResourceClientSecret_rotation(t *testing.T) {
	name := "tf-acc-secret-rotation-" + acctest.RandString(6)
	config := func(rotation string) string {
		return testAccClientSecretResConfig(name, fmt.Sprintf(`
resource "terraform_data" "rotation" {
  input = %q
}

resource "pocketid_client_secret" "app" {
  client_id = pocketid_client.app.id

  lifecycle {
    create_before_destroy = true
    replace_triggered_by  = [terraform_data.rotation]
  }
}
`, rotation))
	}
	var clientID, firstID, clientOwn string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(testAccClientSecretAddr, "id", regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)),
					resource.TestCheckResourceAttrPair(testAccClientSecretAddr, "client_id", "pocketid_client.app", "id"),
					testAccCheckSensitiveMatches(testAccClientSecretAddr, "secret", testAccSecretFormat),
					testAccCheckSensitiveMatches(testAccClientSecretAddr, "prefix", testAccPrefixFormat),
					resource.TestCheckResourceAttr(testAccClientSecretAddr, "is_active", "true"),
					resource.TestCheckResourceAttrSet(testAccClientSecretAddr, "created_at"),
					resource.TestCheckNoResourceAttr(testAccClientSecretAddr, "expires_at"),
					func(s *terraform.State) error {
						attrs, _ := testAccClientSecretResAttrs(s, testAccClientSecretAddr)
						if !strings.HasPrefix(attrs["secret"], attrs["prefix"]) {
							return fmt.Errorf("the prefix is not the start of the secret")
						}
						return nil
					},
					testAccClientSecretResCapture("pocketid_client.app", "id", &clientID),
					testAccClientSecretResCapture(testAccClientSecretAddr, "id", &firstID),
					testAccClientSecretResClientOwn(&clientOwn),
					testAccClientSecretResOnServer(&clientOwn),
				),
			},
			{
				Config: config("2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccClientSecretAddr, plancheck.ResourceActionCreateBeforeDestroy),
						plancheck.ExpectResourceAction("pocketid_client.app", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccClientSecretResChanged(&firstID),
					testAccClientSecretResAbsent(&clientID, &firstID),
					testAccClientSecretResOnServer(&clientOwn),
					resource.TestCheckResourceAttrPtr("pocketid_client.app", "id", &clientID),
					func(*terraform.State) error {
						listed, err := testAccClientSecretResList(clientID)
						if err != nil {
							return err
						}
						if len(listed) != 2 {
							return fmt.Errorf("the client holds %d secrets after the rotation, want 2 (its own and the new one)", len(listed))
						}
						return nil
					},
				),
			},
			{
				ResourceName: testAccClientSecretAddr,
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					attrs, err := testAccClientSecretResAttrs(s, testAccClientSecretAddr)
					return attrs["client_id"] + "/" + attrs["id"], err
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret"},
			},
		},
	})
}

// A supplied write-only value is used, never stored, and bumping the version
// rotates the secret; changing only the value plans nothing.
func TestAccResourceClientSecret_writeOnly(t *testing.T) {
	name := "tf-acc-secret-wo-" + acctest.RandString(6)
	values := []string{"one1-acc-supplied-secret-value", "two2-acc-supplied-secret-value", "thr3-acc-supplied-secret-value"}
	config := func(value, version string) string {
		return testAccClientSecretResConfig(name, fmt.Sprintf(`
resource "pocketid_client_secret" "app" {
  client_id         = pocketid_client.app.id
  secret_wo         = %q
  secret_wo_version = %q

  lifecycle {
    create_before_destroy = true
  }
}
`, value, version))
	}
	noValueInState := func(s *terraform.State) error {
		for address, rs := range s.RootModule().Resources {
			for key, attribute := range rs.Primary.Attributes {
				for _, value := range values {
					if strings.Contains(attribute, value) {
						return fmt.Errorf("%s.%s holds a supplied secret value", address, key)
					}
				}
			}
		}
		return nil
	}
	var clientID, firstID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{
				Config: config(values[0], "1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(testAccClientSecretAddr, "secret"),
					resource.TestCheckNoResourceAttr(testAccClientSecretAddr, "secret_wo"),
					resource.TestCheckResourceAttr(testAccClientSecretAddr, "secret_wo_version", "1"),
					testAccCheckSensitiveEquals(testAccClientSecretAddr, "prefix", values[0][:4]),
					noValueInState,
					testAccClientSecretResOnServer(),
					testAccClientSecretResCapture("pocketid_client.app", "id", &clientID),
					testAccClientSecretResCapture(testAccClientSecretAddr, "id", &firstID),
				),
			},
			{
				Config: config(values[1], "2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccClientSecretAddr, plancheck.ResourceActionCreateBeforeDestroy),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckSensitiveEquals(testAccClientSecretAddr, "prefix", values[1][:4]),
					testAccClientSecretResChanged(&firstID),
					testAccClientSecretResAbsent(&clientID, &firstID),
					testAccClientSecretResOnServer(),
					noValueInState,
				),
			},
			{
				// The value alone is not in the plan: nothing changes.
				Config:   config(values[2], "2"),
				PlanOnly: true,
			},
		},
	})
}

// expires_at: the configured spelling is kept, the same time written
// differently is an in-place no-op, another time replaces the secret, and a
// past time is refused at plan.
func TestAccResourceClientSecret_expiry(t *testing.T) {
	name := "tf-acc-secret-expiry-" + acctest.RandString(6)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	offset := at.In(time.FixedZone("", 2*3600)).Format(time.RFC3339)
	zulu := at.UTC().Format(time.RFC3339)
	later := at.Add(time.Hour).UTC().Format(time.RFC3339)
	config := func(expires string) string {
		return testAccClientSecretResConfig(name, fmt.Sprintf(`
resource "pocketid_client_secret" "app" {
  client_id  = pocketid_client.app.id
  expires_at = %q
}
`, expires))
	}
	var secretID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(offset),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccClientSecretAddr, "expires_at", offset),
					resource.TestCheckResourceAttr(testAccClientSecretAddr, "is_active", "true"),
					testAccClientSecretResOnServer(),
					testAccClientSecretResCapture(testAccClientSecretAddr, "id", &secretID),
					func(s *terraform.State) error {
						attrs, _ := testAccClientSecretResAttrs(s, testAccClientSecretAddr)
						listed, err := testAccClientSecretResList(attrs["client_id"])
						if err != nil {
							return err
						}
						if got := listed[attrs["id"]].ExpiresAt; got == nil || !got.Equal(at) {
							return fmt.Errorf("the server holds expiry %v, want %s", got, at)
						}
						return nil
					},
				),
			},
			{
				Config: config(zulu),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccClientSecretAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccClientSecretAddr, "expires_at", zulu),
					testAccClientSecretResSame("id", &secretID),
				),
			},
			{
				Config: config(later),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccClientSecretAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccClientSecretAddr, "expires_at", later),
					testAccClientSecretResChanged(&secretID),
					testAccClientSecretResOnServer(),
				),
			},
			{
				Config:      config("2020-01-01T00:00:00Z"),
				ExpectError: regexp.MustCompile(`expiry is not in the future`),
			},
		},
	})
}

// A secret that expires is reported through is_active and kept: the plan
// stays empty.
func TestAccResourceClientSecret_expiredIsKept(t *testing.T) {
	name := "tf-acc-secret-expired-" + acctest.RandString(6)
	expires := time.Now().Add(25 * time.Second).Truncate(time.Second).UTC()
	config := testAccClientSecretResConfig(name, fmt.Sprintf(`
resource "pocketid_client_secret" "app" {
  client_id  = pocketid_client.app.id
  expires_at = %q
}
`, expires.Format(time.RFC3339)))
	var secretID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccClientSecretAddr, "is_active", "true"),
					testAccClientSecretResCapture(testAccClientSecretAddr, "id", &secretID),
				),
			},
			{
				PreConfig: func() { time.Sleep(time.Until(expires.Add(2 * time.Second))) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccClientSecretAddr, "is_active", "false"),
					testAccClientSecretResSame("id", &secretID),
					testAccClientSecretResOnServer(),
				),
			},
		},
	})
}

// A secret revoked outside Terraform leaves state on refresh and is created
// again.
func TestAccResourceClientSecret_revokedOutOfBand(t *testing.T) {
	name := "tf-acc-secret-oob-" + acctest.RandString(6)
	config := testAccClientSecretResConfig(name, `
resource "pocketid_client_secret" "app" {
  client_id = pocketid_client.app.id
}
`)
	var clientID, secretID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccClientSecretResCapture("pocketid_client.app", "id", &clientID),
					testAccClientSecretResCapture(testAccClientSecretAddr, "id", &secretID),
				),
			},
			{
				PreConfig: func() {
					c, err := testClient()
					require.NoError(t, err)
					require.NoError(t, c.DeleteClientSecret(context.Background(), clientID, secretID))
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccClientSecretAddr, plancheck.ResourceActionCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccClientSecretResChanged(&secretID),
					testAccClientSecretResOnServer(),
				),
			},
		},
	})
}

// Secrets the resource did not create (Pocket ID 2.17's auto-created one, one
// added through the API) survive its create and destroy; at the server's
// limit the resource refuses with a clear message and creates nothing.
func TestAccResourceClientSecret_unmanagedSecretsAndLimit(t *testing.T) {
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)
	created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
		Name: "tf-acc-secret-unmanaged-" + acctest.RandString(6), CallbackURLs: []string{"https://example.invalid/callback"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), created.ID) })
	if testAccServerAtLeast(t, "2.17.0") {
		require.NotNil(t, created.CreatedSecret, "Pocket ID 2.17 creates a secret with a confidential client")
	}
	_, err = c.CreateClientSecret(ctx, created.ID, nil)
	require.NoError(t, err)
	unmanaged, err := testAccClientSecretResList(created.ID)
	require.NoError(t, err)
	require.NotEmpty(t, unmanaged)

	config := testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client_secret" "app" {
  client_id = %q
}
`, created.ID)
	unmanagedKept := func(*terraform.State) error {
		listed, err := testAccClientSecretResList(created.ID)
		if err != nil {
			return err
		}
		for id := range unmanaged {
			if _, ok := listed[id]; !ok {
				return fmt.Errorf("unmanaged secret %s is gone", id)
			}
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.ComposeAggregateTestCheckFunc(testAccClientSecretResOnServer(), unmanagedKept),
			},
			{
				Config: testAccProviderConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(unmanagedKept, func(*terraform.State) error {
					listed, err := testAccClientSecretResList(created.ID)
					if err != nil {
						return err
					}
					if len(listed) != len(unmanaged) {
						return fmt.Errorf("the client holds %d secrets after destroy, want the %d unmanaged ones", len(listed), len(unmanaged))
					}
					return nil
				}),
			},
			{
				PreConfig: func() {
					listed, err := testAccClientSecretResList(created.ID)
					require.NoError(t, err)
					for i := len(listed); i < client.MaxClientSecrets; i++ {
						_, err := c.CreateClientSecret(ctx, created.ID, nil)
						require.NoError(t, err)
					}
				},
				Config:      config,
				ExpectError: regexp.MustCompile(`Client secret limit reached`),
			},
		},
	})
}

func TestAccResourceClientSecret_publicClient(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "app" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
  is_public     = true
}

resource "pocketid_client_secret" "app" {
  client_id = pocketid_client.app.id
}
`, "tf-acc-secret-public-"+acctest.RandString(6)),
				ExpectError: regexp.MustCompile(`Public clients have no secrets`),
			},
		},
	})
}

// A confidential client that holds no secret at all (pocketid_client with
// generate_secret = false, or 2.17's auto-created secret revoked) gets its
// only secret from this resource, and none once it is destroyed.
func TestAccResourceClientSecret_clientWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)
	created, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{
		Name: "tf-acc-secret-none-" + acctest.RandString(6), CallbackURLs: []string{"https://example.invalid/callback"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), created.ID) })
	existing, err := c.ListClientSecrets(ctx, created.ID)
	require.NoError(t, err)
	for _, secret := range existing {
		require.NoError(t, c.RevokeClientSecret(ctx, created.ID, secret.ID))
	}
	count := func(want int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			listed, err := testAccClientSecretResList(created.ID)
			if err != nil {
				return err
			}
			if len(listed) != want {
				return fmt.Errorf("the client holds %d secrets, want %d", len(listed), want)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() { require.NoError(t, count(0)(nil)) },
				Config: testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client_secret" "app" {
  client_id = %q
}
`, created.ID),
				Check: resource.ComposeAggregateTestCheckFunc(testAccClientSecretResOnServer(), count(1)),
			},
			{
				Config: testAccProviderConfig(),
				Check:  count(0),
			},
		},
	})
}

// secret_wo decided only during the apply (a conditional on a value unknown
// while planning): the plan leaves `secret` unknown, and both outcomes
// apply without an inconsistent result.
func TestAccResourceClientSecret_writeOnlyDecidedAtApply(t *testing.T) {
	const supplied = "late-decided-supplied-value-01"
	name := "tf-acc-secret-late-" + acctest.RandString(6)
	config := testAccClientSecretResConfig(name, fmt.Sprintf(`
resource "terraform_data" "generated_mode" {
  input = "generated"
}

resource "terraform_data" "supplied_mode" {
  input = "supplied"
}

resource "pocketid_client_secret" "generated" {
  client_id         = pocketid_client.app.id
  secret_wo         = terraform_data.generated_mode.output == "supplied" ? %[1]q : null
  secret_wo_version = terraform_data.generated_mode.output == "supplied" ? "1" : null
}

resource "pocketid_client_secret" "supplied" {
  client_id         = pocketid_client.app.id
  secret_wo         = terraform_data.supplied_mode.output == "supplied" ? %[1]q : null
  secret_wo_version = terraform_data.supplied_mode.output == "supplied" ? "1" : null
}
`, supplied))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						testAccExpectUnknownSensitive("pocketid_client_secret.generated", tfjsonpath.New("secret")),
						testAccExpectUnknownSensitive("pocketid_client_secret.supplied", tfjsonpath.New("secret")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckSensitiveMatches("pocketid_client_secret.generated", "secret", testAccSecretFormat),
					resource.TestCheckNoResourceAttr("pocketid_client_secret.generated", "secret_wo_version"),
					resource.TestCheckNoResourceAttr("pocketid_client_secret.supplied", "secret"),
					resource.TestCheckResourceAttr("pocketid_client_secret.supplied", "secret_wo_version", "1"),
					testAccCheckSensitiveEquals("pocketid_client_secret.supplied", "prefix", supplied[:4]),
				),
			},
		},
	})
}
