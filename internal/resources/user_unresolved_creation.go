package resources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// userUnresolvedCreationKey names the private-state marker on a pocketid_user
// whose create, with a chosen ID, had an unknown outcome. The ID is kept in
// state so Terraform does not lose track of a user this apply may have
// created, but whether the user with that ID is the one this configuration
// created cannot be known: another creator may have taken the ID between the
// provider's check and its create, or the create may still commit after a
// read found nothing. Comparing the user's fields would not prove ownership
// either. So while the condition holds the provider never deletes, replaces or
// changes that user; an operator resolves it by importing the user, which
// starts with fresh state, or by removing it from state.
//
// The condition is recorded twice. The computed unresolved_creation attribute
// is what protects the user: Terraform taints a resource whose create returned
// an error, and then plans its replacement from a null prior state with no
// private state, so the destroy half of the replacement carries no private
// data, but it does carry the tainted resource's state. The private marker is
// kept as well, for a state that has it.
const userUnresolvedCreationKey = "unresolved_creation"

var userUnresolvedCreationValue = []byte(`{"unresolved":true}`)

// userUnresolvedCreationDescription documents the unresolved_creation
// attribute of pocketid_user.
const userUnresolvedCreationDescription = "True while creating this user with a chosen `id` has an unknown outcome (the create's answer was lost, or a read after it " +
	"could not settle it), so the user Pocket ID holds under that ID may not be the one this resource created. It is null for every other user. " +
	"While it is set the provider refuses to change, delete or replace the user. It is never cleared by a refresh: check the user in Pocket ID, " +
	"then run `terraform state rm` on the resource and either import the user (importing clears this) or choose another `id`."

// userCreationUnresolved reports whether the user's prior state or its private
// state records an unresolved creation.
func userCreationUnresolved(ctx context.Context, private interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}, flag types.Bool) (bool, diag.Diagnostics) {
	value, diags := private.GetKey(ctx, userUnresolvedCreationKey)
	return flag.ValueBool() || len(value) > 0, diags
}

// userUnresolvedCreationDetail explains what the operator must do.
func userUnresolvedCreationDetail(id, action string) string {
	return "Creating user " + id + " had an unknown outcome in an earlier apply, so Terraform cannot know whether the user Pocket ID holds " +
		"under this ID is the one this configuration created: another creator may have used the same ID, or the create may have " +
		"committed after the provider looked. The provider therefore will not " + action + " it. Check the user in Pocket ID. If it is " +
		"the intended user, run `terraform state rm` on this resource and then `terraform import` it with ID " + id + " (or use an import " +
		"block after removing it from state); importing clears this condition. If it is not, run `terraform state rm` on this resource " +
		"and choose another id. Nothing was changed."
}

// refuseUnresolvedUser adds an error and reports true when the user's
// creation is unresolved; flag is the unresolved_creation attribute of the
// prior state.
func refuseUnresolvedUser(ctx context.Context, private interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}, flag types.Bool, id, action string, diags *diag.Diagnostics) bool {
	unresolved, d := userCreationUnresolved(ctx, private, flag)
	diags.Append(d...)
	if !unresolved {
		return d.HasError()
	}
	diags.AddAttributeError(path.Root("id"), "User creation unresolved", userUnresolvedCreationDetail(id, action))
	return true
}

// ModifyPlan refuses, at plan time, to destroy or change a user whose
// creation is unresolved (see userUnresolvedCreationKey); a plan that changes
// nothing is left alone. Terraform plans the replacement of a tainted
// resource from a null prior state, which this cannot see, so Delete and
// Update refuse it again at apply time from the prior state they are given.
func (r *userResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	var id string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	var flag types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(userUnresolvedCreationKey), &flag)...)
	if resp.Diagnostics.HasError() {
		return
	}
	action := "change"
	if req.Plan.Raw.IsNull() {
		action = "delete or replace"
	}
	refuseUnresolvedUser(ctx, req.Private, flag, id, action, &resp.Diagnostics)
}
