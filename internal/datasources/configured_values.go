package datasources

import (
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// lookupValuesOK refuses lookup values from configuration (an ID, a name, an
// e-mail address) that contain the API key this provider authenticates with,
// before any request is made or any diagnostic names them. Requests refuse
// such a value on their own (the client checks every path and query), but the
// diagnostics that explain a failed or empty lookup repeat the value, so it
// is refused first, with fixed text on the attribute. values maps an
// attribute name to its configured value; null and unknown values pass.
func lookupValuesOK(c *client.Client, diags *diag.Diagnostics, values map[string]types.String) bool {
	ok := true
	for attribute, value := range values {
		if value.IsNull() || value.IsUnknown() || !c.ContainsAPIKey(value.ValueString()) {
			continue
		}
		diags.AddAttributeError(path.Root(attribute), "Value not supported",
			"The configured "+attribute+" contains the API key this provider authenticates with, which the provider never sends, stores or prints. The value is not shown, and no request was made.")
		ok = false
	}
	return ok
}
