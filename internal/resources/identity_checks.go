package resources

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// identity is one identifier an operation was given by configuration, state
// or an import ID, with the kind client.ValidateIdentifier checks it as
// ("user", "user group", "client secret", ..., or "OIDC client").
type identity struct {
	kind string
	id   string
}

// identitiesOK checks the identifiers an operation was given before any
// request, log line or diagnostic uses them: each must have its kind's form
// and none may contain the API key this provider authenticates with, nor may
// the ID they are joined into in state (joined with "/", when there are
// several). A request refuses such an identifier on its own, but the
// diagnostics that explain a failure name the identifiers, so they are
// checked first. Unknown and null identifiers pass (they are checked when
// they are known). On failure it adds a fixed error that names no value and
// returns false; the caller returns without sending anything. source says
// where the identifiers came from: "configuration", "state" or "import ID".
func identitiesOK(c *client.Client, diags *diag.Diagnostics, source string, ids ...identity) bool {
	var joined []string
	for _, id := range ids {
		if c.ValidateIdentifier(id.kind, id.id) != nil {
			identityRefused(diags, source)
			return false
		}
		joined = append(joined, id.id)
	}
	if len(ids) > 1 && c.ContainsAPIKey(strings.Join(joined, "/")) {
		identityRefused(diags, source)
		return false
	}
	return true
}

// known turns a configured or stored identifier into an identity for
// identitiesOK; ok is false when it is null or unknown, so it is skipped.
func known(kind string, value types.String) (identity, bool) {
	if value.IsNull() || value.IsUnknown() {
		return identity{}, false
	}
	return identity{kind: kind, id: value.ValueString()}, true
}

// knownIdentitiesOK is identitiesOK for attribute values, leaving out the
// null and unknown ones (and the joined check with them).
func knownIdentitiesOK(c *client.Client, diags *diag.Diagnostics, source string, values ...func() (identity, bool)) bool {
	var ids []identity
	for _, value := range values {
		if id, ok := value(); ok {
			ids = append(ids, id)
		}
	}
	return identitiesOK(c, diags, source, ids...)
}

func knownAs(kind string, value types.String) func() (identity, bool) {
	return func() (identity, bool) { return known(kind, value) }
}

func identityRefused(diags *diag.Diagnostics, source string) {
	diags.AddError("Unusable identifier",
		fmt.Sprintf("An identifier in the %s is not valid, or contains the API key this provider authenticates with. It is not shown, and no request was made. "+
			"Correct it; for state, run terraform state rm on the resource and import the object again with a valid ID.", source))
}
