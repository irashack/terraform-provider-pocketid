package resources

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The framework's element validators (mapvalidator.KeysAre,
// setvalidator.ValueStringsAre and the like) report a failing element at a
// path that names it (a map key, a set element) and repeat its value. A
// configured map key or set element can hold the API key by mistake, and the
// provider never prints that. The validators here check the same rules and
// report them on the collection itself, in fixed text that names no element.

// customClaimsValidator applies Pocket ID's rules for custom claims at plan
// time: a key and a value are required (non-empty) and a key must not be one
// of the names Pocket ID reserves (compared with case, as the server does).
type customClaimsValidator struct{}

func (customClaimsValidator) Description(context.Context) string {
	return "must have no empty custom claim key or value, and no key that is one of the claim names Pocket ID reserves (" + strings.Join(reservedClaimKeys, ", ") + ")"
}

func (v customClaimsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (customClaimsValidator) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	emptyKey, reserved, emptyValue := false, false, false
	for key, value := range req.ConfigValue.Elements() {
		switch {
		case key == "":
			emptyKey = true
		case slices.Contains(reservedClaimKeys, key):
			reserved = true
		}
		if text, ok := value.(types.String); ok && !text.IsNull() && !text.IsUnknown() && text.ValueString() == "" {
			emptyValue = true
		}
	}
	if emptyKey {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid custom claim", "A custom claim key must not be empty.")
	}
	if reserved {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid custom claim",
			"A custom claim key is one of the claim names Pocket ID reserves ("+strings.Join(reservedClaimKeys, ", ")+"); the key is not repeated here.")
	}
	if emptyValue {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid custom claim", "A custom claim value must not be empty; the claim's key is not repeated here.")
	}
}

// uuidSetValidator requires every element of a set to be a UUID.
type uuidSetValidator struct {
	// what names the elements in the message, for example "user IDs".
	what    string
	pattern *regexp.Regexp
}

func (v uuidSetValidator) Description(context.Context) string {
	return "every element must be a UUID (8-4-4-4-12 hexadecimal digits)"
}

func (v uuidSetValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v uuidSetValidator) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, element := range req.ConfigValue.Elements() {
		text, ok := element.(types.String)
		if !ok || text.IsUnknown() {
			continue
		}
		if text.IsNull() || !v.pattern.MatchString(text.ValueString()) {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid "+v.what,
				fmt.Sprintf("Every one of the %s must be a UUID (8-4-4-4-12 hexadecimal digits); one is not. It is not repeated here.", v.what))
			return
		}
	}
}
