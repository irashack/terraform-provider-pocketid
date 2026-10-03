package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A configured map key or set element can hold the API key by mistake. The
// collection validators report a failing element on the collection, in text
// that names no element, so neither the diagnostic's path nor its text can
// repeat it.
func TestCollectionValidatorsNameNoElement(t *testing.T) {
	const marker = "synthetic-credential-0123456789"
	ctx := context.Background()
	root := path.Root("custom_claims")
	checkDiags := func(t *testing.T, diagsText []string, paths []path.Path, want path.Path) {
		t.Helper()
		require.NotEmpty(t, diagsText)
		for _, text := range diagsText {
			assert.NotContains(t, text, marker)
		}
		for _, p := range paths {
			assert.True(t, p.Equal(want), "the diagnostic is on %s, not %s", want, p)
			assert.NotContains(t, p.String(), marker)
		}
	}

	for name, claims := range map[string]map[string]attr.Value{
		"reserved key":              {"email": types.StringValue(marker)},
		"empty value under a key":   {marker: types.StringValue("")},
		"empty key":                 {"": types.StringValue("x")},
		"reserved key, empty value": {"sub": types.StringValue(""), marker: types.StringValue("ok")},
	} {
		t.Run("custom claims: "+name, func(t *testing.T) {
			req := validator.MapRequest{Path: root, ConfigValue: types.MapValueMust(types.StringType, claims)}
			resp := &validator.MapResponse{}
			customClaimsValidator{}.ValidateMap(ctx, req, resp)
			var texts []string
			var paths []path.Path
			for _, d := range resp.Diagnostics.Errors() {
				texts = append(texts, d.Summary()+d.Detail())
				if withPath, ok := d.(interface{ Path() path.Path }); ok {
					paths = append(paths, withPath.Path())
				}
			}
			checkDiags(t, texts, paths, root)
		})
	}
	t.Run("custom claims: valid", func(t *testing.T) {
		resp := &validator.MapResponse{}
		customClaimsValidator{}.ValidateMap(ctx, validator.MapRequest{Path: root, ConfigValue: types.MapValueMust(types.StringType, map[string]attr.Value{
			"department": types.StringValue("x"), "Email": types.StringValue("case differs from the reserved name"),
		})}, resp)
		assert.False(t, resp.Diagnostics.HasError())
	})

	set := path.Root("user_ids")
	v := uuidSetValidator{what: "user IDs", pattern: groupMembersUUIDPattern}
	resp := &validator.SetResponse{}
	v.ValidateSet(ctx, validator.SetRequest{Path: set, ConfigValue: types.SetValueMust(types.StringType, []attr.Value{
		types.StringValue("00000000-0000-4000-8000-000000000001"), types.StringValue(marker),
	})}, resp)
	var texts []string
	var paths []path.Path
	for _, d := range resp.Diagnostics.Errors() {
		texts = append(texts, d.Summary()+d.Detail())
		if withPath, ok := d.(interface{ Path() path.Path }); ok {
			paths = append(paths, withPath.Path())
		}
	}
	checkDiags(t, texts, paths, set)

	resp = &validator.SetResponse{}
	v.ValidateSet(ctx, validator.SetRequest{Path: set, ConfigValue: types.SetValueMust(types.StringType, []attr.Value{
		types.StringValue("00000000-0000-4000-8000-000000000001"),
	})}, resp)
	assert.False(t, resp.Diagnostics.HasError())
}
