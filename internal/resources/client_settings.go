package resources

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// The range Pocket ID accepts for a client's token lifetimes
// (model.IsValidTokenDurationMinutes, binding "token_duration", 2.13.0 to
// 2.17.0): one minute to one year.
const (
	clientTokenMinutesMin = 1
	clientTokenMinutesMax = 365 * 24 * 60
)

// optionalMinutes maps a token lifetime; 0 means the server did not report it.
func optionalMinutes(minutes int64) types.Int64 {
	if minutes == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(minutes)
}

// fillComputedFromServer sets every attribute still unknown after a create or
// update from the client the server returned. Known planned values are kept:
// the apply must record what the plan showed, and a value the server changed
// since the last refresh appears on the next one.
func fillComputedFromServer(plan *clientResourceModel, api *client.OIDCClient) {
	if plan.HasLogo.IsUnknown() {
		plan.HasLogo = types.BoolValue(api.HasLogo)
	}
	if plan.HasDarkLogo.IsUnknown() {
		plan.HasDarkLogo = types.BoolValue(api.HasDarkLogo)
	}
	if plan.ClientType.IsUnknown() {
		plan.ClientType = optionalString(api.ClientType)
	}
	if plan.PkceSupported.IsUnknown() {
		plan.PkceSupported = types.BoolValue(api.PkceSupported)
	}
	if plan.LaunchURL.IsUnknown() {
		plan.LaunchURL = optionalString(api.LaunchURL)
	}
	if plan.Description.IsUnknown() {
		plan.Description = types.StringValue(api.Description)
	}
	if plan.SkipConsent.IsUnknown() {
		plan.SkipConsent = types.BoolValue(api.SkipConsent)
	}
	if plan.AccessTokenDurationMinutes.IsUnknown() {
		plan.AccessTokenDurationMinutes = optionalMinutes(api.AccessTokenDurationMinutes)
	}
	if plan.RefreshTokenDurationMinutes.IsUnknown() {
		plan.RefreshTokenDurationMinutes = optionalMinutes(api.RefreshTokenDurationMinutes)
	}
}

// planPkceSupported plans pkce_supported for a client that is changing (its
// planned value is then unknown): an update sending pkce_enabled = false
// resets it (updateOIDCClientModelFromDto), otherwise it keeps its state
// value. On create it stays unknown.
func planPkceSupported(state *clientResourceModel, plan *clientResourceModel) {
	if !plan.PkceSupported.IsUnknown() || state == nil || plan.PkceEnabled.IsUnknown() {
		return
	}
	if !plan.PkceEnabled.ValueBool() {
		plan.PkceSupported = types.BoolValue(false)
		return
	}
	if !state.PkceSupported.IsNull() {
		plan.PkceSupported = state.PkceSupported
	}
}
