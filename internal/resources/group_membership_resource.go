package resources

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &groupMembershipResource{}
	_ resource.ResourceWithConfigure   = &groupMembershipResource{}
	_ resource.ResourceWithImportState = &groupMembershipResource{}
)

func init() { register(NewGroupMembershipResource) }

// NewGroupMembershipResource is a helper function to simplify the provider implementation.
func NewGroupMembershipResource() resource.Resource {
	return &groupMembershipResource{}
}

// groupMembershipResource is the resource implementation.
type groupMembershipResource struct {
	client *client.Client
}

// groupMembershipResourceModel maps the resource schema data.
type groupMembershipResourceModel struct {
	ID      types.String `tfsdk:"id"`
	GroupID types.String `tfsdk:"group_id"`
	UserID  types.String `tfsdk:"user_id"`
	// UnresolvedCreation is true while adding the user was accepted, or may
	// have been, but the result could not be confirmed; null otherwise.
	UnresolvedCreation types.Bool `tfsdk:"unresolved_creation"`
}

// groupMembershipID builds the composite import/state identifier for a
// (group, user) pair.
func groupMembershipID(groupID, userID string) string {
	return groupID + "/" + userID
}

// Metadata returns the resource type name.
func (r *groupMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_membership"
}

// Schema defines the schema for the resource.
func (r *groupMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Adds a single user to a single group in Pocket-ID without taking ownership of the group's other members.",
		MarkdownDescription: "Adds a single user to a single group in Pocket-ID.\n\n" +
			"This resource is deliberately non-authoritative: it manages exactly one `(group_id, user_id)` pair. " +
			"Creating it adds the user to the group without removing any other member; deleting it removes only " +
			"that user, leaving every other member untouched. Use it when a group's membership is partly or " +
			"fully managed outside Terraform (for example, by a self-service onboarding process or an identity " +
			"broker) and Terraform should only ever add or remove specific users, never own the full list.\n\n" +
			"~> **Do not combine with `pocketid_group_members` on the same group** `pocketid_group_members` replaces a group's whole " +
			"member list, so it removes a user this resource added and puts back one this resource removed; the two undo each " +
			"other's changes on every apply. For one group, use either this resource or `pocketid_group_members`.\n\n" +
			"~> **API mechanism** Pocket-ID exposes no endpoint to add or remove a single group member. This " +
			"resource reads the user's current group list, adds or removes the target group, and writes the " +
			"full list back (`PUT /api/users/{id}/user-groups`, the same endpoint `pocketid_user`'s `groups` " +
			"attribute uses). A concurrent writer of the same user's groups — another Terraform apply, or an " +
			"external process — that runs between the read and the write can have its change silently " +
			"overwritten; there is no compare-and-swap primitive that would close this window. Avoid concurrent " +
			"writers of one user's group memberships. Within one provider process this resource, `pocketid_user` and " +
			"`pocketid_group_members` hold one lock around each read, write and verification, so their changes do not " +
			"overwrite each other within an apply. After the write the provider checks the user's groups: " +
			"Pocket ID ignores a group ID that names no group, so a group that does not exist (or is deleted " +
			"during the apply) is an error naming it, never a recorded membership. If adding the user is accepted, or may have been, but the result " +
			"cannot be confirmed, the pair is kept in state as an unresolved creation (`unresolved_creation`), because the request may still take " +
			"effect: a refresh that does not see the user in the group keeps it, with a warning, until a refresh sees the user in the group or the " +
			"resource is removed from state with `terraform state rm`.\n\n" +
			"~> **Do not combine with the `groups` attribute of `pocketid_user` for the same user** That " +
			"attribute is authoritative: every apply of a `pocketid_user` resource replaces the user's entire " +
			"group list with exactly what `groups` contains, including an empty list when `groups` is left " +
			"unset. Managing a user with both a `pocketid_user` resource that sets (or omits) `groups` and one " +
			"or more `pocketid_group_membership` resources is unsupported — the `pocketid_user` apply will " +
			"remove memberships this resource added, on the very next unrelated change. `pocketid_group` does " +
			"not manage membership at all and is always safe to use alongside this resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The resource ID, in the form `<group_id>/<user_id>`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"group_id": schema.StringAttribute{
				Description: "The ID of the group the user is added to. Changing this forces a new resource to be created.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_id": schema.StringAttribute{
				Description: "The ID of the user added to the group. Changing this forces a new resource to be created.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"unresolved_creation": schema.BoolAttribute{
				Description:         groupMembershipUnresolvedDescription,
				MarkdownDescription: groupMembershipUnresolvedDescription,
				Computed:            true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *groupMembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = c
}

// Create creates the resource and sets the initial Terraform state.
func (r *groupMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "configuration", knownAs("user group", plan.GroupID), knownAs("user", plan.UserID)) {
		return
	}

	groupID := plan.GroupID.ValueString()
	userID := plan.UserID.ValueString()
	// A computed value must be known after the apply, whichever way it ends.
	plan.UnresolvedCreation = types.BoolNull()

	tflog.Debug(ctx, "Adding user to group", map[string]any{
		"group_id": groupID,
		"user_id":  userID,
	})

	// Pocket-ID has no add/remove-one-member endpoint: Create and Delete do a
	// read-modify-write of the user's whole group list (see
	// Client.AddUserToGroup and Client.RemoveUserFromGroup), and Terraform
	// applies resources concurrently. The read, the write and its
	// verification therefore run under the lock every resource that writes
	// user-group relations holds (membership_lock.go), so that neither
	// another membership of this user nor pocketid_user or
	// pocketid_group_members can slip a change in between and have it
	// overwritten.
	defer lockMembershipWrites()()

	plan.ID = types.StringValue(groupMembershipID(groupID, userID))
	if err := r.client.AddUserToGroup(ctx, userID, groupID); err != nil {
		var mismatch *client.UserGroupsMismatchError
		// A failure before the write was sent (the read of the user's groups
		// that precedes it) leaves nothing pending, whatever its kind: it is
		// an ordinary error, and no identity is kept.
		attempted := !errors.Is(err, client.ErrWriteNotAttempted)
		switch {
		case attempted && (errors.Is(err, client.ErrResultUnread) || !errors.As(err, &mismatch) && !client.IsDefiniteRejection(err)):
			// The addition was accepted, or may have been, but could not be
			// verified. The pair is kept in state (marked for replacement)
			// as an unresolved creation, so that removing it from the
			// configuration still revokes it: the request can still take
			// effect after a refresh that sees the user outside the group,
			// so a refresh does not drop it (see Read).
			plan.UnresolvedCreation = types.BoolValue(true)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			resp.Diagnostics.AddError("Group membership result uncertain",
				fmt.Sprintf("Adding user %s to group %s may have succeeded, but the result could not be confirmed: %s. "+
					"The membership is kept in state as an unresolved creation, so that Terraform still tracks it: the request may still take effect, "+
					"and a refresh that does not see the user in the group does not remove it. Once a refresh sees the user in the group, the "+
					"condition clears.", userID, groupID, err))
		default:
			// A definite rejection, a group the user is confirmed not to be
			// in (it does not exist), or a failure before anything was
			// sent: nothing to track.
			resp.Diagnostics.AddError(
				"Error adding user to group",
				fmt.Sprintf("Could not add user %s to group %s: %s", userID, groupID, err),
			)
		}
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *groupMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("user group", state.GroupID), knownAs("user", state.UserID)) {
		return
	}

	groupID := state.GroupID.ValueString()
	userID := state.UserID.ValueString()

	exists, err := r.client.UserHasGroupMembership(ctx, userID, groupID)
	if err != nil {
		// Only a positively confirmed missing user (client.IsUserNotFound)
		// means the membership is gone. Any other error - including a
		// generic or malformed 404 from a wrong base URL, a proxy's own
		// not-found page, or an endpoint missing on an older server - must
		// surface as an error rather than silently dropping the resource
		// from state.
		if client.IsUserNotFound(err) {
			if state.UnresolvedCreation.ValueBool() {
				// The addition's outcome is still unknown; the user's
				// absence does not settle it.
				resp.Diagnostics.AddAttributeWarning(path.Root("id"), "Group membership creation still unresolved",
					groupMembershipUnresolvedAbsenceDetail(userID, groupID, "Pocket ID reports no such user"))
				return
			}
			tflog.Debug(ctx, "User no longer exists, removing group membership from state", map[string]any{
				"group_id": groupID,
				"user_id":  userID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading group membership",
			fmt.Sprintf("Could not check whether user %s is a member of group %s: %s", userID, groupID, err),
		)
		return
	}

	if !exists && state.UnresolvedCreation.ValueBool() {
		// An addition whose outcome is unknown may still take effect after
		// this answer, so the old group list does not settle it.
		resp.Diagnostics.AddAttributeWarning(path.Root("id"), "Group membership creation still unresolved",
			groupMembershipUnresolvedAbsenceDetail(userID, groupID, "the user is not in the group"))
		return
	}
	if !exists {
		tflog.Debug(ctx, "User is no longer a member of the group, removing from state", map[string]any{
			"group_id": groupID,
			"user_id":  userID,
		})
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(groupMembershipID(groupID, userID))
	// The user is in the group: an earlier uncertain addition has taken
	// effect, and nothing is unresolved any more.
	state.UnresolvedCreation = types.BoolNull()

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never expected to run: both group_id and user_id force
// replacement, so any change to either recreates the resource instead.
func (r *groupMembershipResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update not supported",
		"group_id and user_id both require replacement; pocketid_group_membership cannot be updated in place.",
	)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *groupMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !knownIdentitiesOK(r.client, &resp.Diagnostics, "state", knownAs("user group", state.GroupID), knownAs("user", state.UserID)) {
		return
	}

	groupID := state.GroupID.ValueString()
	userID := state.UserID.ValueString()

	tflog.Debug(ctx, "Removing user from group", map[string]any{
		"group_id": groupID,
		"user_id":  userID,
	})

	// See Create: the check, the read-modify-write and its verification run
	// under the membership lock.
	defer lockMembershipWrites()()

	// An unresolved addition is only removed when it is seen: a user outside
	// the group (or a missing one) may be a request that has not landed yet,
	// and recording the removal as done would leave it untracked.
	if state.UnresolvedCreation.ValueBool() {
		observed := ""
		exists, err := r.client.UserHasGroupMembership(ctx, userID, groupID)
		switch {
		case err != nil:
			observed = "whether the user is in the group could not be checked (" + err.Error() + ")"
		case !exists:
			observed = "the user is not in the group"
		}
		if observed != "" {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "Group membership creation unresolved",
				groupMembershipUnresolvedRefusal(userID, groupID, observed))
			return
		}
	}

	// RemoveUserFromGroup itself only treats a positively confirmed missing
	// user as "nothing left to remove" (including re-confirming with a GET
	// after a 404 from the update itself, which does not by itself prove the
	// user is gone). Any error it returns here is a real error.
	if err := r.client.RemoveUserFromGroup(ctx, userID, groupID); err != nil {
		resp.Diagnostics.AddError(
			"Error removing user from group",
			fmt.Sprintf("Could not remove user %s from group %s: %s", userID, groupID, err),
		)
		return
	}
}

// ImportState imports an existing resource into Terraform. The import
// identifier is "<group_id>/<user_id>".
func (r *groupMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || r.client.ValidateIdentifier("user group", parts[0]) != nil || r.client.ValidateIdentifier("user", parts[1]) != nil ||
		r.client.ContainsAPIKey(req.ID) {
		// The import ID is not repeated: it may hold the API key by mistake.
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			"Expected an import identifier in the form <group_id>/<user_id>: two UUIDs separated by a slash, neither containing the API key this provider authenticates with.",
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
