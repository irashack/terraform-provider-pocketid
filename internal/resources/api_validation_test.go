package resources

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apiValidationErrors returns the error diagnostics of a validation, checking
// that none carries text the operator typed (a map key or a set element) in
// its summary, detail or attribute path, and that each is attached to the
// given top-level attribute.
func apiValidationErrors(t *testing.T, diags []*tfprotov6.Diagnostic, typed, root string) []*tfprotov6.Diagnostic {
	t.Helper()
	var errs []*tfprotov6.Diagnostic
	for _, d := range diags {
		if d.Severity != tfprotov6.DiagnosticSeverityError {
			continue
		}
		errs = append(errs, d)
		assert.NotContains(t, d.Summary, typed)
		assert.NotContains(t, d.Detail, typed)
		require.NotNil(t, d.Attribute, "the diagnostic names an attribute")
		assert.NotContains(t, d.Attribute.String(), typed, "the attribute path carries no map key or set element")
		assert.Equal(t, root, d.Attribute.String(), "attached to the collection root, not to an element")
	}
	return errs
}

// Validation messages and attribute paths never carry a map key or a set
// element, which are text the operator typed and can carry a credential. The
// validators run before the provider is configured, so they cannot tell, and
// say nothing about the value. This goes through ValidateResourceConfig, the
// phase that precedes planning.
func TestAPIValidation_NoConfiguredTextInDiagnostics(t *testing.T) {
	const typed = "synthetic-api-key-0123456789"
	_, c := newAPITestPocketIDWithKey(t, "unrelated-key-0123456789")
	h := newAPIHarness(t, c)

	permission := func(name string, description types.String) attr.Value {
		return types.ObjectValueMust(apiPermissionAttrTypes, map[string]attr.Value{
			"id": types.StringNull(), "name": types.StringValue(name), "description": description,
			"allowed_for_cimd_clients": types.BoolNull(),
		})
	}
	apiConfig := func(permissions map[string]attr.Value) apiResourceModel {
		return apiResourceModel{
			ID: types.StringNull(), Name: types.StringValue("Inventory"), Resource: types.StringValue("https://inventory.example"),
			CreatedAt: types.StringNull(), AllowCIMDClients: types.BoolNull(),
			Permissions: types.MapValueMust(types.ObjectType{AttrTypes: apiPermissionAttrTypes}, permissions),
		}
	}

	t.Run("pocketid_api", func(t *testing.T) {
		r := &apiResource{}
		for name, permissions := range map[string]map[string]attr.Value{
			"map key with an invalid character":    {"bad key " + typed: permission("Read", types.StringNull())},
			"valid map key, empty name":            {"scope-" + typed: permission("", types.StringNull())},
			"valid map key, name too long":         {"scope-" + typed: permission(strings.Repeat("n", 51), types.StringNull())},
			"valid map key, description too long":  {"scope-" + typed: permission("Read", types.StringValue(strings.Repeat("d", 201)))},
			"map key too long":                     {typed + strings.Repeat("k", 128): permission("Read", types.StringNull())},
			"reserved key does not repeat the key": {"openid": permission("Read", types.StringNull())},
			"one invalid key among valid ones":     {"ok": permission("Read", types.StringNull()), "bad " + typed: permission("Read", types.StringNull())},
		} {
			t.Run(name, func(t *testing.T) {
				config := apiConfig(permissions)
				diags := h.validate("pocketid_api", apiHarnessValue(t, r, &config))
				errs := apiValidationErrors(t, diags, typed, "AttributeName(\"permissions\")")
				assert.NotEmpty(t, errs, "the configuration is invalid")
			})
		}

		// A top-level value keeps its own attribute.
		config := apiConfig(map[string]attr.Value{})
		config.Name = types.StringValue(strings.Repeat("n", 51))
		diags := h.validate("pocketid_api", apiHarnessValue(t, r, &config))
		errs := apiValidationErrors(t, diags, typed, "AttributeName(\"name\")")
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Detail, "name")
	})

	t.Run("pocketid_api_client_access", func(t *testing.T) {
		r := &apiClientAccessResource{}
		for name, tc := range map[string]struct {
			user, client []string
			root         string
		}{
			"user element with an invalid character":   {[]string{"bad key " + typed}, nil, "AttributeName(\"user_delegated_permissions\")"},
			"client element with an invalid character": {nil, []string{"bad key " + typed}, "AttributeName(\"client_permissions\")"},
			"element too long":                         {[]string{typed + strings.Repeat("k", 128)}, nil, "AttributeName(\"user_delegated_permissions\")"},
		} {
			t.Run(name, func(t *testing.T) {
				config := apiAccessConfig(tc.user, tc.client)
				diags := h.validate("pocketid_api_client_access", apiHarnessValue(t, r, &config))
				errs := apiValidationErrors(t, diags, typed, tc.root)
				assert.NotEmpty(t, errs, "the configuration is invalid")
			})
		}
	})
}
