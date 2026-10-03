//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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
