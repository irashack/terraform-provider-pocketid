package resources

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// backchannelLogoutMinVersion is the first Pocket ID release that stores an
// OIDC Back-Channel Logout URL on a client.
const backchannelLogoutMinVersion = "2.17.0"

// backchannelLogoutURLProblem applies Pocket ID's rules for a back-channel
// logout URL (OidcClientUpdateDto: http_url plus backchannel_logout_url): an
// absolute http or https URL with a host and no fragment, and https when the
// client is public. It returns "" when the value is acceptable. An empty
// string is refused as well: Pocket ID stores it as "no URL", which reads back
// as null, so the attribute is omitted instead.
func backchannelLogoutURLProblem(value string, isPublic bool) string {
	if value == "" {
		return "must not be empty; omit backchannel_logout_url to have no back-channel logout URL"
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "must be an absolute http or https URL with a host"
	}
	if strings.Contains(value, "#") {
		return "must not contain a fragment"
	}
	if isPublic && u.Scheme != "https" {
		return "must use https for a public client (is_public = true)"
	}
	return ""
}

// backchannelLogoutURLValidator checks the URL alone; the https requirement
// for a public client also needs is_public and is checked in ValidateConfig.
type backchannelLogoutURLValidator struct{}

func (backchannelLogoutURLValidator) Description(context.Context) string {
	return "must be an absolute http or https URL without a fragment"
}

func (v backchannelLogoutURLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (backchannelLogoutURLValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := backchannelLogoutURLProblem(req.ConfigValue.ValueString(), false); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid back-channel logout URL", "backchannel_logout_url "+problem+".")
	}
}

// checkBackchannelLogoutSupport refuses, before any mutation, to send a
// back-channel logout URL to a server that would silently drop it: the next
// plan would show the same change again, forever.
func checkBackchannelLogoutSupport(api *client.Client, value *string) error {
	if value == nil {
		return nil
	}
	supported, err := api.VersionAtLeast(backchannelLogoutMinVersion)
	if err != nil {
		return fmt.Errorf("could not verify that the server supports backchannel_logout_url: %w", err)
	}
	if !supported {
		return fmt.Errorf("backchannel_logout_url requires Pocket ID %s or later; no mutation was attempted", backchannelLogoutMinVersion)
	}
	return nil
}

// stringPointer returns nil for a null, unknown or empty value.
func stringPointer(value types.String) *string {
	if value.IsNull() || value.IsUnknown() || value.ValueString() == "" {
		return nil
	}
	v := value.ValueString()
	return &v
}

// backchannelLogoutURLForUpdate decides what an update sends. Pocket ID
// replaces the client in full, so whatever is sent (or omitted, which clears
// it) becomes the server's value.
//
// When the plan changes the attribute, the planned value is sent. When the
// plan shows no change, the value the server holds right now is sent back,
// even if state has not seen it: a URL set outside Terraform and planned
// without a refresh (or state written before the attribute existed) is kept
// rather than cleared by an update that did not show that change. The next
// refreshed plan shows it. A server before 2.17.0 reports no URL, so nothing
// is sent to it.
func backchannelLogoutURLForUpdate(planned, prior types.String, current string) *string {
	if planned.Equal(prior) {
		if current == "" {
			return nil
		}
		return &current
	}
	return stringPointer(planned)
}
