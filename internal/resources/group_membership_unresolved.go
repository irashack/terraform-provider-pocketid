package resources

import "fmt"

// groupMembershipUnresolvedDescription documents the unresolved_creation
// attribute of pocketid_group_membership.
//
// Adding a user to a group is a PUT that replaces the user's whole group list.
// When that PUT is accepted, or may have been, but its result cannot be
// confirmed, the request can still take effect after any later read, so a read
// that sees the user outside the group proves nothing. The pair therefore stays
// in state, and in the state of a tainted replacement, which is all a
// replacement's destroy step is given, until the user is seen in the group
// (the condition then clears) or the resource is removed from state by hand.
const groupMembershipUnresolvedDescription = "True while adding the user to the group was accepted, or may have been, but the result could not be confirmed, " +
	"so the request may still take effect. It is null otherwise. While it is set, a refresh that finds the user outside the group (or the user missing) " +
	"keeps the resource in state with a warning instead of removing it, and a destroy or replacement that finds the user outside the group is refused " +
	"instead of recorded as done. It clears when a refresh sees the user in the group. To give up on the membership instead, run `terraform state rm` on the resource."

// groupMembershipUnresolvedAbsenceDetail explains why a refresh that did not
// see the membership keeps the resource.
func groupMembershipUnresolvedAbsenceDetail(userID, groupID, observed string) string {
	return fmt.Sprintf("Adding user %s to group %s had an unknown outcome in an earlier apply, and %s. The request may still take effect, "+
		"so the resource stays in state. Refresh again later: once the user is seen in the group, this condition clears. To give up on the "+
		"membership, run `terraform state rm` on this resource; if the request lands afterwards, the membership is not tracked.", userID, groupID, observed)
}

// groupMembershipUnresolvedRefusal explains why a delete of an unresolved
// membership that is not visible was refused.
func groupMembershipUnresolvedRefusal(userID, groupID, observed string) string {
	return fmt.Sprintf("Adding user %s to group %s had an unknown outcome in an earlier apply, and %s, so removing the membership cannot be recorded "+
		"as done: the request may still take effect afterwards. Nothing was changed. Refresh again later; once the user is seen in the group it can "+
		"be removed. To stop tracking it without removing anything, run `terraform state rm` on this resource.", userID, groupID, observed)
}
