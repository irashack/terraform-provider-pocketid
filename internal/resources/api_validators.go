package resources

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// apisMinVersion is the first Pocket ID release whose API endpoints this
// provider uses: per-client grants (PUT and DELETE
// /api/apis/{id}/clients/{clientId}, GET /api/api-access/{clientId}/apis) and
// CIMD access (PUT /api/apis/{id}/cimd-access) were added in v2.14.0, which
// replaced the whole-client grant list of v2.13.0. The routes, DTOs and
// service rules are unchanged from v2.14.0 to v2.17.0.
const apisMinVersion = "2.14.0"

// checkAPISupport refuses, before any mutation, to manage APIs on a server
// older than apisMinVersion.
func checkAPISupport(ctx context.Context, api *client.Client) error {
	supported, err := api.VersionAtLeast(ctx, apisMinVersion)
	if err != nil {
		return fmt.Errorf("could not verify that the server supports APIs: %w; no mutation was attempted", err)
	}
	if !supported {
		return fmt.Errorf("managing APIs requires Pocket ID %s or later; no mutation was attempted", apisMinVersion)
	}
	return nil
}

// apiRuneLengthValidator checks a string's length in Unicode characters, the
// way Pocket ID's min and max bindings count it (stringvalidator counts
// bytes, which would refuse names the server accepts).
type apiRuneLengthValidator struct {
	min, max int
}

func (v apiRuneLengthValidator) Description(context.Context) string {
	if v.min > 0 {
		return fmt.Sprintf("must be %d to %d characters long", v.min, v.max)
	}
	return fmt.Sprintf("must be at most %d characters long", v.max)
}

func (v apiRuneLengthValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v apiRuneLengthValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := utf8.RuneCountInString(req.ConfigValue.ValueString()); n < v.min || n > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid length", fmt.Sprintf("%s %s; got %d.", req.Path, v.Description(ctx), n))
	}
}

// apiResourceValidator applies client.APIResourceProblem.
type apiResourceValidator struct{}

func (apiResourceValidator) Description(context.Context) string {
	return "must be an absolute URI without whitespace, a fragment or a trailing slash, at most 350 characters long"
}

func (v apiResourceValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (apiResourceValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := client.APIResourceProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid API resource identifier", fmt.Sprintf("%s %s.", req.Path, problem))
	}
}

// apiPermissionKeyValidator applies client.APIPermissionKeyProblem. It is
// used on the keys of pocketid_api's permissions map and on the elements of
// pocketid_api_client_access's permission sets.
type apiPermissionKeyValidator struct{}

func (apiPermissionKeyValidator) Description(context.Context) string {
	return "must be a permission key Pocket ID accepts: 1 to 128 OAuth scope characters, not a reserved scope or claim name"
}

func (v apiPermissionKeyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (apiPermissionKeyValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := client.APIPermissionKeyProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid permission key", fmt.Sprintf("Permission key at %s %s.", req.Path, problem))
	}
}

// apiIdentifierValidator checks an ID with one of the client package's
// identifier rules (client.ValidateUUID, client.ValidateClientID), so a
// value that could never be addressed is refused at plan time.
type apiIdentifierValidator struct {
	description string
	check       func(string) error
}

func (v apiIdentifierValidator) Description(context.Context) string { return v.description }

func (v apiIdentifierValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v apiIdentifierValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := v.check(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid identifier", err.Error())
	}
}
