package resources

import (
	"context"
	"time"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// accountCleanupTimeout bounds the cleanup of an object a Create made
// before a later step failed: the DELETE, and the read that checks its
// outcome when the DELETE itself fails.
const accountCleanupTimeout = 15 * time.Second

// createdAccountObject is a user or group that Create made before a later step
// (custom claims, group membership) failed.
type createdAccountObject struct {
	// kind names the object in diagnostics, such as "user" or "user group".
	kind string
	id   string
	// missing is the kind of not-found error that confirms it is gone.
	missing client.Resource
	remove  func(ctx context.Context, id string) error
	read    func(ctx context.Context, id string) error
}

// accountCleanupOutcome is what rollBackAccountObject established about the object.
type accountCleanupOutcome struct {
	// gone is true only when the object is confirmed deleted: the DELETE
	// succeeded, or Pocket ID's own not-found error for it was returned.
	gone            bool
	summary, detail string
}

// rollBackAccountObject deletes an object a Create made before step failed with
// cause, and reports what is known afterwards. The cleanup runs on a context
// that keeps the caller's values but not its cancellation, bounded by
// accountCleanupTimeout: a cancelled apply (one reason the step failed)
// must not leave the new object behind without trying, and must not report a
// deletion that was never sent. Only a successful DELETE or Pocket ID's own
// not-found error counts as gone; anything else means the caller keeps the
// object's ID in state.
func rollBackAccountObject(ctx context.Context, obj createdAccountObject, step string, cause error) accountCleanupOutcome {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accountCleanupTimeout)
	defer cancel()

	why := "Setting the " + step + " of the new " + obj.kind + " failed: " + cause.Error()
	deleteErr := obj.remove(cleanupCtx, obj.id)
	if deleteErr == nil {
		return accountCleanupOutcome{gone: true,
			summary: capitalizeAccountKind(obj.kind) + " creation rolled back",
			detail:  why + ". The new " + obj.kind + " (ID " + obj.id + ") was deleted, so nothing was created."}
	}
	if client.IsNotFound(deleteErr, obj.missing) {
		return accountCleanupOutcome{gone: true,
			summary: capitalizeAccountKind(obj.kind) + " rollback verified",
			detail:  why + ". Pocket ID reports the new " + obj.kind + " (ID " + obj.id + ") no longer exists."}
	}
	// A failed DELETE may still have committed. Only Pocket ID's own
	// not-found error confirms that; a bare or proxy 404 does not.
	readErr := obj.read(cleanupCtx, obj.id)
	if client.IsNotFound(readErr, obj.missing) {
		return accountCleanupOutcome{gone: true,
			summary: capitalizeAccountKind(obj.kind) + " rollback verified",
			detail:  why + ". Deleting the new " + obj.kind + " returned an error, but a read confirmed it (ID " + obj.id + ") no longer exists."}
	}
	found := "A read found the " + obj.kind + " still exists."
	if readErr != nil {
		found = "Whether the " + obj.kind + " still exists could not be confirmed (read: " + readErr.Error() + ")."
	}
	return accountCleanupOutcome{
		summary: capitalizeAccountKind(obj.kind) + " cleanup failed",
		detail: why + ". Deleting the new " + obj.kind + " also failed (" + deleteErr.Error() + "). " + found +
			" Its ID " + obj.id + " is kept in state, marked for replacement; inspect it before applying again."}
}

func capitalizeAccountKind(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
