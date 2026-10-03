package resources

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &groupMembersResource{}
	_ resource.ResourceWithConfigure   = &groupMembersResource{}
	_ resource.ResourceWithImportState = &groupMembersResource{}
)

func init() { register(NewGroupMembersResource) }

// NewGroupMembersResource creates the resource that owns one group's whole
// membership.
func NewGroupMembersResource() resource.Resource {
	return &groupMembersResource{}
}

type groupMembersResource struct {
	client *client.Client
}

type groupMembersResourceModel struct {
	ID      types.String `tfsdk:"id"`
	GroupID types.String `tfsdk:"group_id"`
	UserIDs types.Set    `tfsdk:"user_ids"`
}

var groupMembersUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Metadata returns the resource type name.
func (r *groupMembersResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_members"
}

// Schema defines the schema for the resource.
func (r *groupMembersResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Owns the whole membership of one Pocket-ID group: the users in `user_ids` are exactly the group's members.",
		MarkdownDescription: "Owns the whole membership of one Pocket-ID group: the users in `user_ids` are exactly the group's members. " +
			"Use it when Terraform should be the only writer of who is in the group; use `pocketid_group_membership` to add single users to a group that others also change.\n\n" +
			"The group is written with one request (`PUT /api/user-groups/{id}/users`) that replaces the membership, so a plan that shows the new set is the whole change. " +
			"A refresh reads the group's actual members, so a user added or removed outside Terraform shows up as a difference.\n\n" +
			"~> **Do not combine with other writers of the same group's membership** For one group, do not use this resource together with `pocketid_group_membership`, " +
			"with the `groups` attribute of `pocketid_user` on any user who is, or should be, a member, or with a second `pocketid_group_members`. " +
			"Each of those replaces or edits the same membership list, and they undo each other's changes on every apply.\n\n" +
			"~> **Adopting a group that already has members** Creating this resource refuses to remove members that `user_ids` does not list, because a plan for a new resource cannot show them. " +
			"Import the group first (`terraform import pocketid_group_members.<name> <group_id>`), so the plan shows each member that would be removed, or list every current member.\n\n" +
			"~> **Removing members can end sessions (Pocket ID 2.17)** When a user stops being a member, Pocket ID 2.17 can sign that user out of group-restricted OIDC clients that have a back-channel logout URL, if the group was what let them in. " +
			"Removing a user from `user_ids` and destroying this resource both remove members.\n\n" +
			"~> **Concurrent changes by others** Pocket ID can only replace a group's whole member list. Before every write the provider reads the group's members: create and update refuse to remove a member that the plan did not show, and destroy keeps every member that is not in `user_ids`. " +
			"Those checks cover what the group held at that read only. A user added by something else (the Pocket ID admin interface, another Terraform run, an onboarding service) after the read and before the write, an instant later, cannot be protected: the write removes them, no check can prevent it, and the provider cannot tell afterwards that it happened. " +
			"Within one Terraform run the provider serializes every write of user-group relations, so this resource, `pocketid_group_membership` and `pocketid_user` do not overwrite each other.\n\n" +
			"~> **LDAP groups** A group synchronized from LDAP gets its membership rewritten by the next LDAP synchronization. Do not manage its members with this resource.\n\n" +
			"**Partial results.** If Pocket ID applies only part of a request (it skips an ID that names no user), the resource reports the error and still records the members the group actually holds, so that Terraform marks it tainted and destroying it removes those members; nothing is left unmanaged. " +
			"When a request fails in a way that does not show whether it was applied, the group is read once and what it holds is recorded; if that read fails too, the members last read are kept and a refresh reconciles them.\n\n" +
			"**Destroying** the resource removes the users in `user_ids` from the group. Members added outside Terraform since the last refresh stay. The group itself is not deleted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The resource ID, the same as `group_id`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"group_id": schema.StringAttribute{
				Description: "The ID of the group (a UUID). Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(groupMembersUUIDPattern, "must be a UUID"),
				},
			},
			"user_ids": schema.SetAttribute{
				Description: "The IDs of the users (UUIDs) that are the group's members, no more and no fewer. An empty set means the group has no members. An ID that names no user is an error, not skipped.",
				Required:    true,
				ElementType: types.StringType,
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(groupMembersUUIDPattern, "must be a UUID")),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *groupMembersResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// groupMembersIDs reads a set of user IDs out of the model, sorted.
func groupMembersIDs(ctx context.Context, set types.Set, diags *diag.Diagnostics) []string {
	var ids []string
	diags.Append(set.ElementsAs(ctx, &ids, false)...)
	sort.Strings(ids)
	return ids
}

