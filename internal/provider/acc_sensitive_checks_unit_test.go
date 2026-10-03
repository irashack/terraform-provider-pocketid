//go:build acc
// +build acc

package provider_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
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

// The plan check for a sensitive attribute accepts only an unknown value, and
// when it rejects a plan (a known value, null, missing, the resource absent)
// it says so without the value.
func TestExpectUnknownSensitiveNeverPrintsValues(t *testing.T) {
	const secret = "SYNTHETICsecretVALUE0123456789ab"
	// A plan as `terraform show -json` renders it; the request is decoded
	// from that text so the test needs no direct dependency on terraform-json.
	plan := func(after, afterUnknown string) plancheck.CheckPlanRequest {
		var req plancheck.CheckPlanRequest
		text := `{"Plan":{"format_version":"1.2","resource_changes":[{"address":"r.x","change":{"actions":["create"],"before":null,` +
			`"after":` + after + `,"after_unknown":` + afterUnknown + `}}]}}`
		require.NoError(t, json.Unmarshal([]byte(text), &req))
		return req
	}
	for name, tc := range map[string]struct {
		req     plancheck.CheckPlanRequest
		address string
		want    string // empty: accepted
	}{
		"unknown":               {plan(`{"id":"1"}`, `{"secret":true}`), "r.x", ""},
		"known value":           {plan(`{"secret":"`+secret+`"}`, `{}`), "r.x", `expected "secret" to be unknown`},
		"known short value":     {plan(`{"secret":"`+secret[:4]+`"}`, `{}`), "r.x", `expected "secret" to be unknown`},
		"null":                  {plan(`{"secret":null}`, `{}`), "r.x", `expected "secret" to be unknown`},
		"missing":               {plan(`{"id":"1"}`, `{}`), "r.x", `expected "secret" to be unknown`},
		"marked known by false": {plan(`{"secret":"`+secret+`"}`, `{"secret":false}`), "r.x", `expected "secret" to be unknown`},
		"another resource":      {plan(`{"secret":"`+secret+`"}`, `{}`), "r.y", "r.y: the resource is not in the plan"},
	} {
		t.Run(name, func(t *testing.T) {
			var resp plancheck.CheckPlanResponse
			testAccExpectUnknownSensitive(tc.address, tfjsonpath.New("secret")).CheckPlan(context.Background(), tc.req, &resp)
			if tc.want == "" {
				assert.NoError(t, resp.Error)
				return
			}
			require.Error(t, resp.Error)
			assert.Contains(t, resp.Error.Error(), tc.want)
			assert.Contains(t, resp.Error.Error(), tc.address)
			assert.NotContains(t, resp.Error.Error(), secret)
			assert.NotContains(t, resp.Error.Error(), secret[:8])
		})
	}
}
