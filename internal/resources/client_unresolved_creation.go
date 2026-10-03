package resources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// clientUnresolvedCreationKey names the private-state marker on a
// pocketid_client whose create, with a chosen client_id, had an unknown
// outcome. As for pocketid_user (userUnresolvedCreationKey), the ID is kept so
// Terraform does not lose track of a client this apply may have created, but
// a client found under it may be another actor's, created between the
// provider's absence check and its create. While the condition holds the
// provider never changes, deletes or replaces that client; importing it (a
// fresh state) or `terraform state rm` resolves it. The computed
// unresolved_creation attribute carries the condition into the destroy half of
// a tainted resource's replacement, which has no private state.
const clientUnresolvedCreationKey = "unresolved_creation"

var clientUnresolvedCreationValue = []byte(`{"unresolved":true}`)

// clientUnresolvedCreationDescription documents the unresolved_creation
// attribute of pocketid_client.
const clientUnresolvedCreationDescription = "True while creating this client with a chosen `client_id` has an unknown outcome (the create's answer was lost or unusable), so the client Pocket ID holds under that ID may not be the one this resource created. It is null for every other client. " +
	"While it is set the provider refuses to change, delete or replace the client, and a refresh that finds no client keeps the resource in state with a warning, because the create may still commit. " +
	"It is never cleared by a refresh: check the client in Pocket ID, then run `terraform state rm` on the resource and either import the client (importing clears this) or choose another `client_id`."

// clientCreationUnresolved reports whether the prior state or the private
// state records an unresolved creation.
func clientCreationUnresolved(ctx context.Context, private privateGetter, flag types.Bool) (bool, diag.Diagnostics) {
	if private == nil {
		return flag.ValueBool(), nil
	}
	value, diags := private.GetKey(ctx, clientUnresolvedCreationKey)
	return flag.ValueBool() || len(value) > 0, diags
}

// clientUnresolvedCreationDetail explains what the operator must do.
func clientUnresolvedCreationDetail(id, action string) string {
	return "Creating OIDC client " + id + " had an unknown outcome in an earlier apply, so Terraform cannot know whether the client Pocket ID holds " +
		"under this ID is the one this configuration created: another creator may have used the same ID, or the create may have committed after " +
		"the provider looked. The provider therefore will not " + action + " it. Check the client in Pocket ID. If it is the intended client, run " +
		"`terraform state rm` on this resource and then `terraform import` it with ID " + id + " (or use an import block after removing it from state); " +
		"importing clears this condition. If it is not, run `terraform state rm` on this resource and choose another client_id. Nothing was changed."
}

// refuseUnresolvedClient adds an error and reports true when the client's
// creation is unresolved; flag is the unresolved_creation attribute of the
// prior state.
func refuseUnresolvedClient(ctx context.Context, private privateGetter, flag types.Bool, id, action string, diags *diag.Diagnostics) bool {
	unresolved, d := clientCreationUnresolved(ctx, private, flag)
	diags.Append(d...)
	if !unresolved {
		return d.HasError()
	}
	diags.AddAttributeError(path.Root("client_id"), "OIDC client creation unresolved", clientUnresolvedCreationDetail(id, action))
	return true
}

// keepPriorUnresolved plans unresolved_creation as the prior state holds it,
// null included, so the attribute shows no change on an ordinary update; a
// new resource plans it unknown until Create sets it.
type keepPriorUnresolved struct{}

func (keepPriorUnresolved) Description(context.Context) string {
	return "keeps the prior value, null included, for an existing resource"
}

func (m keepPriorUnresolved) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (keepPriorUnresolved) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}
