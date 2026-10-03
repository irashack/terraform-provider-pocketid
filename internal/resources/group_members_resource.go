package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	_ resource.ResourceWithModifyPlan  = &groupMembersResource{}
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
	ID                types.String `tfsdk:"id"`
	GroupID           types.String `tfsdk:"group_id"`
	UserIDs           types.Set    `tfsdk:"user_ids"`
	UnresolvedUserIDs types.Set    `tfsdk:"unresolved_user_ids"`
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
			"~> **Concurrent changes by others** Pocket ID can only replace a group's whole member list. Before every write the provider reads the group's members: create and update refuse to remove a member that the plan did not show, and destroy keeps every member that is in neither `user_ids` nor `unresolved_user_ids`. " +
			"Those checks cover what the group held at that read only. A change made by something else (the Pocket ID admin interface, another Terraform run, an onboarding service) after the read and before the write, an instant later, cannot be protected, in either direction: a user added in that instant is removed by the write, and a user removed in that instant is put back by it, which restores access that was just revoked. " +
			"No check can prevent this, and the provider cannot tell afterwards that it happened. " +
			"Within one provider process this resource holds a lock around the whole read, write and verification of each change. `pocketid_group_membership` and `pocketid_user` take the same lock around theirs, so that the writes of the three do not overwrite each other. The lock does not reach another Terraform run, another process or anything outside Terraform.\n\n" +
			"~> **Replace this resource only by destroying the old one first** When this resource is replaced for the same group (a failed create leaves it tainted, so Terraform replaces it on the next apply, or you run `terraform apply -replace=...`), the old resource must be destroyed before the new one is created, which is Terraform's default order. " +
			"Do not set `create_before_destroy = true` on it, directly or through lifecycle ordering inherited from a resource that depends on it. " +
			"The provider cannot tell the old resource's cleanup (the members it recorded and the users in `unresolved_user_ids`) from the members the new resource has just recorded for itself, and another read of the group cannot tell it either, so in the other order the old resource's destroy removes users that the new resource's state still lists as members.\n\n" +
			"~> **LDAP groups** A group synchronized from LDAP gets its membership rewritten by the next LDAP synchronization. Do not manage its members with this resource.\n\n" +
			"**Partial results.** If Pocket ID applies only part of a request (it skips an ID that names no user), the resource reports the error and still records the members the group actually holds, so that nothing is left unmanaged. After a failed create Terraform marks the resource tainted, and destroying it removes those members; after a failed update the resource stays in place with the members the group holds recorded, and the corrected configuration applies to it.\n\n" +
			"**Requests whose outcome is unknown.** When a request fails in a way that does not show whether it was applied (the answer was lost or could not be read, or a server or proxy error), the provider reads the group once. If the group then holds what was asked for, that is recorded. " +
			"Otherwise, whether the group still shows its old members or the read fails too, the request may yet take effect (a proxy can give up on a request the server goes on to commit), so the resource keeps its identity, records the members it read, and lists the users that were requested in `unresolved_user_ids`. " +
			"While that is set, plans for the resource are refused, naming the recovery. A refresh reads the group again and clears it, recording the members then held. Destroying the resource first reads the group and removes the requested users that are members, together with the members it had recorded, so a request that committed late is cleaned up too; if the group cannot be read, destroy stops with an error and keeps the resource in state. " +
			"Membership carries no grant provenance: Pocket ID does not say who made a user a member, so a user listed in `unresolved_user_ids` who is a member when destroy reads the group is removed, including one that an administrator granted independently of the request that failed. " +
			"A refresh reads the group once. A destroy reads it before it writes (the initial snapshot) and, if its own removal request fails in a way that does not show whether it was applied, once more to verify. These are observations of the group at a moment, and none of them proves that the earlier request has finished. Cleanup covers the grants visible at those observations, not later commits. A request that is still pending when a refresh clears `unresolved_user_ids`, or when destroy finishes, can still be applied afterwards, and the user it adds is then in the group with nothing managing it. " +
			"To stop managing the group without changing it, run `terraform state rm` for the resource.\n\n" +
			"**Destroying** the resource removes the users in `user_ids` from the group, and the users in `unresolved_user_ids` who are members when it reads the group. Members added outside Terraform since the last refresh stay, unless they are in `unresolved_user_ids`. The group itself is not deleted.",
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
			"unresolved_user_ids": schema.SetAttribute{
				Description: "The users this resource asked Pocket ID to make members when the outcome of that request is unknown (the answer was lost or unreadable, and the group then still showed its old members or could not be read), so they may become members later. Null for every other resource. While it is set, plans for the resource are refused; a refresh reads the group once and clears it, and destroy reads it (and once more to verify its own removal, if that fails uncertainly) and removes the users listed here that are members, or stops with an error if the group cannot be read. None of those reads proves the earlier request has finished: they cover the grants visible when they read, not later commits. A listed user who is a member when destroy reads the group is removed whoever granted it: membership has no grant provenance. `terraform state rm` gives up management without changing the group.",
				Computed:    true,
				ElementType: types.StringType,
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
	// Uncertain: the request failed without proving that it was refused (a
	// server or proxy error, a broken connection, an unusable answer). Even a
	// read that shows nothing changed does not settle it: a request that
	// timed out upstream can still commit afterwards.
	Uncertain bool
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
		return groupMembersPut{Changed: !groupMembersSame(reread.MemberIDs, before), Observed: reread.MemberIDs, ObservedKnown: true, Err: err, Uncertain: true}
	case client.IsNotFound(readErr, client.ResourceUserGroup):
		return groupMembersPut{Gone: true, Err: err}
	default:
		return groupMembersPut{Changed: true, Err: err, Uncertain: true}
	}
}

