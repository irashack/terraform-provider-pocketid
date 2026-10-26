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
	// The passwords the state holds before the import, compared with the
	// imported ones without printing either.
	passwords := map[string]string{}
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
					func(s *terraform.State) error {
						attrs := s.RootModule().Resources[resourceName].Primary.Attributes
						for _, name := range []string{"smtp_password", "ldap_bind_password"} {
							passwords[name] = attrs[name]
						}
						return nil
					},
				),
			},
			// ImportState testing
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				// The passwords are compared below, never by
				// ImportStateVerify, whose difference output would print them.
				ImportStateVerifyIgnore: []string{"smtp_password", "ldap_bind_password"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected one imported state, got %d", len(states))
					}
					for _, name := range []string{"smtp_password", "ldap_bind_password"} {
						if err := testAccSameError("the imported "+name, passwords[name], states[0].Attributes[name]); err != nil {
							return err
						}
					}
					return nil
				},
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
	// The messages name the attribute and its rule, never the value.
	invalid := []struct{ attribute, value, message string }{
		{"allow_user_signups", "everyone", `allow_user_signups must be "disabled", "withToken" (with a signup token only) or "open"`},
		{"accent_color", "", `accent_color must be "default" or a CSS color; it must not be empty, because Pocket ID would store its default "default" instead`},
		{"signup_default_custom_claims", `{"department":"it"}`, `signup_default_custom_claims must be a JSON array of objects`},
		{"session_duration", "0", `session_duration must be a whole number of minutes, at least 1`},
		{"smtp_from", "Pocket ID <no-reply@example.com>", `smtp_from must be a plain e-mail address`},
		{"app_name", "", `app_name must be 1 to 30 characters; it must not be empty, because Pocket ID requires a value`},
		{"cimd_url_allowlist", `["https://example(.com/"]`, `cimd_url_allowlist must be a JSON array of URL patterns`},
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

// Pocket ID 2.19.0 replaced the boolean e-mail toggles with modes. On 2.19.0
// or later the mode attributes are read, written and shown by the data
// source, the boolean attributes are null and configuring one is refused at
// plan time; before it the booleans work and the modes are refused. Every
// step is followed by Terraform's own check that the plan is then empty.
func TestAccResourceApplicationConfig_emailModes(t *testing.T) {
	resourceName := "pocketid_application_config.test"
	dataName := "data.pocketid_application_config.test"
	original := testAccAppConfig(t)
	t.Cleanup(func() {
		current := testAccAppConfig(t)
		for _, key := range []string{"emailLoginNotificationEnabled", "emailLoginNotificationMode", "emailVerificationEnabled", "emailVerification"} {
			if value, ok := original[key]; ok {
				current[key] = value
			}
		}
		if status := testAccPutAppConfig(t, current); status != 200 {
			t.Errorf("restoring the e-mail settings returned HTTP %d", status)
		}
	})
	config := func(settings string) string {
		return fmt.Sprintf(`
resource "pocketid_application_config" "test" {
  app_name = "tf-acc-email-modes"
%s}

data "pocketid_application_config" "test" {
  depends_on = [pocketid_application_config.test]
}
`, settings)
	}
	serverValue := func(key, want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := testAccAppConfig(t)[key]; got != want {
				return fmt.Errorf("server %s = %q, want %q", key, got, want)
			}
			return nil
		}
	}
	serverLacks := func(keys ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			current := testAccAppConfig(t)
			for _, key := range keys {
				if _, ok := current[key]; ok {
					return fmt.Errorf("server reports %s", key)
				}
			}
			return nil
		}
	}

	if !testAccServerAtLeast(t, "2.19.0") {
		resource.Test(t, resource.TestCase{
			PreCheck:                 func() { testAccPreCheck(t) },
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: config("  email_login_notification_enabled = \"true\"\n  email_verification_enabled       = \"true\"\n"),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(resourceName, "email_login_notification_enabled", "true"),
						resource.TestCheckResourceAttr(resourceName, "email_verification_enabled", "true"),
						resource.TestCheckResourceAttr(dataName, "email_login_notification_enabled", "true"),
						resource.TestCheckNoResourceAttr(resourceName, "email_login_notification_mode"),
						resource.TestCheckNoResourceAttr(resourceName, "email_verification"),
						resource.TestCheckNoResourceAttr(dataName, "email_verification"),
						serverValue("emailLoginNotificationEnabled", "true"),
						serverValue("emailVerificationEnabled", "true"),
						serverLacks("emailLoginNotificationMode", "emailVerification"),
					),
				},
				{
					Config:      config("  email_login_notification_mode = \"always\"\n"),
					ExpectError: regexp.MustCompile(`email_login_notification_mode\s+requires\s+Pocket\s+ID\s+2\.19\.0\s+or\s+later`),
				},
				{
					Config:      config("  email_verification = \"optional\"\n"),
					ExpectError: regexp.MustCompile(`email_verification\s+requires\s+Pocket\s+ID\s+2\.19\.0\s+or\s+later`),
				},
				{
					Config: config("  email_login_notification_enabled = \"false\"\n  email_verification_enabled       = \"false\"\n"),
					Check: resource.ComposeAggregateTestCheckFunc(
						serverValue("emailLoginNotificationEnabled", "false"),
						serverValue("emailVerificationEnabled", "false"),
					),
				},
			},
		})
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("  email_login_notification_mode = \"ipAndUserAgent\"\n  email_verification            = \"optional\"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email_login_notification_mode", "ipAndUserAgent"),
					resource.TestCheckResourceAttr(resourceName, "email_verification", "optional"),
					resource.TestCheckResourceAttr(dataName, "email_login_notification_mode", "ipAndUserAgent"),
					resource.TestCheckResourceAttr(dataName, "email_verification", "optional"),
					resource.TestCheckNoResourceAttr(resourceName, "email_login_notification_enabled"),
					resource.TestCheckNoResourceAttr(resourceName, "email_verification_enabled"),
					resource.TestCheckNoResourceAttr(dataName, "email_login_notification_enabled"),
					resource.TestCheckNoResourceAttr(dataName, "email_verification_enabled"),
					serverValue("emailLoginNotificationMode", "ipAndUserAgent"),
					serverValue("emailVerification", "optional"),
					serverLacks("emailLoginNotificationEnabled", "emailVerificationEnabled"),
				),
			},
			{
				// Omitted keeps the server's values.
				Config: config(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email_login_notification_mode", "ipAndUserAgent"),
					serverValue("emailVerification", "optional"),
				),
			},
			{
				Config: config("  email_login_notification_mode = \"browserRecognition\"\n  email_verification            = \"disabled\"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					serverValue("emailLoginNotificationMode", "browserRecognition"),
					serverValue("emailVerification", "disabled"),
				),
			},
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"smtp_password", "ldap_bind_password"},
			},
			{
				Config:      config("  email_login_notification_enabled = \"true\"\n"),
				ExpectError: regexp.MustCompile(`email_login_notification_enabled\s+was\s+replaced\s+in\s+Pocket\s+ID\s+2\.19\.0`),
			},
			{
				Config:      config("  email_verification_enabled = \"true\"\n"),
				ExpectError: regexp.MustCompile(`use\s+email_verification\s+instead`),
			},
			{
				Config:      config("  email_verification = \"sometimes\"\n"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`email_verification\s+must\s+be`),
			},
			{
				// Back to Pocket ID's defaults, which other tests expect.
				Config: config("  email_login_notification_mode = \"disabled\"\n  email_verification            = \"disabled\"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					serverValue("emailLoginNotificationMode", "disabled"),
					serverValue("emailVerification", "disabled"),
				),
			},
		},
	})
}

