package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestCustomClaimsToAPI(t *testing.T) {
	ctx := context.Background()

	t.Run("null map returns empty slice", func(t *testing.T) {
		claims, diags := customClaimsToAPI(ctx, types.MapNull(types.StringType))
		require.False(t, diags.HasError())
		assert.Empty(t, claims)
	})

	t.Run("unknown map returns empty slice", func(t *testing.T) {
		claims, diags := customClaimsToAPI(ctx, types.MapUnknown(types.StringType))
		require.False(t, diags.HasError())
		assert.Empty(t, claims)
	})

	t.Run("populated map returns sorted slice", func(t *testing.T) {
		m, d := types.MapValueFrom(ctx, types.StringType, map[string]string{
			"level":      "senior",
			"department": "engineering",
		})
		require.False(t, d.HasError())

		claims, diags := customClaimsToAPI(ctx, m)
		require.False(t, diags.HasError())
		assert.Equal(t, []client.CustomClaim{
			{Key: "department", Value: "engineering"},
			{Key: "level", Value: "senior"},
		}, claims)
	})
}

func TestCustomClaimsToState(t *testing.T) {
	ctx := context.Background()

	t.Run("empty slice maps to null", func(t *testing.T) {
		m, diags := customClaimsToState(ctx, nil, types.MapNull(types.StringType))
		require.False(t, diags.HasError())
		assert.True(t, m.IsNull())
	})

	t.Run("empty slice keeps an explicit empty map", func(t *testing.T) {
		empty := types.MapValueMust(types.StringType, map[string]attr.Value{})
		m, diags := customClaimsToState(ctx, []client.CustomClaim{}, empty)
		require.False(t, diags.HasError())
		require.False(t, m.IsNull())
		assert.Empty(t, m.Elements())
	})

	t.Run("populated slice maps to map", func(t *testing.T) {
		m, diags := customClaimsToState(ctx, []client.CustomClaim{
			{Key: "department", Value: "engineering"},
			{Key: "level", Value: "senior"},
		}, types.MapNull(types.StringType))
		require.False(t, diags.HasError())
		require.False(t, m.IsNull())

		var values map[string]string
		diags = m.ElementsAs(ctx, &values, false)
		require.False(t, diags.HasError())
		assert.Equal(t, map[string]string{
			"department": "engineering",
			"level":      "senior",
		}, values)
	})

	t.Run("round trip preserves values", func(t *testing.T) {
		original := []client.CustomClaim{
			{Key: "a", Value: "1"},
			{Key: "b", Value: "2"},
		}
		m, diags := customClaimsToState(ctx, original, types.MapNull(types.StringType))
		require.False(t, diags.HasError())

		roundTripped, diags := customClaimsToAPI(ctx, m)
		require.False(t, diags.HasError())
		assert.ElementsMatch(t, original, roundTripped)
	})
}

func TestCheckCustomClaims(t *testing.T) {
	want := []client.CustomClaim{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}
	require.NoError(t, checkCustomClaims(want, []client.CustomClaim{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}}))
	require.NoError(t, checkCustomClaims(nil, nil))
	err := checkCustomClaims(want, []client.CustomClaim{{Key: "a", Value: "zz-held-value"}, {Key: "c", Value: "3"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not stored: b")
	assert.Contains(t, err.Error(), "stored but not requested: c")
	assert.Contains(t, err.Error(), "stored with a different value: a")
	assert.NotContains(t, err.Error(), "zz-held-value", "values are never named")
	// Default claims left on a user whose plan has none.
	require.Error(t, checkCustomClaims(nil, []client.CustomClaim{{Key: "dept", Value: "default"}}))
}

// A claim whose value is null cannot be converted; the error is reported on
// custom_claims without the claim's key.
func TestCustomClaimsToAPI_ConversionNamesNoKey(t *testing.T) {
	claims := types.MapValueMust(types.StringType, map[string]attr.Value{"claim-synthetic-token": types.StringNull()})
	_, diags := customClaimsToAPI(context.Background(), claims)
	require.True(t, diags.HasError())
	for _, d := range diags {
		assert.NotContains(t, d.Summary()+d.Detail(), "synthetic-token")
		withPath, ok := d.(diag.DiagnosticWithPath)
		require.True(t, ok)
		assert.Equal(t, "custom_claims", withPath.Path().String())
	}
}