// groupMembersWrite is the outcome of write.
type groupMembersWrite struct {
	// OK: the group now holds exactly the requested members.
	OK bool
	// Changed: the group's members were changed by this call even though it
	// did not succeed (a request that named an unknown user still added the
	// others), so the resource must stay in state to be able to undo that.
	Changed bool
	// Unresolved: the request's outcome is unknown and stays unknown even after
	// the group was read, so it may still take effect. The resource must stay
	// in state with Candidates recorded as users it may have to remove.
	Unresolved bool
	// Candidates are the users that were requested, when Unresolved.
	Candidates []string
	// Members is what to record in state when Changed or Unresolved and not
	// OK: the membership observed after the request, or, when that could not be
	// read, the one read before it.
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
	case result.Err != nil && !result.Uncertain:
		// Refused outright.
		diags.AddError("Error setting group members", fmt.Sprintf("Pocket ID refused to set the members of group %s: %s. Nothing was changed.", groupID, result.Err))
		return groupMembersWrite{}
	case !result.ObservedKnown:
		diags.AddError(
			"Group members may have changed",
			fmt.Sprintf("The request to set the members of group %s failed (%s) and reading the group afterwards failed too, so it is not known whether the members changed or will change. "+
				"The resource keeps the members last read and lists the requested users in unresolved_user_ids; refresh to see what the group holds, or run terraform state rm to give up management. "+groupMembersReplaceNote, groupID, result.Err),
		)
		return groupMembersWrite{Changed: true, Unresolved: true, Candidates: want, Members: current.MemberIDs}
	}

	if groupMembersSame(result.Observed, want) {
		if result.Err != nil {
			diags.AddWarning("Request failed but the members were set",
				fmt.Sprintf("The request to set the members of group %s failed (%s), but reading the group afterwards shows the requested members, so the change is recorded.", groupID, result.Err))
		}
		return groupMembersWrite{OK: true, Changed: true}
	}
	if result.Uncertain {
		// The request failed without proving it was refused, and the group does
		// not hold what was asked: still unsettled, whether it shows the old
		// members or a mixture, because the request may yet be applied.
		diags.AddError(
			"Group members may have changed",
			fmt.Sprintf("The request to set the members of group %s failed (%s) and reading the group afterwards does not show the requested members. That does not prove the request had no effect: a request that timed out can still be applied afterwards. "+
				"The resource keeps the members the group holds now and lists the requested users in unresolved_user_ids; refresh to see what the group holds then, or run terraform state rm to give up management. "+groupMembersReplaceNote, groupID, result.Err),
		)
		return groupMembersWrite{Changed: result.Changed, Unresolved: true, Candidates: want, Members: result.Observed}
	}
	groupMembersReportDifference(groupID, want, result.Observed, result.Err, result.Changed, diags)
	return groupMembersWrite{Changed: result.Changed, Members: result.Observed}
}