// email_verification "required" needs require_user_email, an SMTP host and
// sender, and emails that are not verified by default (Pocket ID 2.19.0): the
// provider refuses it before any change when the merged configuration lacks
// one, and the server stores it when they are there.
func TestAccResourceApplicationConfig_emailVerificationRequired(t *testing.T) {
	if !testAccServerAtLeast(t, "2.19.0") {
		t.Skip("email_verification needs Pocket ID 2.19.0")
	}
	resourceName := "pocketid_application_config.test"
	original := testAccAppConfig(t)
	t.Cleanup(func() {
		if status := testAccPutAppConfig(t, original); status != 200 {
			t.Errorf("restoring the application configuration returned HTTP %d", status)
		}
	})
	config := func(settings string) string {
		return fmt.Sprintf("resource \"pocketid_application_config\" \"test\" {\n  app_name = \"tf-acc-required\"\n%s}\n", settings)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config("  email_verification = \"required\"\n  emails_verified    = \"true\"\n"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`emails_verified\s+must\s+be\s+"false"`),
			},
			{
				Config:      config("  email_verification = \"required\"\n  emails_verified    = \"false\"\n  smtp_host          = \"\"\n"),
				ExpectError: regexp.MustCompile(`smtp_host\s+must\s+be\s+set`),
			},
			{
				Config: config("  email_verification = \"required\"\n  emails_verified    = \"false\"\n  smtp_host          = \"smtp.example.invalid\"\n  smtp_from          = \"tf-acc@example.invalid\"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email_verification", "required"),
					func(*terraform.State) error {
						if got := testAccAppConfig(t)["emailVerification"]; got != "required" {
							return fmt.Errorf("server emailVerification = %q, want required", got)
						}
						return nil
					},
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
					testAccCheckSensitiveEquals(resourceName, "smtp_password", plain),
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
				// The versions live only in configuration and state. The
				// passwords are checked below, never by ImportStateVerify,
				// whose difference output would print them.
				ImportStateVerifyIgnore: []string{"smtp_password_wo_version", "ldap_bind_password_wo_version", "smtp_password", "ldap_bind_password"},
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
					testAccCheckSensitiveEquals(resourceName, "smtp_password", plain),
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
				ExpectError: regexp.MustCompile(`(?s)smtp_password.*these\s+are\s+not\s+set.*smtp_password_wo`),
			},
			{
				Config: `
resource "pocketid_application_config" "test" {
  ldap_bind_password_wo = "synthetic"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)ldap_bind_password_wo.*also\s+these\s+are\s+set.*ldap_bind_password_wo_version`),
			},
		},
	})
}
