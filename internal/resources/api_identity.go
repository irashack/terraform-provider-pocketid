package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// apiNotShown stands for an identifier that failed its check where a
// diagnostic would otherwise print it.
const apiNotShown = "(an identifier that is not shown)"

// apiIdentityOK checks the identifiers an operation was given by
// configuration, state or an import ID before any request, log line or
// diagnostic uses them: the API ID must be a UUID, a client ID must follow
// Pocket ID's rule for client IDs, and none may contain the API key this
// provider authenticates with (a contaminated state or a mistaken
// configuration must not spread the credential). On failure it adds a fixed
// error that never includes a value and returns false; the caller returns
// without sending anything. source says where the identifier came from, for
// example "configuration" or "state".
func apiIdentityOK(c *client.Client, diags *diag.Diagnostics, source, apiID string, clientIDs ...string) bool {
	failed := c.CheckAPIIdentifier(apiID) != nil
	for _, id := range clientIDs {
		if c.CheckClientIdentifier(id) != nil {
			failed = true
		}
	}
	if !failed {
		return true
	}
	diags.AddError("Unusable identifier",
		fmt.Sprintf("An identifier in the %s is not valid, or contains the API key this provider authenticates with. It is not shown, and no request was made. "+
			"Correct the configuration; for state, run terraform state rm on the resource and import the object again with a valid ID.", source))
	return false
}

// apiShownAPIID returns an API ID for a diagnostic when it passes the check,
// and a fixed placeholder otherwise.
func apiShownAPIID(c *client.Client, id string) string {
	if c.CheckAPIIdentifier(id) != nil {
		return apiNotShown
	}
	return id
}

// apiShownClientID is apiShownAPIID for an OIDC client ID.
func apiShownClientID(c *client.Client, id string) string {
	if c.CheckClientIdentifier(id) != nil {
		return apiNotShown
	}
	return id
}

// apiCredentialTextDiag is the fixed refusal for configured text that
// contains the API key. It names no value.
func apiCredentialTextDiag(diags *diag.Diagnostics) {
	diags.AddError("Value not supported",
		"The name, resource identifier or a permission key, name or description in this configuration contains the API key this provider "+
			"authenticates with. Pocket ID would accept it, but the provider never stores that credential in state or prints it, so it "+
			"would reject the server's answer after the API was created. The value is not shown, and no request was made. Change the value.")
}

// apiModelTexts lists the configured text of an API that is known at this
// point: its name and resource identifier, and each permission's key, name and
// description. Unknown and null values are left out.
func apiModelTexts(ctx context.Context, m apiResourceModel) []string {
	var texts []string
	for _, value := range []types.String{m.Name, m.Resource} {
		if !value.IsNull() && !value.IsUnknown() {
			texts = append(texts, value.ValueString())
		}
	}
	if m.Permissions.IsNull() || m.Permissions.IsUnknown() {
		return texts
	}
	var permissions map[string]apiPermissionModel
	if m.Permissions.ElementsAs(ctx, &permissions, false).HasError() {
		return texts
	}
	for key, p := range permissions {
		texts = append(texts, key)
		for _, value := range []types.String{p.Name, p.Description} {
			if !value.IsNull() && !value.IsUnknown() {
				texts = append(texts, value.ValueString())
			}
		}
	}
	return texts
}

// apiPairOK checks the canonical pair "<api_id>/<client_id>" that becomes a
// grant's ID in state and appears in recovery hints. A key can span the
// separator (a static key of 16 or more characters such as "000000000001/app"
// passes both halves), so the pair is checked as a whole before it is stored
// or shown. On failure it adds a fixed error that never includes a value and
// returns false; nothing is sent. A delete needs only the individually
// checked halves and does not call this.
func apiPairOK(c *client.Client, diags *diag.Diagnostics, apiID, clientID string) bool {
	if !c.ContainsAPIKey(apiID + "/" + clientID) {
		return true
	}
	diags.AddError("Unusable identifier",
		"The combined ID of this grant, <api_id>/<client_id>, contains the API key this provider authenticates with. It would be stored in state and "+
			"shown in messages, so it is refused; it is not shown here, and no request was made. For state, run terraform state rm on the resource "+
			"(terraform destroy -refresh=false still removes the grant).")
	return false
}

// apiShownIDs returns the identities of a grant for a diagnostic: each when
// it passes its check and the pair does too, otherwise placeholders.
func apiShownIDs(c *client.Client, apiID, clientID string) (string, string) {
	shownAPI, shownClient := apiShownAPIID(c, apiID), apiShownClientID(c, clientID)
	if c.ContainsAPIKey(apiID + "/" + clientID) {
		return apiNotShown, apiNotShown
	}
	return shownAPI, shownClient
}

// apiPermissionKeysOK refuses configured permission keys that contain the API
// key before they are sent or printed (a missing key is named in a
// diagnostic): a fixed error, no value, and false.
func apiPermissionKeysOK(c *client.Client, diags *diag.Diagnostics, keys ...[]string) bool {
	for _, list := range keys {
		if c.ContainsAPIKey(list...) {
			diags.AddError("Value not supported",
				"A permission key in this configuration contains the API key this provider authenticates with, which the provider never stores or "+
					"prints. The value is not shown, and no request was made. Change the value.")
			return false
		}
	}
	return true
}
