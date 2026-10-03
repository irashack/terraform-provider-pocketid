package resources

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// clientIDPattern is Pocket ID's rule for a client ID chosen at creation
// (OidcClientCreateDto: binding "client_id,min=2,max=128", validateClientIDRegex
// "^[a-zA-Z0-9._-]+$"), unchanged from v2.0.0 to v2.17.0.
var clientIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,128}$`)

// clientIDValidator applies Pocket ID's rule at plan time. ".." passes the
// server's rule but cannot be used as a path segment, so it is refused too.
type clientIDValidator struct{}

func (clientIDValidator) Description(context.Context) string {
	return "must be 2 to 128 letters, digits, '.', '_' or '-'"
}

func (v clientIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (clientIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if value := req.ConfigValue.ValueString(); !clientIDPattern.MatchString(value) || value == ".." {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid client ID",
			"client_id must be 2 to 128 characters, each a letter, a digit, '.', '_' or '-' (and not \"..\").")
	}
}

// clientIDReplace requires replacement when a configured client_id differs
// from the existing client's ID (the id attribute, which Read keeps equal to
// the server's). Pocket ID ignores an ID sent with an update, so no in-place
// change is possible. Comparing with id rather than with the stored client_id
// also catches state in which an earlier provider recorded an ID the server
// never applied. An omitted client_id never replaces.
type clientIDReplace struct{}

func (clientIDReplace) Description(context.Context) string {
	return "Pocket ID cannot change a client's ID, so a different client_id replaces the client."
}

func (m clientIDReplace) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (clientIDReplace) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if id.IsNull() || id.IsUnknown() {
		return
	}
	if req.ConfigValue.ValueString() != id.ValueString() {
		resp.RequiresReplace = true
	}
}

// clientIDOutOfDate reports state in which an earlier provider version
// recorded a client_id the server never applied (it ignores an ID sent with an
// update), and a configuration that still asks for that value. Refresh sets
// client_id to the real ID, after which clientIDReplace plans the
// replacement. Without a refresh the replacement cannot be planned: the
// stored and configured values are equal, and Terraform and OpenTofu drop a
// replacement request for an attribute whose value does not change.
func clientIDOutOfDate(state, config clientResourceModel) bool {
	if config.ClientID.IsNull() || config.ClientID.IsUnknown() || state.ClientID.IsNull() || state.ClientID.IsUnknown() || state.ID.IsNull() || state.ID.IsUnknown() {
		return false
	}
	return state.ClientID.ValueString() != state.ID.ValueString() && config.ClientID.ValueString() == state.ClientID.ValueString()
}
