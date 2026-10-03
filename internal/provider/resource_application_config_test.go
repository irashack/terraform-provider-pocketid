//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccResourceApplicationConfig_basic(t *testing.T) {
	resourceName := "pocketid_application_config.test"
	// Pocket-ID enforces appName max length of 30 characters, so keep both the
	// create and update names short and distinct.
	suffix := acctest.RandString(8)
	appName := "tf-acc-" + suffix
	appNameUpdated := "tf-upd-" + suffix

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccResourceApplicationConfigConfig_basic(appName, "60"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "app_name", appName),
					resource.TestCheckResourceAttr(resourceName, "session_duration", "60"),
					resource.TestCheckResourceAttr(resourceName, "id", "application-configuration"),
					// Computed defaults should be populated by the server.
					resource.TestCheckResourceAttrSet(resourceName, "allow_user_signups"),
				),
			},
			// ImportState testing
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Update and Read testing
			{
				Config: testAccResourceApplicationConfigConfig_basic(appNameUpdated, "120"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "app_name", appNameUpdated),
					resource.TestCheckResourceAttr(resourceName, "session_duration", "120"),
				),
			},
		},
	})
}

func TestAccResourceApplicationConfig_dataSource(t *testing.T) {
	// Pocket-ID enforces appName max length of 30 characters.
	appName := "tf-acc-" + acctest.RandString(8)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceApplicationConfigConfig_withDataSource(appName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_application_config.test", "app_name", appName),
					resource.TestCheckResourceAttr("data.pocketid_application_config.test", "app_name", appName),
					resource.TestCheckResourceAttr("data.pocketid_application_config.test", "id", "application-configuration"),
				),
			},
		},
	})
}

func testAccResourceApplicationConfigConfig_basic(appName, sessionDuration string) string {
	return fmt.Sprintf(`
resource "pocketid_application_config" "test" {
  app_name         = %[1]q
  session_duration = %[2]q
}
`, appName, sessionDuration)
}

func testAccResourceApplicationConfigConfig_withDataSource(appName string) string {
	return fmt.Sprintf(`
resource "pocketid_application_config" "test" {
  app_name = %[1]q
}

data "pocketid_application_config" "test" {
  depends_on = [pocketid_application_config.test]
}
`, appName)
}

// Values Pocket ID would refuse, or would store as something else, are
// refused at plan time with the attribute and the rule named.
func TestAccResourceApplicationConfig_planTimeValidation(t *testing.T) {
	invalid := []struct{ attribute, value, message string }{
		{"allow_user_signups", "everyone", `allow_user_signups must be one of "disabled", "withToken" or "open"`},
		{"accent_color", "", `accent_color must not be empty: Pocket ID would store its default "default"`},
		{"signup_default_custom_claims", `{"department":"it"}`, `signup_default_custom_claims must be a JSON array of objects`},
		{"session_duration", "0", `session_duration must be between 1 and`},
		{"smtp_from", "Pocket ID <no-reply@example.com>", `smtp_from must be a plain e-mail address`},
		{"app_name", "", `app_name must not be empty`},
		{"cimd_url_allowlist", `["https://example(.com/"]`, `is not a valid URL pattern`},
	}
	steps := make([]resource.TestStep, 0, len(invalid))
	for _, tc := range invalid {
		steps = append(steps, resource.TestStep{
			Config:   fmt.Sprintf("resource \"pocketid_application_config\" \"test\" {\n  %s = %q\n}\n", tc.attribute, tc.value),
			PlanOnly: true,
			// Terraform wraps long messages: any space may be a line break.
			ExpectError: regexp.MustCompile(strings.ReplaceAll(regexp.QuoteMeta(tc.message), " ", `\s+`)),
		})
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

// auto_create_oidc_client_secret exists from Pocket ID 2.17.0: there it is
// read, written and shown by the data source; an older server reports it as
// null, and configuring it is refused at plan time.
func TestAccResourceApplicationConfig_autoCreateOIDCClientSecret(t *testing.T) {
	resourceName := "pocketid_application_config.test"
	config := func(value string) string {
		setting := ""
		if value != "" {
			setting = fmt.Sprintf("  auto_create_oidc_client_secret = %q\n", value)
		}
		return fmt.Sprintf(`
resource "pocketid_application_config" "test" {
  app_name = "tf-acc-auto-create"
%s}

data "pocketid_application_config" "test" {
  depends_on = [pocketid_application_config.test]
}
`, setting)
	}
	serverValue := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			got, ok := testAccAppConfig(t)["autoCreateOidcClientSecret"]
			if !ok || got != want {
				return fmt.Errorf("server autoCreateOidcClientSecret = %q (reported %t), want %q", got, ok, want)
			}
			return nil
		}
	}

	if !testAccServerAtLeast(t, "2.17.0") {
		resource.Test(t, resource.TestCase{
			PreCheck:                 func() { testAccPreCheck(t) },
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: config(""),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckNoResourceAttr(resourceName, "auto_create_oidc_client_secret"),
						resource.TestCheckNoResourceAttr("data.pocketid_application_config.test", "auto_create_oidc_client_secret"),
					),
				},
				{
					Config:      config("false"),
					ExpectError: regexp.MustCompile(`auto_create_oidc_client_secret requires Pocket ID 2\.17\.0 or later`),
				},
			},
		})
		return
	}

	// Leave Pocket ID's default in place even if a step fails.
	t.Cleanup(func() {
		current := testAccAppConfig(t)
		if current["autoCreateOidcClientSecret"] != "true" {
			current["autoCreateOidcClientSecret"] = "true"
			testAccPutAppConfig(t, current)
		}
	})
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("false"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "auto_create_oidc_client_secret", "false"),
					resource.TestCheckResourceAttr("data.pocketid_application_config.test", "auto_create_oidc_client_secret", "false"),
					serverValue("false"),
				),
			},
			{
				// Omitted keeps the server's value.
				Config: config(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "auto_create_oidc_client_secret", "false"),
					serverValue("false"),
				),
			},
			{
				// Back to Pocket ID's default, which other tests expect.
				Config: config("true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "auto_create_oidc_client_secret", "true"),
					serverValue("true"),
				),
			},
		},
	})
}

