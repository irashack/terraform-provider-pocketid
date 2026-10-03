package resources

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// errGroupsDropped means Pocket ID accepted an allowed-groups write but kept
// only some of the IDs: OidcService.UpdateAllowedUserGroups looks the groups up
// with "id IN ?" and silently drops IDs that name no group.
var errGroupsDropped = errors.New("the server did not keep every requested allowed user group")

// knownSetSize returns how many elements a set has, 0 for null, and -1 while
// the set itself is unknown. A known set with unknown elements has at least
// one element, which is all callers need.
func knownSetSize(set types.Set) int {
	if set.IsUnknown() {
		return -1
	}
	if set.IsNull() {
		return 0
	}
	return len(set.Elements())
}

// setStrings returns the known string elements of a set, never nil.
func setStrings(set types.Set) []string {
	out := []string{}
	if set.IsNull() || set.IsUnknown() {
		return out
	}
	for _, element := range set.Elements() {
		if value, ok := element.(types.String); ok && !value.IsNull() && !value.IsUnknown() {
			out = append(out, value.ValueString())
		}
	}
	return out
}

// sameMembers reports whether two ID lists hold the same IDs.
func sameMembers(a, b []string) bool {
	return len(missingFrom(a, b)) == 0 && len(missingFrom(b, a)) == 0
}

// missingFrom returns the IDs in want that got lacks, sorted.
func missingFrom(want, got []string) []string {
	// Group IDs are UUIDs, the same in any letter case.
	var missing []string
	for _, id := range want {
		if !slices.ContainsFunc(got, func(held string) bool { return client.SameUUID(held, id) }) {
			missing = append(missing, id)
		}
	}
	slices.Sort(missing)
	return slices.Compact(missing)
}

// planGroupRestriction plans is_group_restricted when the configuration
// omits it. The client is restricted when it is given groups or is restricted
// now, so configuration can restrict a client but never opens a restricted one
// as a side effect; opening one takes is_group_restricted = false. A value
// known in the proposed plan means the resource is not changing, and is kept.
// state is nil when the client is being created.
func planGroupRestriction(state *clientResourceModel, config clientResourceModel, plan *clientResourceModel) {
	if !config.IsGroupRestricted.IsNull() || !plan.IsGroupRestricted.IsUnknown() {
		return
	}
	groups := knownSetSize(plan.AllowedUserGroups)
	switch {
	case groups > 0:
		plan.IsGroupRestricted = types.BoolValue(true)
	case state != nil && state.IsGroupRestricted.ValueBool():
		plan.IsGroupRestricted = types.BoolValue(true)
	case groups == 0 && state == nil:
		plan.IsGroupRestricted = types.BoolValue(false)
	case groups == 0 && !state.IsGroupRestricted.IsNull() && !state.IsGroupRestricted.IsUnknown():
		plan.IsGroupRestricted = types.BoolValue(false)
	}
	// Otherwise it stays unknown: state predates the attribute, and the
	// update resolves it from the server's current value the same way.
}

// warnOnOpening warns when a plan removes a client's group restriction.
func warnOnOpening(state *clientResourceModel, plan clientResourceModel, diags *diag.Diagnostics) {
	if state != nil && state.IsGroupRestricted.ValueBool() && !plan.IsGroupRestricted.IsUnknown() && !plan.IsGroupRestricted.ValueBool() {
		diags.AddAttributeWarning(path.Root("is_group_restricted"), "Client opened to every user",
			"This plan removes the client's group restriction: every user of Pocket ID will be able to sign in to it.")
	}
}

// resolveGroupRestriction decides the restriction an apply writes: the planned
// value, or when it is still unknown the same rule planGroupRestriction
// applies, with the server's current restriction.
func resolveGroupRestriction(planned types.Bool, groups []string, currentlyRestricted bool) bool {
	if !planned.IsNull() && !planned.IsUnknown() {
		return planned.ValueBool()
	}
	return len(groups) > 0 || currentlyRestricted
}

// writeAllowedGroups replaces a client's allowed groups and verifies what the
// server kept. The PUT is never retried. Dropped IDs wrap errGroupsDropped
// and name the IDs; got is then the set the server holds.
func (r *clientResource) writeAllowedGroups(ctx context.Context, clientID string, want []string) (got []string, err error) {
	tflog.Debug(ctx, "Updating allowed user groups", map[string]any{"id": clientID, "groups": len(want)})
	got, err = r.client.UpdateClientAllowedUserGroups(ctx, clientID, want)
	if err != nil {
		return nil, err
	}
	if dropped := missingFrom(want, got); len(dropped) > 0 {
		return got, fmt.Errorf("%w: these IDs name no existing user group and were dropped: %s", errGroupsDropped, strings.Join(dropped, ", "))
	}
	if extra := missingFrom(got, want); len(extra) > 0 {
		return got, fmt.Errorf("after the write the client also allows groups that were not requested: %s", strings.Join(extra, ", "))
	}
	return got, nil
}

// userGroupIDList returns the IDs of groups, never nil.
func userGroupIDList(groups []client.UserGroup) []string {
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}

// groupsFromIDs builds group references from IDs.
func groupsFromIDs(ids []string) []client.UserGroup {
	groups := make([]client.UserGroup, 0, len(ids))
	for _, id := range ids {
		groups = append(groups, client.UserGroup{ID: id})
	}
	return groups
}
