package resources

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"

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
