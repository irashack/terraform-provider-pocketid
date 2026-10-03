package resources

import "sync"

// clientSecretLocks holds one mutex per OIDC client ID; see
// lockClientSecrets. Entries are never removed: a provider process lives for
// one plan or apply, and the clients it touches are bounded by the
// configuration.
var (
	clientSecretLocksMu sync.Mutex
	clientSecretLocks   = map[string]*sync.Mutex{}
)

// lockClientSecrets serializes, within this provider process, everything
// that changes an OIDC client's secrets or identifies a secret by comparing
// the client's secret list over time. It blocks until the client's lock is
// free and returns the function that releases it:
//
//	unlock := lockClientSecrets(clientID)
//	defer unlock()
//
// Pocket ID keeps a client's secrets in one document on the client and
// rewrites it for every change, so concurrent changes to one client contend
// for it. And a create whose outcome is uncertain is reported by comparing
// the secrets listed before and after it, which names the right candidate
// only if no other change to that client's secrets runs in between.
//
// Every such sequence must hold the lock for its whole length, from the list
// it compares against (or the mutation) to the last read that interprets the
// result:
//   - pocketid_client_secret: Create (the list before, the POST, the list
//     after an uncertain result) and Delete (the DELETE and its confirming
//     list);
//   - pocketid_client: on create, from revoking the secret Pocket ID 2.17
//     creates with a client (with its confirming list) to generating its own;
//     on update, the reads that decide a secret change (a pending
//     revocation's check, an unresolved generation's check, identifying the
//     secret to revoke) and, separately, the generation or revocation itself.
//     The client's own PUT and its group write run between those two and
//     hold no lock.
//
// The lock is per client ID and never held across clients, so it cannot
// deadlock between resources; callers must not take it twice for the same
// client (it is not reentrant). Changes made outside this process (another
// apply, Pocket ID's interface) are not covered.
//
// Take it once, at the top of the resource operation (Create, Update or
// Delete), never inside a helper that such an operation also calls. Nothing
// in the client package takes it, so a holder may run any sequence of client
// calls (generate, create, list, revoke) and helpers such as
// pocketid_client's revokeServerCreatedSecret under it. A new client's ID,
// the key, exists only once the POST that creates it has returned, so
// pocketid_client takes the lock right after that POST and may release it
// after its last secret step, before the group update and any failedCreate
// cleanup, which touch no secret.
func lockClientSecrets(clientID string) func() {
	clientSecretLocksMu.Lock()
	lock, ok := clientSecretLocks[clientID]
	if !ok {
		lock = &sync.Mutex{}
		clientSecretLocks[clientID] = lock
	}
	clientSecretLocksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}
