package resources

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/valuefree"
)

// customClaimsToAPI converts a Terraform map of custom claims into the API's
// list representation. A null or unknown map yields an empty slice so the API
// performs a full replace that clears any existing claims.
func customClaimsToAPI(ctx context.Context, claims types.Map) ([]client.CustomClaim, diag.Diagnostics) {
	var diags diag.Diagnostics
	if claims.IsNull() || claims.IsUnknown() {
		return []client.CustomClaim{}, diags
	}

	values := make(map[string]string, len(claims.Elements()))
	// The conversion's diagnostics name the claim's key: only a value-free
	// form of them is returned.
	diags = valuefree.Conversion(path.Root("custom_claims"), claims.ElementsAs(ctx, &values, false))
	if diags.HasError() {
		return nil, diags
	}

	// Sort keys for deterministic request ordering.
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]client.CustomClaim, 0, len(keys))
	for _, key := range keys {
		result = append(result, client.CustomClaim{Key: key, Value: values[key]})
	}
	return result, diags
}

// customClaimsToState converts the API's list of custom claims into a
// Terraform map. No claims take the representation of like, the configured or
// prior value: null stays null and an explicit empty map stays empty, so
// neither "custom_claims = {}" nor an omitted attribute shows a difference
// when the object has no claims.
func customClaimsToState(ctx context.Context, claims []client.CustomClaim, like types.Map) (types.Map, diag.Diagnostics) {
	if len(claims) == 0 {
		if like.IsNull() || like.IsUnknown() {
			return types.MapNull(types.StringType), diag.Diagnostics{}
		}
		return types.MapValueMust(types.StringType, map[string]attr.Value{}), diag.Diagnostics{}
	}

	values := make(map[string]string, len(claims))
	for _, claim := range claims {
		values[claim.Key] = claim.Value
	}
	return types.MapValueFrom(ctx, types.StringType, values)
}

// checkCustomClaims compares the claims Pocket ID holds after a replacement
// with the ones requested. Pocket ID stores keys and values in Unicode NFC
// form, so a request in another form comes back different; an error names
// the keys that differ (never the values).
func checkCustomClaims(want, held []client.CustomClaim) error {
	wanted := make(map[string]string, len(want))
	for _, claim := range want {
		wanted[claim.Key] = claim.Value
	}
	have := make(map[string]string, len(held))
	for _, claim := range held {
		have[claim.Key] = claim.Value
	}
	var missing, unexpected, changed []string
	for key, value := range wanted {
		got, ok := have[key]
		switch {
		case !ok:
			missing = append(missing, key)
		case got != value:
			changed = append(changed, key)
		}
	}
	for key := range have {
		if _, ok := wanted[key]; !ok {
			unexpected = append(unexpected, key)
		}
	}
	if len(missing)+len(unexpected)+len(changed) == 0 {
		return nil
	}
	var parts []string
	for _, group := range []struct {
		label string
		keys  []string
	}{{"not stored", missing}, {"stored but not requested", unexpected}, {"stored with a different value", changed}} {
		if len(group.keys) > 0 {
			sort.Strings(group.keys)
			parts = append(parts, fmt.Sprintf("%s: %s", group.label, strings.Join(group.keys, ", ")))
		}
	}
	return fmt.Errorf("the custom claims Pocket ID holds differ from the requested ones (%s); it stores keys and values in Unicode NFC form", strings.Join(parts, "; "))
}