// groupMembersReplaceNote ends every diagnostic that keeps a resource in state
// after a failed write: the order of a replacement is part of this resource's
// contract. It is conditional because only a failed Create taints the resource
// (and so has Terraform replace it on the next apply); after a failed Update the
// corrected configuration proceeds in place, and -replace replaces any of them.
const groupMembersReplaceNote = "If this resource is replaced (Terraform replaces one that a failed create left tainted on the next apply, and `-replace` replaces any), the old resource must be destroyed before the new one is created, which is its default order. " +
	"Do not use create_before_destroy for this resource, including lifecycle ordering inherited from a resource that depends on it."

// groupMembersReportDifference adds an error naming how what the group holds
// differs from what was asked for.
//
// kept says the resource is kept in state after this error.
func groupMembersReportDifference(groupID string, want, got []string, requestErr error, kept bool, diags *diag.Diagnostics) {
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
	detail := fmt.Sprintf(lead+": %s. The resource records the members the group actually holds; correct user_ids and apply again.", groupID, strings.Join(parts, "; "))
	if kept {
		detail += " " + groupMembersReplaceNote
	}
	diags.AddError("Group members differ from the request", detail)
}

// The unresolved candidates are recorded twice: in the computed
// unresolved_user_ids attribute and in private state. Terraform taints a
// resource whose create returned an error and plans its replacement from a
// null prior state with no private state; the destroy half of that replacement
// runs Delete with the replacement plan's private data, which has no marker,
// and with the tainted resource's prior state. The attribute is therefore what
// protects the users. The private marker covers a state that has it.
const groupMembersUnresolvedKey = "unresolved_members"