// groupMembersDiff returns the IDs in a that are not in b, sorted.
func groupMembersDiff(a, b []string) []string {
	in := make(map[string]struct{}, len(b))
	for _, id := range b {
		in[id] = struct{}{}
	}
	var out []string
	for _, id := range a {
		if _, ok := in[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// groupMembersList renders IDs for a message: no more than five of them.
func groupMembersList(ids []string) string {
	const shown = 5
	if len(ids) <= shown {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ids[:shown], ", "), len(ids)-shown)
}

// groupMembersSame reports whether a and b hold the same IDs.
func groupMembersSame(a, b []string) bool {
	return len(groupMembersDiff(a, b)) == 0 && len(groupMembersDiff(b, a)) == 0
}

// groupMembersPut is what became of one PUT of a membership.
type groupMembersPut struct {
	// Changed is false only when the group provably holds what it held before:
	// the request was refused outright, or it was answered or re-read and
	// nothing differs. Otherwise the group's members may have changed.
	Changed bool
	// Observed is the membership read after the request, if it could be read.
	Observed      []string
	ObservedKnown bool
	// Gone: the group no longer exists.
	Gone bool
	// Err is why the request itself failed, or nil. A failure that does not
	// prove the change was not applied is still followed by a read.
	Err error
}

// put replaces the group's members with ids and finds out what the group holds
// afterwards. before is the membership read just before. The PUT is never
// repeated. An accepted answer is taken as read; an answer that cannot be used,
// or a failure that does not prove the request was refused (a server error, a
// broken connection, an interrupted body), is followed by one read of the group,
// because the change may have been made.
func (r *groupMembersResource) put(ctx context.Context, groupID string, ids, before []string) groupMembersPut {
	got, err := r.client.SetGroupMembers(ctx, groupID, ids)
	if err == nil {
		return groupMembersPut{Changed: !groupMembersSame(got, before), Observed: got, ObservedKnown: true}
	}
	if client.IsNotFound(err, client.ResourceUserGroup) {
		return groupMembersPut{Gone: true, Err: err}
	}
	if client.IsDefiniteRejection(err) || errors.Is(err, client.ErrInvalidIdentifier) {
		return groupMembersPut{Err: err}
	}

	reread, readErr := r.client.GetUserGroupDetail(ctx, groupID)
	switch {
	case readErr == nil:
		return groupMembersPut{Changed: !groupMembersSame(reread.MemberIDs, before), Observed: reread.MemberIDs, ObservedKnown: true, Err: err}
	case client.IsNotFound(readErr, client.ResourceUserGroup):
		return groupMembersPut{Gone: true, Err: err}
	default:
		return groupMembersPut{Changed: true, Err: err}
	}
}

// groupMembersWrite is the outcome of write.
type groupMembersWrite struct {
	// OK: the group now holds exactly the requested members.
	OK bool
	// Changed: the group's members may have been changed by this call even
	// though it did not succeed, so the resource must stay in state to be able
	// to reconcile or undo that.
	Changed bool
	// Members is what to record in state when Changed and not OK: the
	// membership observed after the request, or, when that could not be read,
	// the one read before it.
	Members []string
}

// write replaces the group's members with want, after checking that the
// replacement removes nobody the plan did not show.
//
// The group's current members must all be in want or in known (the members
// Terraform's state recorded): anyone else joined since the plan was made, and
// replacing the membership would silently remove them. The result is checked
// against what was asked for, because Pocket ID drops IDs that name no user
// without an error. The caller holds the membership lock. On failure the error
// is in diags, and the outcome says whether the group may have changed.
func (r *groupMembersResource) write(ctx context.Context, groupID string, known, want []string, diags *diag.Diagnostics) groupMembersWrite {
	current, err := r.client.GetUserGroupDetail(ctx, groupID)
	if err != nil {
		diags.AddError("Error reading group", fmt.Sprintf("Could not read group %s before changing its members: %s", groupID, err))
		return groupMembersWrite{}
	}
	if unplanned := groupMembersDiff(current.MemberIDs, append(append([]string(nil), known...), want...)); len(unplanned) > 0 {
		diags.AddError(
			"Group has members the plan did not show",
			fmt.Sprintf("Group %s has %d member(s) that user_ids does not list and that Terraform did not know of: %s. "+
				"Applying would remove them, and the plan could not have shown that. "+
				"Add them to user_ids, or import the group (terraform import pocketid_group_members.<name> %s) so that the plan shows them. Nothing was changed.",
				groupID, len(unplanned), groupMembersList(unplanned), groupID),
		)
		return groupMembersWrite{}
	}
	if groupMembersSame(current.MemberIDs, want) {
		return groupMembersWrite{OK: true} // already exactly right: nothing to write
	}

	result := r.put(ctx, groupID, want, current.MemberIDs)
	switch {
	case result.Gone:
		diags.AddError("Group not found", fmt.Sprintf("Group %s does not exist (any longer). Nothing was changed.", groupID))
		return groupMembersWrite{}
	case result.Err != nil && !result.Changed && !result.ObservedKnown:
		// Refused outright.
		diags.AddError("Error setting group members", fmt.Sprintf("Pocket ID refused to set the members of group %s: %s. Nothing was changed.", groupID, result.Err))
		return groupMembersWrite{}
	case !result.ObservedKnown:
		diags.AddError(
			"Group members may have changed",
			fmt.Sprintf("The request to set the members of group %s failed (%s) and reading the group afterwards failed too, so it is not known whether the members changed. "+
				"The resource keeps the members last read; refresh and plan again to see what the group holds.", groupID, result.Err),
		)
		return groupMembersWrite{Changed: true, Members: current.MemberIDs}
	}

	if groupMembersSame(result.Observed, want) {
		if result.Err != nil {
			diags.AddWarning("Request failed but the members were set",
				fmt.Sprintf("The request to set the members of group %s failed (%s), but reading the group afterwards shows the requested members, so the change is recorded.", groupID, result.Err))
		}
		return groupMembersWrite{OK: true, Changed: true}
	}
	groupMembersReportDifference(groupID, want, result.Observed, result.Err, diags)
	return groupMembersWrite{Changed: result.Changed, Members: result.Observed}
}

// groupMembersReportDifference adds an error naming how what the group holds
// differs from what was asked for.
func groupMembersReportDifference(groupID string, want, got []string, requestErr error, diags *diag.Diagnostics) {
	missing := groupMembersDiff(want, got)
	extra := groupMembersDiff(got, want)
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("%d requested user(s) are not members, most likely because no such user exists: %s", len(missing), groupMembersList(missing)))
	}
	if len(extra) > 0 {
		parts = append(parts, fmt.Sprintf("%d member(s) were not requested: %s", len(extra), groupMembersList(extra)))
	}
	lead := "Pocket ID changed the members of group %s, but the group does not hold what was asked for"
	if requestErr != nil {
		lead = "The request to set the members of group %s failed (" + requestErr.Error() + ") and the group does not hold what was asked for"
	}
	diags.AddError(
		"Group members differ from the request",
		fmt.Sprintf(lead+": %s. The resource records the members the group actually holds; correct user_ids and apply again.", groupID, strings.Join(parts, "; ")),
	)
}

