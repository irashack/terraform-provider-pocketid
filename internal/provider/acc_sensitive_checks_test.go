//go:build acc
// +build acc

package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/stretchr/testify/assert"
)

// Acceptance tests handle real credentials the fixture generates. The stock
// assertions print what they compare when they fail (TestCheckResourceAttr
// prints both values, TestMatchResourceAttr the one it got, testify's Equal
// and Len their operands), so a regression that stores or returns a wrong
// value would put a working credential, or the whole value in a field meant
// for its first four characters, into the test log. The checks here report
// only fixed text, lengths and character-class counts. Use them for every
// assertion on a secret, token or password, and for a secret's prefix.

// Formats the fixture's server produces: a generated client secret, and the
// prefix Pocket ID keeps of it.
var (
	testAccSecretFormat = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	testAccPrefixFormat = regexp.MustCompile(`^[A-Za-z0-9]{4}$`)
)

// testAccShape describes a value by its length and character classes only.
func testAccShape(value string) string {
	alphanumeric, other := 0, 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			alphanumeric++
		} else {
			other++
		}
	}
	return fmt.Sprintf("length %d, %d letters or digits, %d other bytes", len(value), alphanumeric, other)
}

// testAccFormatError is nil when value matches the pattern, and otherwise an
// error that names what was checked and the value's shape, never the value.
func testAccFormatError(what, value string, pattern *regexp.Regexp) error {
	if pattern.MatchString(value) {
		return nil
	}
	return fmt.Errorf("%s does not match %s (it has %s)", what, pattern, testAccShape(value))
}

// testAccSameError is nil when the two values are equal, and otherwise an
// error that reports only their shapes.
func testAccSameError(what, want, got string) error {
	if want == got {
		return nil
	}
	return fmt.Errorf("%s is not the expected value (expected %s; got %s)", what, testAccShape(want), testAccShape(got))
}

// testAccCheckSensitiveMatches checks that a state attribute exists and
// matches pattern, without ever printing the value.
func testAccCheckSensitiveMatches(address, attribute string, pattern *regexp.Regexp) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccClientSecretResAttrs(s, address)
		if err != nil {
			return err
		}
		value, ok := attrs[attribute]
		if !ok {
			return fmt.Errorf("%s.%s is not set", address, attribute)
		}
		return testAccFormatError(address+"."+attribute, value, pattern)
	}
}

// testAccCheckSensitiveEquals checks that a state attribute equals want,
// without ever printing either value.
func testAccCheckSensitiveEquals(address, attribute, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		attrs, err := testAccClientSecretResAttrs(s, address)
		if err != nil {
			return err
		}
		value, ok := attrs[attribute]
		if !ok {
			return fmt.Errorf("%s.%s is not set", address, attribute)
		}
		return testAccSameError(address+"."+attribute, want, value)
	}
}

// testAccRequireFormat fails the test, without printing the value, unless
// value matches pattern.
func testAccRequireFormat(t *testing.T, what, value string, pattern *regexp.Regexp) {
	t.Helper()
	if err := testAccFormatError(what, value, pattern); err != nil {
		t.Fatal(err.Error())
	}
}

// testAccAssertSame records a failure, without printing either value, unless
// the two are equal; the test goes on.
func testAccAssertSame(t *testing.T, what, want, got string) {
	t.Helper()
	assert.NoError(t, testAccSameError(what, want, got))
}

// testAccRequireCount fails the test unless a collection has want entries,
// reporting only the two counts: testify's Len and Contains print the whole
// collection, and a list of secrets carries their prefixes.
func testAccRequireCount(t *testing.T, what string, want, got int) {
	t.Helper()
	if want != got {
		t.Fatalf("%s: expected %d, got %d", what, want, got)
	}
}

// expectUnknownSensitive is plancheck.ExpectUnknownValue for an attribute
// that must not be shown: the stock check, when it finds a known value,
// reports it ("Expected unknown value at ..., but found known value: ..."),
// so a plan that carries a credential would print it. This one rejects a
// missing, null and known value alike with one fixed message that names only
// the resource and the attribute path.
type expectUnknownSensitive struct {
	address string
	path    tfjsonpath.Path
}

// testAccExpectUnknownSensitive returns a plan check that the attribute is
// unknown in the planned change of the resource, and never reports its value.
func testAccExpectUnknownSensitive(address string, path tfjsonpath.Path) plancheck.PlanCheck {
	return expectUnknownSensitive{address: address, path: path}
}

func (c expectUnknownSensitive) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	if req.Plan == nil {
		resp.Error = fmt.Errorf("%s: there is no plan to check", c.address)
		return
	}
	for _, change := range req.Plan.ResourceChanges {
		if change == nil || change.Address != c.address {
			continue
		}
		if change.Change != nil {
			if unknown, err := tfjsonpath.Traverse(change.Change.AfterUnknown, c.path); err == nil && unknown == true {
				return
			}
		}
		resp.Error = fmt.Errorf("%s: expected %q to be unknown in the plan, but it is known, null or missing (the value is not shown)", c.address, c.path.String())
		return
	}
	resp.Error = fmt.Errorf("%s: the resource is not in the plan", c.address)
}
