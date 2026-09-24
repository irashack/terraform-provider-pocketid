package resources

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// groupMembershipLocks serializes Create and Delete for every
// pocketid_group_membership resource that targets the same user ID.
//
// Pocket-ID has no add/remove-one-member endpoint: Create and Delete both do
// a read-modify-write against PUT /api/users/{id}/user-groups (see
// Client.AddUserToGroup and Client.RemoveUserFromGroup). Terraform applies
// resources concurrently within one apply (parallelism defaults to 10), and
// the framework does not serialize calls across different resource
// instances or even guarantee the same Go value handles them. Two
// unsynchronized read-modify-write cycles for the same user - for example,
// adding that user to five groups in one apply - race: the second PUT can be
// built from a snapshot taken before the first PUT lands, and silently drops
// the first addition. This is exactly the intended use (one user added to
// many groups in a single apply), so it is serialized here rather than left
// as a documented limitation.
//
// The map is keyed by user ID and grows for the life of the provider
// process; entries are never removed. A provider process is short-lived
// (one plan or apply), and the number of distinct users touched in a run is
// bounded by the configuration, so this is not considered a practical leak.
var (
	groupMembershipLocksMu sync.Mutex
	groupMembershipLocks   = map[string]*sync.Mutex{}
)

// lockForUser returns the mutex serializing group-membership mutations for
// userID, creating it on first use.
func lockForUser(userID string) *sync.Mutex {
	groupMembershipLocksMu.Lock()
	defer groupMembershipLocksMu.Unlock()

	m, ok := groupMembershipLocks[userID]
	if !ok {
		m = &sync.Mutex{}
		groupMembershipLocks[userID] = m
	}
	return m
}

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &groupMembershipResource{}
	_ resource.ResourceWithConfigure   = &groupMembershipResource{}
	_ resource.ResourceWithImportState = &groupMembershipResource{}
)

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
			"~> **API mechanism** Pocket-ID exposes no endpoint to add or remove a single group member. This " +
			"resource reads the user's current group list, adds or removes the target group, and writes the " +
			"full list back (`PUT /api/users/{id}/user-groups`, the same endpoint `pocketid_user`'s `groups` " +
			"attribute uses). A concurrent writer of the same user's groups — another Terraform apply, or an " +
			"external process — that runs between the read and the write can have its change silently " +
			"overwritten; there is no compare-and-swap primitive that would close this window. Avoid concurrent " +
			"writers of one user's group memberships.\n\n" +
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

	groupID := plan.GroupID.ValueString()
	userID := plan.UserID.ValueString()

	tflog.Debug(ctx, "Adding user to group", map[string]any{
		"group_id": groupID,
		"user_id":  userID,
	})

	// Serialize the read-modify-write against this user's group list with
	// every other Create/Delete for the same user, so concurrently applying
	// several pocketid_group_membership resources for one user cannot lose
	// an addition or a removal to a race. See groupMembershipLocks.
	lock := lockForUser(userID)
	lock.Lock()
	defer lock.Unlock()

	if err := r.client.AddUserToGroup(userID, groupID); err != nil {
		resp.Diagnostics.AddError(
			"Error adding user to group",
			fmt.Sprintf("Could not add user %s to group %s: %s", userID, groupID, err),
		)
		return
	}

	plan.ID = types.StringValue(groupMembershipID(groupID, userID))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *groupMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	groupID := state.GroupID.ValueString()
	userID := state.UserID.ValueString()

	exists, err := r.client.UserHasGroupMembership(userID, groupID)
	if err != nil {
		// Only a positively confirmed missing user (client.IsUserNotFound)
		// means the membership is gone. Any other error - including a
		// generic or malformed 404 from a wrong base URL, a proxy's own
		// not-found page, or an endpoint missing on an older server - must
		// surface as an error rather than silently dropping the resource
		// from state.
		if client.IsUserNotFound(err) {
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

	if !exists {
		tflog.Debug(ctx, "User is no longer a member of the group, removing from state", map[string]any{
			"group_id": groupID,
			"user_id":  userID,
		})
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(groupMembershipID(groupID, userID))

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

	groupID := state.GroupID.ValueString()
	userID := state.UserID.ValueString()

	tflog.Debug(ctx, "Removing user from group", map[string]any{
		"group_id": groupID,
		"user_id":  userID,
	})

	// See Create: serialize against every other Create/Delete for this user.
	lock := lockForUser(userID)
	lock.Lock()
	defer lock.Unlock()

	// RemoveUserFromGroup itself only treats a positively confirmed missing
	// user as "nothing left to remove" (including re-confirming with a GET
	// after a 404 from the update itself, which does not by itself prove the
	// user is gone). Any error it returns here is a real error.
	if err := r.client.RemoveUserFromGroup(userID, groupID); err != nil {
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
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			fmt.Sprintf("Expected import identifier in the form <group_id>/<user_id>, got: %q", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