// groupMembersPrivateReader and groupMembersPrivateWriter are the parts of the
// framework's private state this file uses: requests offer GetKey, responses
// both.
type groupMembersPrivateReader interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type groupMembersPrivateWriter interface {
	groupMembersPrivateReader
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// groupMembersPrivateAvailable reports whether private state can be written.
// The framework always supplies it; a direct call from a test may not.
func groupMembersPrivateAvailable(private groupMembersPrivateWriter) bool {
	if private == nil {
		return false
	}
	value := reflect.ValueOf(private)
	return value.Kind() != reflect.Pointer || !value.IsNil()
}

// groupMembersUnresolved returns the candidates recorded by an earlier write
// whose outcome was unknown, from the resource's state and its private state,
// and whether there are any.
func groupMembersUnresolved(ctx context.Context, attribute types.Set, private groupMembersPrivateReader, diags *diag.Diagnostics) ([]string, bool) {
	unresolved := false
	var candidates []string
	if !attribute.IsNull() && !attribute.IsUnknown() {
		unresolved = true
		diags.Append(attribute.ElementsAs(ctx, &candidates, false)...)
	}
	if private != nil {
		value, d := private.GetKey(ctx, groupMembersUnresolvedKey)
		diags.Append(d...)
		if len(value) > 0 {
			unresolved = true
			var recorded struct {
				UserIDs []string `json:"user_ids"`
			}
			if err := json.Unmarshal(value, &recorded); err == nil {
				candidates = append(candidates, recorded.UserIDs...)
			}
		}
	}
	candidates = groupMembersDiff(candidates, nil) // sorted copy
	return groupMembersUnique(candidates), unresolved
}

// groupMembersUnique drops repeats from a sorted list.
func groupMembersUnique(sorted []string) []string {
	var out []string
	for i, id := range sorted {
		if i == 0 || id != sorted[i-1] {
			out = append(out, id)
		}
	}
	return out
}

// groupMembersSetUnresolved records or clears the private marker.
func groupMembersSetUnresolved(ctx context.Context, private groupMembersPrivateWriter, candidates []string, diags *diag.Diagnostics) {
	if !groupMembersPrivateAvailable(private) {
		return
	}
	if candidates == nil {
		// Clear only what is there: there is nothing to remove otherwise.
		if value, _ := private.GetKey(ctx, groupMembersUnresolvedKey); len(value) > 0 {
			diags.Append(private.SetKey(ctx, groupMembersUnresolvedKey, nil)...)
		}
		return
	}
	value, err := json.Marshal(struct {
		UserIDs []string `json:"user_ids"`
	}{UserIDs: candidates})
	if err != nil {
		diags.AddError("Error recording the unresolved members", err.Error())
		return
	}
	diags.Append(private.SetKey(ctx, groupMembersUnresolvedKey, value)...)
}

// groupMembersUnresolvedDetail explains the recovery when a plan is refused.
func groupMembersUnresolvedDetail(groupID string, candidates []string) string {
	return fmt.Sprintf("An earlier change to the members of group %s had an unknown outcome, so it may still take effect: the resource lists %d requested user(s) in unresolved_user_ids (%s). "+
		"Nothing was changed. Refresh (run the plan without -refresh=false) so that the provider reads the group and records what it holds, then plan again; "+
		"or run terraform state rm for this resource to stop managing the group without changing it.", groupID, len(candidates), groupMembersList(candidates))
}

// ModifyPlan keeps unresolved_user_ids null in every plan and refuses to plan a
// change for a resource that has unresolved candidates recorded: the plan would
// not show what the unknown request may still do. A destroy is not refused,
// because Delete reconciles the candidates before it removes anything. Terraform
// plans the replacement of a tainted resource from a null prior state, which
// this cannot see; Delete is where that case is protected.
func (r *groupMembersResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if !req.State.Raw.IsNull() {
		var state groupMembersResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if candidates, unresolved := groupMembersUnresolved(ctx, state.UnresolvedUserIDs, req.Private, &resp.Diagnostics); unresolved {
			resp.Diagnostics.AddError("Group members have an unresolved change", groupMembersUnresolvedDetail(state.GroupID.ValueString(), candidates))
			return
		}
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("unresolved_user_ids"), types.SetNull(types.StringType))...)
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
		if outcome.Changed || outcome.Unresolved {
			// The group's members changed (for example the valid users of a
			// request that also named a user that does not exist were added),
			// or may still change (the request's outcome is unknown). Keep the
			// resource, with the members the group actually holds and any
			// candidates to clean up, so that destroying it removes them and a
			// refresh sees them; the error stays, so Terraform marks the
			// resource tainted.
			r.keep(ctx, &plan, outcome, &resp.State, resp.Private, &resp.Diagnostics)
		}
		return
	}
	plan.ID = types.StringValue(groupID)
	plan.UnresolvedUserIDs = types.SetNull(types.StringType)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	groupMembersSetUnresolved(ctx, resp.Private, nil, &resp.Diagnostics)
}

