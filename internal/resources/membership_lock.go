package resources

import "sync"

// membershipWriteMu serializes every write of user-group relations within one
// provider process.
var membershipWriteMu sync.Mutex

// lockMembershipWrites takes the one lock that every resource writing
// user-group relations must hold, and returns the function that releases it:
//
//	defer lockMembershipWrites()()
//
// Pocket ID changes a user's groups and a group's users only by replacing a
// whole list: PUT /users/{id}/user-groups replaces one user's groups, and PUT
// /user-groups/{id}/users replaces one group's members. A resource therefore
// reads a list, decides, writes the replacement and checks the result, and
// another resource doing the same for an overlapping pair, even for a
// different group or a different user, can slip a change in between and have
// it overwritten with the stale list, with both reporting success. Terraform
// applies resources concurrently, so this is not hypothetical.
//
// Hold the lock across the ENTIRE read, write and verify sequence, not just
// the write. One lock for all of them is deliberate: a lock per user or per
// group would not exclude a user-side writer from a group-side one.
//
// The lock does not reach another process. A change made by the Pocket ID
// admin interface, another Terraform run or an onboarding service between this
// provider's read and its write is still replaced by the write; Pocket ID has
// no conditional write that could prevent that.
func lockMembershipWrites() func() {
	membershipWriteMu.Lock()
	return membershipWriteMu.Unlock
}
