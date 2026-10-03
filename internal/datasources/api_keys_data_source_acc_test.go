//go:build acc
// +build acc

package datasources_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/stretchr/testify/require"
)

// apiKeysCheckExample is the documented example with the key name and the
// margin of the fixture, so the test runs the text users copy.
func apiKeysCheckExample(t *testing.T, margin, timestampFunction string) string {
	t.Helper()
	raw, err := os.ReadFile("../../examples/data-sources/pocketid_api_keys/data-source.tf")
	require.NoError(t, err)
	example := string(raw)
	require.Contains(t, example, `"336h"`)
	require.Contains(t, example, "plantimestamp()")
	example = strings.ReplaceAll(example, `"336h"`, fmt.Sprintf("%q", margin))
	example = strings.ReplaceAll(example, "plantimestamp()", timestampFunction)
	example = strings.ReplaceAll(example, `"terraform"`, `"provider-fixture"`)
	example = strings.ReplaceAll(example, `\"terraform\"`, `\"provider-fixture\"`)
	return "provider \"pocketid\" {}\n\n" + example
}

// expectCheck asserts the result of a check block in the plan itself, which is
// where a user who only runs `plan` sees it. A check whose condition is
// unknown while planning reports "unknown" and says nothing until an apply.
type expectCheck struct {
	name   string
	status string
	// message, when set, must be among the problems of a failed check.
	message string
}

func (e expectCheck) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, check := range req.Plan.Checks {
		if check.Address.Name != e.name {
			continue
		}
		if string(check.Status) != e.status {
			resp.Error = fmt.Errorf("check %s has status %q in the plan, want %q", e.name, check.Status, e.status)
			return
		}
		if e.message != "" {
			for _, instance := range check.Instances {
				for _, problem := range instance.Problems {
					if strings.Contains(problem.Message, e.message) {
						return
					}
				}
			}
			resp.Error = fmt.Errorf("check %s has no problem containing %q", e.name, e.message)
		}
		return
	}
	resp.Error = fmt.Errorf("the plan has no result for check %s", e.name)
}

// The fixture's own key (named provider-fixture, valid for a day, never given
// a description) is the only key its owner has. The data source lists it with
// its times and no key value.
func TestAccAPIKeysDataSource_listsTheProvidersOwnKey(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "pocketid" {}

data "pocketid_api_keys" "mine" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.pocketid_api_keys.mine", "keys.#", "1"),
				resource.TestCheckResourceAttr("data.pocketid_api_keys.mine", "keys.0.name", "provider-fixture"),
				resource.TestCheckResourceAttrSet("data.pocketid_api_keys.mine", "keys.0.id"),
				resource.TestCheckResourceAttrSet("data.pocketid_api_keys.mine", "keys.0.expires_at"),
				resource.TestCheckResourceAttrSet("data.pocketid_api_keys.mine", "keys.0.created_at"),
				// The request that read the list used the key.
				resource.TestCheckResourceAttrSet("data.pocketid_api_keys.mine", "keys.0.last_used_at"),
				resource.TestCheckNoResourceAttr("data.pocketid_api_keys.mine", "keys.0.description"),
				resource.TestCheckNoResourceAttr("data.pocketid_api_keys.mine", "keys.0.key"),
			),
		}},
	})
}

// The documented check block gives its verdict at plan time: it passes for a
// key that outlasts the margin and fails, as a warning, for one that does not
// (the fixture's key is valid for about a day). With timestamp() instead of
// plantimestamp() the verdict is unknown during planning, which is why the
// example does not use it.
func TestAccAPIKeysDataSource_checkWarnsAtPlanTime(t *testing.T) {
	const checkName = "pocketid_management_key_expiry"
	step := func(margin, timestampFunction string, expected expectCheck) resource.TestStep {
		return resource.TestStep{
			Config: apiKeysCheckExample(t, margin, timestampFunction),
			// A data source scoped to a check block is always planned as a
			// read during apply, so the plan is never empty.
			ExpectNonEmptyPlan: true,
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{expected},
			},
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			step("12h", "plantimestamp()", expectCheck{name: checkName, status: "pass"}),
			step("48h", "plantimestamp()", expectCheck{name: checkName, status: "fail", message: "expires within two weeks"}),
			step("48h", "timestamp()", expectCheck{name: checkName, status: "unknown"}),
		},
	})
}
