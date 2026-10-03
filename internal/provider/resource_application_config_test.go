//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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
	}
	steps := make([]resource.TestStep, 0, len(invalid))
	for _, tc := range invalid {
		steps = append(steps, resource.TestStep{
			Config:      fmt.Sprintf("resource \"pocketid_application_config\" \"test\" {\n  %s = %q\n}\n", tc.attribute, tc.value),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(regexp.QuoteMeta(tc.message)),
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