// keep records the resource after a write that failed but changed the group or
// may still change it: with the members the group holds (or held before the
// request, when it could not be read), and, when the outcome is unknown, the
// requested users as candidates to clean up.
func (r *groupMembersResource) keep(ctx context.Context, model *groupMembersResourceModel, outcome groupMembersWrite, state *tfsdk.State, private groupMembersPrivateWriter, diags *diag.Diagnostics) {
	set, setDiags := ugIDSetValueResource(ctx, outcome.Members)
	diags.Append(setDiags...)
	if setDiags.HasError() {
		return
	}
	model.ID = types.StringValue(model.GroupID.ValueString())
	model.UserIDs = set
	model.UnresolvedUserIDs = types.SetNull(types.StringType)
	var candidates []string
	if outcome.Unresolved {
		candidates = groupMembersUnique(groupMembersDiff(outcome.Candidates, nil))
		if candidates == nil {
			candidates = []string{}
		}
		recorded, d := ugIDSetValueResource(ctx, candidates)
		diags.Append(d...)
		if d.HasError() {
			return
		}
		model.UnresolvedUserIDs = recorded
	}
	diags.Append(state.Set(ctx, model)...)
	groupMembersSetUnresolved(ctx, private, candidates, diags)
}

// Read refreshes the state with the group's actual members. A resource with
// unresolved candidates is resolved by this read: the members the group holds
// are recorded, the candidates cleared.
func (r *groupMembersResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupMembersResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	group, err := r.client.GetUserGroupDetail(ctx, state.GroupID.ValueString())
	if err != nil {
		// Only Pocket ID's own "no such group" means the group is gone; any
		// other failure (a wrong URL, a proxy's 404, an outage) is an error,
		// and an unresolved resource stays as it is.
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
	state.UnresolvedUserIDs = types.SetNull(types.StringType)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	groupMembersSetUnresolved(ctx, resp.Private, nil, &resp.Diagnostics)
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
	// ModifyPlan refuses to plan a change for an unresolved resource; refuse
	// again here, because the members to protect are not in the plan.
	if candidates, unresolved := groupMembersUnresolved(ctx, state.UnresolvedUserIDs, req.Private, &resp.Diagnostics); unresolved {
		resp.Diagnostics.AddError("Group members have an unresolved change", groupMembersUnresolvedDetail(groupID, candidates))
		return
	}
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
		if outcome.Changed || outcome.Unresolved {
			// Record what the group actually holds, not the prior members and
			// not the plan, and any candidates to clean up; the error stays.
			r.keep(ctx, &plan, outcome, &resp.State, resp.Private, &resp.Diagnostics)
		}
		return
	}
	plan.ID = types.StringValue(groupID)
	plan.UnresolvedUserIDs = types.SetNull(types.StringType)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	groupMembersSetUnresolved(ctx, resp.Private, nil, &resp.Diagnostics)
}

// Delete removes the users the resource manages from the group. Members added
// since the last refresh stay, unless they are among the unresolved candidates:
// membership carries no record of who granted it, so a candidate who is a member
// is removed even if someone else granted it. A group that is already gone is
// nothing to do.
//
// The users managed are the ones in state, and, when an earlier write left its
// outcome unknown, the users that request named: they are read from the group
// here, now, and a member among them is removed, so that a request that
// committed late is cleaned up and not left as unmanaged access. If the group
// cannot be read the resource stays in state with an error; it is never
// forgotten on the strength of the snapshot from before the write.
func (r *groupMembersResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupMembersResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := state.GroupID.ValueString()
	managed := groupMembersIDs(ctx, state.UserIDs, &resp.Diagnostics)
	candidates, _ := groupMembersUnresolved(ctx, state.UnresolvedUserIDs, req.Private, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	managed = groupMembersUnique(groupMembersDiff(append(managed, candidates...), nil))

	// The shared lock, held across the whole read, write and verify sequence;
	// see lockMembershipWrites.
	defer lockMembershipWrites()()

	current, err := r.client.GetUserGroupDetail(ctx, groupID)
	if err != nil {
		if client.IsNotFound(err, client.ResourceUserGroup) {
			return
		}
		resp.Diagnostics.AddError("Error reading group", fmt.Sprintf("Could not read group %s before removing its members: %s. Nothing was removed and the resource stays in state; destroy again once the group can be read.", groupID, err))
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
		groupMembersReportDifference(groupID, remaining, result.Observed, result.Err, false, &resp.Diagnostics)
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