// An update of one setting plans every unset setting at its current value
// (not "known after apply"), and keeps a value changed outside Terraform.
func TestAccResourceApplicationConfig_unrelatedUpdate(t *testing.T) {
	resourceName := "pocketid_application_config.test"
	suffix := acctest.RandString(8)
	const outside = "#2a9d8f"
	original := testAccAppConfig(t)
	t.Cleanup(func() {
		current := testAccAppConfig(t)
		current["accentColor"] = original["accentColor"]
		testAccPutAppConfig(t, current)
	})
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceApplicationConfigConfig_basic("tf-acc-"+suffix, "60"),
			},
			{
				PreConfig: func() {
					current := testAccAppConfig(t)
					current["accentColor"] = outside
					if status := testAccPutAppConfig(t, current); status != 200 {
						t.Fatalf("setting the accent color outside Terraform returned HTTP %d", status)
					}
				},
				Config: testAccResourceApplicationConfigConfig_basic("tf-upd-"+suffix, "60"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(resourceName, tfjsonpath.New("accent_color"), knownvalue.StringExact(outside)),
						plancheck.ExpectKnownValue(resourceName, tfjsonpath.New("smtp_tls"), knownvalue.NotNull()),
						plancheck.ExpectKnownValue(resourceName, tfjsonpath.New("ldap_enabled"), knownvalue.NotNull()),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "app_name", "tf-upd-"+suffix),
					resource.TestCheckResourceAttr(resourceName, "accent_color", outside),
					func(*terraform.State) error {
						if got := testAccAppConfig(t)["accentColor"]; got != outside {
							return fmt.Errorf("server accentColor = %q after an unrelated update, want %q", got, outside)
						}
						return nil
					},
				),
			},
		},
	})
}

