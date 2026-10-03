package resources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// userUnresolvedCreationKey names the private-state marker on a pocketid_user
// whose create, with a chosen ID, had an unknown outcome. The ID is kept in
// state so Terraform does not lose track of a user this apply may have
// created, but whether the user with that ID is the one this configuration
// created cannot be known: another creator may have taken the ID between the
// provider's check and its create, or the create may still commit after a
// read found nothing. Comparing the user's fields would not prove ownership
// either. So while the marker is set the provider never deletes, replaces or
// changes that user; an operator resolves it by importing the user, which
// starts with fresh private state, or by removing it from state.
const userUnresolvedCreationKey = "unresolved_creation"

var userUnresolvedCreationValue = []byte(`{"unresolved":true}`)

// userCreationUnresolved reports whether the private state carries the
// marker.
func userCreationUnresolved(ctx context.Context, private interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}) (bool, diag.Diagnostics) {
	value, diags := private.GetKey(ctx, userUnresolvedCreationKey)
	return len(value) > 0, diags
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
// creation is unresolved.
func refuseUnresolvedUser(ctx context.Context, private interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}, id, action string, diags *diag.Diagnostics) bool {
	unresolved, d := userCreationUnresolved(ctx, private)
	diags.Append(d...)
	if !unresolved {
		return d.HasError()
	}
	diags.AddAttributeError(path.Root("id"), "User creation unresolved", userUnresolvedCreationDetail(id, action))
	return true
}

// ModifyPlan refuses, at plan time, to destroy or change a user whose
// creation is unresolved (see userUnresolvedCreationKey); a plan that changes
// nothing is left alone. Delete and Update refuse it again at apply time,
// which also covers a replacement Terraform plans without asking for a
// destroy plan.
func (r *userResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Private == nil || req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	var id string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	action := "change"
	if req.Plan.Raw.IsNull() {
		action = "delete or replace"
	}
	refuseUnresolvedUser(ctx, req.Private, id, action, &resp.Diagnostics)
}
