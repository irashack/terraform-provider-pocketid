package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// ugClaimsMapValue converts a user's or group's custom claims to a map from
// claim key to value. Pocket ID allows each key once per owner. No claims is an
// empty map, not null, so length() and for expressions work on it.
func ugClaimsMapValue(ctx context.Context, claims []client.CustomClaim) (types.Map, diag.Diagnostics) {
	values := make(map[string]string, len(claims))
	for _, claim := range claims {
		values[claim.Key] = claim.Value
	}
	return types.MapValueFrom(ctx, types.StringType, values)
}

// ugIDSetValue converts IDs to a set of strings. No IDs is an empty set, not
// null.
func ugIDSetValue(ctx context.Context, ids []string) (types.Set, diag.Diagnostics) {
	if ids == nil {
		ids = []string{}
	}
	return types.SetValueFrom(ctx, types.StringType, ids)
}

// ugUniqueIDs returns ids without repeats, in order of first appearance. A
// group's members are counted from the result, so the count and the set it
// describes cannot disagree.
func ugUniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}