// The write-only passwords: moving from smtp_password to smtp_password_wo
// keeps the password on the server and removes it from state; an update that
// does not change the version (even with a different write-only value) sends
// the server's password back unchanged; a version change sends the new one;
// import and refresh never put a password into state; the data source has
// none. Every password is checked through the API.
func TestAccResourceApplicationConfig_writeOnlyPasswords(t *testing.T) {
	resourceName := "pocketid_application_config.test"
	suffix := acctest.RandString(8)
	// Synthetic, generated per run.
	plain := "tf-acc-plain-" + acctest.RandString(16)
	ldapPlain := "tf-acc-ldap-plain-" + acctest.RandString(16)
	firstWO := "tf-acc-wo1-" + acctest.RandString(16)
	secondWO := "tf-acc-wo2-" + acctest.RandString(16)
	ldapWO := "tf-acc-ldap-wo-" + acctest.RandString(16)

	plainConfig := func(appName, smtp string) string {
		return fmt.Sprintf(`
resource "pocketid_application_config" "test" {
  app_name           = %q
  smtp_password      = %q
  ldap_bind_password = %q
}
`, appName, smtp, ldapPlain)
	}
	writeOnlyConfig := func(appName, smtp, version string) string {
		return fmt.Sprintf(`
resource "pocketid_application_config" "test" {
  app_name                      = %q
  smtp_password_wo              = %q
  smtp_password_wo_version      = %q
  ldap_bind_password_wo         = %q
  ldap_bind_password_wo_version = "1"
}

data "pocketid_application_config" "test" {
  depends_on = [pocketid_application_config.test]
}
`, appName, smtp, version, ldapWO)
	}
	server := func(smtp, ldap string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			config := testAccAppConfig(t)
			if config["smtpPassword"] != smtp {
				return fmt.Errorf("the server's SMTP password is not the expected one")
			}
			if config["ldapBindPassword"] != ldap {
				return fmt.Errorf("the server's LDAP bind password is not the expected one")
			}
			return nil
		}
	}
	noPasswordsInState := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckNoResourceAttr(resourceName, "smtp_password"),
		resource.TestCheckNoResourceAttr(resourceName, "ldap_bind_password"),
		resource.TestCheckNoResourceAttr(resourceName, "smtp_password_wo"),
		resource.TestCheckNoResourceAttr(resourceName, "ldap_bind_password_wo"),
	)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: plainConfig("tf-acc-"+suffix, plain),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "smtp_password", plain),
					server(plain, ldapPlain),
				),
			},
			{
				// From the plain attributes to the write-only inputs.
				Config: writeOnlyConfig("tf-acc-"+suffix, firstWO, "1"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(resourceName, tfjsonpath.New("smtp_password"), knownvalue.Null()),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					noPasswordsInState,
					resource.TestCheckResourceAttr(resourceName, "smtp_password_wo_version", "1"),
					resource.TestCheckNoResourceAttr("data.pocketid_application_config.test", "smtp_password"),
					resource.TestCheckNoResourceAttr("data.pocketid_application_config.test", "ldap_bind_password"),
					server(firstWO, ldapWO),
				),
			},
			{
				// An unrelated update keeps both passwords.
				Config: writeOnlyConfig("tf-upd-"+suffix, firstWO, "1"),
				Check:  resource.ComposeAggregateTestCheckFunc(noPasswordsInState, server(firstWO, ldapWO)),
			},
			{
				// A different write-only value without a version change is
				// not sent.
				Config: writeOnlyConfig("tf-acc-"+suffix, secondWO, "1"),
				Check:  resource.ComposeAggregateTestCheckFunc(noPasswordsInState, server(firstWO, ldapWO)),
			},
			{
				// The version change sends it.
				Config: writeOnlyConfig("tf-acc-"+suffix, secondWO, "2"),
				Check:  resource.ComposeAggregateTestCheckFunc(noPasswordsInState, server(secondWO, ldapWO)),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				// The versions live only in configuration and state.
				ImportStateVerifyIgnore: []string{"smtp_password_wo_version", "ldap_bind_password_wo_version"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					for _, name := range []string{"smtp_password", "ldap_bind_password"} {
						if _, ok := states[0].Attributes[name]; ok {
							return fmt.Errorf("import stored %s", name)
						}
					}
					return nil
				},
			},
			{
				// Back to the plain attribute.
				Config: plainConfig("tf-acc-"+suffix, plain),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "smtp_password", plain),
					resource.TestCheckNoResourceAttr(resourceName, "smtp_password_wo_version"),
					server(plain, ldapPlain),
				),
			},
		},
	})
}

// smtp_password and smtp_password_wo cannot be set together, and a write-only
// input needs its version.
func TestAccResourceApplicationConfig_writeOnlyValidation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "pocketid_application_config" "test" {
  smtp_password            = "synthetic"
  smtp_password_wo         = "synthetic"
  smtp_password_wo_version = "1"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)smtp_password.*cannot be specified when.*smtp_password_wo`),
			},
			{
				Config: `
resource "pocketid_application_config" "test" {
  ldap_bind_password_wo = "synthetic"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)ldap_bind_password_wo_version.*must be specified`),
			},
		},
	})
}
