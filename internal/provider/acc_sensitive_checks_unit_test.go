//go:build acc
// +build acc

package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The checks used on credentials fail without printing what they compared,
// whether the value is too long, in the wrong character class, or a whole
// secret where a four-character prefix belongs.
func TestSensitiveChecksNeverPrintValues(t *testing.T) {
	const secret = "SYNTHETICsecretVALUE0123456789ab"
	state := func(attrs map[string]string) *terraform.State {
		return &terraform.State{Modules: []*terraform.ModuleState{{
			Path:      []string{"root"},
			Resources: map[string]*terraform.ResourceState{"r.x": {Primary: &terraform.InstanceState{Attributes: attrs}}},
		}}}
	}
	for name, tc := range map[string]struct {
		check func(*terraform.State) error
		attrs map[string]string
		want  string
	}{
		"secret too long":         {testAccCheckSensitiveMatches("r.x", "secret", testAccSecretFormat), map[string]string{"secret": secret + "!"}, "length 33"},
		"secret in a wrong class": {testAccCheckSensitiveMatches("r.x", "secret", testAccSecretFormat), map[string]string{"secret": secret[:31] + "-"}, "1 other bytes"},
		"whole value as prefix":   {testAccCheckSensitiveMatches("r.x", "prefix", testAccPrefixFormat), map[string]string{"prefix": secret}, "length 32"},
		"prefix not equal":        {testAccCheckSensitiveEquals("r.x", "prefix", secret[:4]), map[string]string{"prefix": secret}, "expected length 4"},
		"token not equal":         {testAccCheckSensitiveEquals("r.x", "token", secret), map[string]string{"token": secret[:31]}, "length 31"},
		"attribute missing":       {testAccCheckSensitiveEquals("r.x", "token", secret), map[string]string{}, "r.x.token is not set"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.check(state(tc.attrs))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			for _, value := range []string{secret, secret[:31], secret + "!", secret[:31] + "-"} {
				assert.NotContains(t, err.Error(), value, "the error repeats a value")
			}
		})
	}
	assert.NoError(t, testAccFormatError("x", "abcdEFGH0123abcdEFGH0123abcdEFGH", testAccSecretFormat))
	assert.NoError(t, testAccSameError("x", secret, secret))
}
