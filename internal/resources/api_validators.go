package resources

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

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

// apiRootPath is the attribute a validation diagnostic is attached to: the
// top-level attribute, never the map key or set element below it. Those are
// text the operator typed, which can carry a credential, and a diagnostic's
// attribute path is shown to the operator and logged.
func apiRootPath(p path.Path) path.Path {
	if steps := p.Steps(); len(steps) > 0 {
		if name, ok := steps[0].(path.PathStepAttributeName); ok {
			return path.Root(string(name))
		}
	}
	return p
}

// apiValidatedLabel names the value a message is about without any dynamic
// path step: "name" for a top-level attribute, "name in permissions" for the
// name inside a permission, and the collection itself for a map key or a set
// element.
func apiValidatedLabel(p path.Path) string {
	steps := p.Steps()
	root := apiRootPath(p).String()
	if len(steps) > 1 {
		if last, ok := steps[len(steps)-1].(path.PathStepAttributeName); ok {
			return fmt.Sprintf("%s in %s", string(last), root)
		}
	}
	return root
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
		resp.Diagnostics.AddAttributeError(apiRootPath(req.Path), "Invalid length", fmt.Sprintf("The %s %s; got %d.", apiValidatedLabel(req.Path), v.Description(ctx), n))
	}
}

// apiResourceValidator applies client.APIResourceProblem.
type apiResourceValidator struct{}

func (apiResourceValidator) Description(context.Context) string {
	return "must be an absolute URI without whitespace or a fragment, at most 350 characters long, and must not end with a slash (Pocket ID removes trailing slashes)"
}

func (v apiResourceValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (apiResourceValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := client.APIResourceProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(apiRootPath(req.Path), "Invalid API resource identifier", fmt.Sprintf("The %s %s.", apiValidatedLabel(req.Path), problem))
	}
}

// apiPermissionKeyValidator applies client.APIPermissionKeyProblem. It is
// used on the keys of pocketid_api's permissions map and on the elements of
// pocketid_api_client_access's permission sets.
type apiPermissionKeyValidator struct{}

func (apiPermissionKeyValidator) Description(context.Context) string {
	return "must be a permission key Pocket ID accepts: 1 to 128 characters valid in an OAuth scope (printable ASCII other than space, '\"' and '\\'), and not one of the scope or claim names reserved by Pocket ID"
}

func (v apiPermissionKeyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (apiPermissionKeyValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := client.APIPermissionKeyProblem(req.ConfigValue.ValueString()); problem != "" {
		// Neither the key nor its path is shown: it is a map key or a set element.
		resp.Diagnostics.AddAttributeError(apiRootPath(req.Path), "Invalid permission key", fmt.Sprintf("A permission key in %s %s.", apiRootPath(req.Path), problem))
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
		resp.Diagnostics.AddAttributeError(apiRootPath(req.Path), "Invalid identifier", err.Error())
	}
}

// apiPermissionsNameRequired requires a name in every permission of the
// map. It stands in for Required on the nested attribute, whose framework
// check would name the configured key.
type apiPermissionsNameRequired struct{}

func (apiPermissionsNameRequired) Description(context.Context) string {
	return "requires name in every permission"
}

func (v apiPermissionsNameRequired) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (apiPermissionsNameRequired) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	for _, element := range apiPermissionObjects(req.ConfigValue) {
		if name, ok := element["name"]; ok && name.IsNull() {
			resp.Diagnostics.AddAttributeError(req.Path, "Missing permission name", "Every permission in "+req.Path.String()+" requires a name.")
			return
		}
	}
}

// apiPermissionsIDNotConfigured refuses a configured permission ID, which
// Pocket ID assigns. It stands in for the nested attribute being read-only,
// whose framework check would name the configured key.
type apiPermissionsIDNotConfigured struct{}

func (apiPermissionsIDNotConfigured) Description(context.Context) string {
	return "takes no id in a permission: Pocket ID assigns it"
}

func (v apiPermissionsIDNotConfigured) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (apiPermissionsIDNotConfigured) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	for _, element := range apiPermissionObjects(req.ConfigValue) {
		if id, ok := element["id"]; ok && !id.IsNull() {
			resp.Diagnostics.AddAttributeError(req.Path, "Permission ID set", "A permission in "+req.Path.String()+" sets id, which Pocket ID assigns.")
			return
		}
	}
}

// apiPermissionObjects returns the attributes of every known permission in
// a configured map; nothing for a null or unknown map.
func apiPermissionObjects(m types.Map) []map[string]attr.Value {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	var out []map[string]attr.Value
	for _, element := range m.Elements() {
		object, ok := element.(types.Object)
		if !ok || object.IsNull() || object.IsUnknown() {
			continue
		}
		out = append(out, object.Attributes())
	}
	return out
}