// Create replaces the group's membership with user_ids.
func (r *groupMembersResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupMembersResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := plan.GroupID.ValueString()
	want := groupMembersIDs(ctx, plan.UserIDs, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The shared lock, held across the whole read, write and verify sequence;
	// see lockMembershipWrites.
	defer lockMembershipWrites()()

	// A new resource knows of no member: every current member must be one of
	// want.
	outcome := r.write(ctx, groupID, nil, want, &resp.Diagnostics)
	if !outcome.OK {
		if outcome.Changed {
			// The group's members changed (for example the valid users of a
			// request that also named a user that does not exist were added).
			// Keep the resource, with the members the group actually holds, so
			// that destroying it removes them and a refresh sees them; the
			// error stays, so Terraform marks the resource tainted.
			r.keep(ctx, &plan, outcome.Members, &resp.State, &resp.Diagnostics)
		}
		return
	}
	plan.ID = types.StringValue(groupID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// keep records the resource with the given members, after a write that failed
// but may have changed the group.
func (r *groupMembersResource) keep(ctx context.Context, model *groupMembersResourceModel, members []string, state *tfsdk.State, diags *diag.Diagnostics) {
	set, setDiags := ugIDSetValueResource(ctx, members)
	diags.Append(setDiags...)
	if setDiags.HasError() {
		return
	}
	model.ID = types.StringValue(model.GroupID.ValueString())
	model.UserIDs = set
	diags.Append(state.Set(ctx, model)...)
}

// Read refreshes the state with the group's actual members.
func (r *groupMembersResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupMembersResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	group, err := r.client.GetUserGroupDetail(ctx, state.GroupID.ValueString())
	if err != nil {
		// Only Pocket ID's own "no such group" means the group is gone; any
		// other failure (a wrong URL, a proxy's 404, an outage) is an error.
		if client.IsNotFound(err, client.ResourceUserGroup) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading group members", fmt.Sprintf("Could not read group %s: %s", state.GroupID.ValueString(), err))
		return
	}

	members, diags := ugIDSetValueResource(ctx, group.MemberIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ID = types.StringValue(state.GroupID.ValueString())
	state.UserIDs = members
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ugIDSetValueResource converts IDs to a set of strings, empty rather than
// null for none.
func ugIDSetValueResource(ctx context.Context, ids []string) (types.Set, diag.Diagnostics) {
	if ids == nil {
		ids = []string{}
	}
	return types.SetValueFrom(ctx, types.StringType, ids)
}

// Update replaces the group's membership with the new user_ids.
func (r *groupMembersResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupMembersResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := plan.GroupID.ValueString()
	want := groupMembersIDs(ctx, plan.UserIDs, &resp.Diagnostics)
	known := groupMembersIDs(ctx, state.UserIDs, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The shared lock, held across the whole read, write and verify sequence;
	// see lockMembershipWrites.
	defer lockMembershipWrites()()

	outcome := r.write(ctx, groupID, known, want, &resp.Diagnostics)
	if !outcome.OK {
		if outcome.Changed {
			// Record what the group actually holds, not the prior members and
			// not the plan; the error stays.
			r.keep(ctx, &plan, outcome.Members, &resp.State, &resp.Diagnostics)
		}
		return
	}
	plan.ID = types.StringValue(groupID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the users the resource manages from the group. Members added
// since the last refresh stay, and a group that is already gone is nothing to
// do.
func (r *groupMembersResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupMembersResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := state.GroupID.ValueString()
	managed := groupMembersIDs(ctx, state.UserIDs, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The shared lock, held across the whole read, write and verify sequence;
	// see lockMembershipWrites.
	defer lockMembershipWrites()()

	current, err := r.client.GetUserGroupDetail(ctx, groupID)
	if err != nil {
		if client.IsNotFound(err, client.ResourceUserGroup) {
			return
		}
		resp.Diagnostics.AddError("Error reading group", fmt.Sprintf("Could not read group %s before removing its members: %s", groupID, err))
		return
	}
	remaining := groupMembersDiff(current.MemberIDs, managed)
	if len(remaining) == len(current.MemberIDs) {
		return // none of the managed users is a member any more
	}

	result := r.put(ctx, groupID, remaining, current.MemberIDs)
	switch {
	case result.Gone:
		return // the group was deleted in the meantime
	case !result.ObservedKnown && result.Err != nil && !result.Changed:
		resp.Diagnostics.AddError("Error removing group members", fmt.Sprintf("Pocket ID refused to remove the members of group %s: %s", groupID, result.Err))
		return
	case !result.ObservedKnown:
		resp.Diagnostics.AddError(
			"Group members may have changed",
			fmt.Sprintf("The request to remove the members of group %s failed (%s) and reading the group afterwards failed too, so it is not known whether they were removed.", groupID, result.Err),
		)
		return
	}
	if !groupMembersSame(result.Observed, remaining) {
		groupMembersReportDifference(groupID, remaining, result.Observed, result.Err, &resp.Diagnostics)
	}
}

// ImportState imports a group's membership. The import identifier is the
// group's ID; Read then fills in user_ids with the group's actual members.
func (r *groupMembersResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if err := client.ValidateUUID("user group", req.ID); err != nil {
		resp.Diagnostics.AddError("Unexpected Import Identifier", "Expected the ID of a group (a UUID).")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), req.ID)...)
}
