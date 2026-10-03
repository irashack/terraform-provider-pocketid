package valuefree

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lengthRule struct{}

func (lengthRule) Description(context.Context) string { return "must be 1 to 50 characters long" }

// A diagnostic below a map key or set element is reported on the collection,
// with the attribute names that follow the element in its sentence and the
// key never shown.
func TestRewriteKeepsStaticChildNames(t *testing.T) {
	const key = "zzSyntheticAdminKeyMarker0123"
	for _, tc := range []struct {
		name   string
		at     path.Path
		safe   string
		detail string
	}{
		{"nested attribute under a map key", path.Root("permissions").AtMapKey(key).AtName("name"), "permissions",
			"Attribute name in permissions must be 1 to 50 characters long. The configured value is not shown."},
		{"deeper nesting", path.Root("a").AtListIndex(0).AtName("b").AtSetValue(types.StringValue(key)).AtName("c").AtName("d"), "a[0].b",
			"Attribute c.d in a[0].b must be 1 to 50 characters long. The configured value is not shown."},
		{"the map value itself", path.Root("custom_claims").AtMapKey(key), "custom_claims",
			"Attribute custom_claims must be 1 to 50 characters long. The configured value is not shown."},
		{"no element", path.Root("name"), "name",
			"Attribute name must be 1 to 50 characters long. The configured value is not shown."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reported diag.Diagnostics
			reported.AddAttributeError(tc.at, "Invalid length", "quotes "+key)
			out := rewrite(context.Background(), tc.at, lengthRule{}, reported)
			require.Len(t, out, 1)
			got, ok := out[0].(diag.DiagnosticWithPath)
			require.True(t, ok)
			assert.Equal(t, tc.safe, got.Path().String())
			assert.Equal(t, tc.detail, out[0].Detail())
			assert.NotContains(t, out[0].Detail()+got.Path().String(), key)
		})
	}
}
