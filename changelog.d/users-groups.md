- A `pocketid_user` or `pocketid_group` deleted outside Terraform (for
  example in the admin web UI) is now removed from state on the next refresh
  and planned for creation again. Before, every plan and refresh failed with
  an error. Destroying one that is already gone now succeeds. Only Pocket
  ID's own "not found" answer for that user or group counts; any other 404
  (a wrong base URL, a proxy's error page) is still an error.
- When creating a `pocketid_user` or `pocketid_group` fails after Pocket ID
  has created the object (for example, setting its custom claims or groups
  fails, or the run is cancelled at that moment), the provider deletes the new
  object and says so only when the deletion is confirmed. If the deletion
  fails or cannot be confirmed, the object's ID is kept in state (marked for
  replacement) and the error says the cleanup failed; before, the provider
  reported "the user was deleted" without checking and lost track of the
  object.
- `pocketid_group_membership` and the `groups` attribute of `pocketid_user`
  now check the user's groups after every change. Pocket ID silently ignores a
  group ID that names no group (one that never existed, or was deleted during
  the apply); before, the provider then recorded a membership that did not
  exist. Now the apply fails with an error naming the group. For
  `pocketid_user`, state records the groups the user is actually in.
- A `pocketid_user` created by the provider now holds exactly the configured
  `groups` and `custom_claims`, including none at all. Before, Pocket ID's
  signup default groups and default custom claims (Settings, "Signup defaults")
  were added to every user the provider created and stayed there, invisible to
  Terraform when `groups` or `custom_claims` was empty or omitted. The planned
  groups are now sent with the create request, which keeps Pocket ID from
  adding its default groups; with no groups planned, and for the default
  claims (which Pocket ID always adds), the provider replaces them right after
  the create. If your instance has signup defaults and you relied on them for
  users created by Terraform, list those groups and claims in the
  configuration.
- `groups = []`, `custom_claims = {}` (on `pocketid_user` and
  `pocketid_group`) and an omitted attribute now all plan empty once applied.
  Before, an explicit empty value showed a change on every plan, and clearing
  claims with `{}` failed with "Provider produced inconsistent result".
- An omitted `first_name` or `last_name` stays null instead of becoming `""`,
  which made creating a user without them fail with "Provider produced
  inconsistent result" and then show a change on every plan.
  **Upgrade note** for users without `first_name` or `last_name` under 2.4.x,
  which recorded `""` for them in state:
  - A user 2.4.x *imported* (or one untainted by hand) plans one in-place
    update of that attribute from `""` to null after upgrading, also with
    `-refresh=false`. Applying it changes nothing in Pocket ID (the name stays
    empty) and keeps the user; later plans are empty. The provider cannot skip
    it: from state alone, `""` written by 2.4.x for an omitted name cannot be
    told apart from a configured `first_name = ""`, which must stay `""`.
  - A user 2.4.x *created* this way is tainted in state (its create failed
    with "inconsistent result"), so every 2.4.x apply replaced it. The next
    apply replaces it once more, deleting and recreating the account and its
    passkeys, and later plans are empty. To keep the account, run
    `terraform untaint` on it before applying; it then gets the in-place
    update instead.
- After writing custom claims, the provider compares what Pocket ID stored with
  what was configured and fails, naming the keys, if they differ (Pocket ID
  stores keys and values in Unicode NFC form).
- `pocketid_user.email` is now optional, for instances that do not require
  an email address (`require_user_email = "false"`). Pocket ID requires one by
  default, and the error then says so. Existing configurations are unaffected.
- **Breaking:** more values Pocket ID would reject are now refused at plan time
  instead of failing the apply: a `username` that breaks Pocket ID's rule (1
  to 50 letters, digits, `_`, `.`, `@`, `-`, starting and ending with a letter
  or digit), a `first_name` or `last_name` over 50 characters, a
  `display_name` over 100, a group `name` shorter than 2 or longer than 255
  characters, a group `friendly_name` shorter than 2 or longer than 50, a
  custom claim with an empty key or value or a reserved name (such as `email`,
  `groups`, `sub` or `type`), and a one-time access token `ttl` outside
  1 second to 744h (now checked at plan time). Lengths are counted in
  characters, as Pocket ID counts them, so names with accented letters are no
  longer cut short at 50 bytes.
- A user or group that Pocket ID synchronizes from LDAP, while LDAP is
  enabled: changing anything but a user's `locale` (or its groups and custom
  claims) now fails before anything is written, naming the attributes, because
  Pocket ID would silently keep them. Changing only a group's custom claims no
  longer sends a group update Pocket ID refuses for LDAP groups, and refused
  deletions explain why.
- `pocketid_user.id` can be set to choose the new user's ID (a lowercase
  UUID; Pocket ID 2.12.0 or later, checked before anything is created). A
  user that already has that ID is not taken over: import it instead. Changing
  `id` later is a plan-time error rather than a replacement, because replacing
  a user deletes their passkeys. Leaving `id` unset works as before.
- `pocketid_one_time_access_token`: when Pocket ID answers without a token,
  or creation fails without a definite answer (a server error or a lost
  connection), the error now says a token may have been created, that it
  stays valid until used or expired, and that nothing was recorded or
  repeated. A token resource is never stored without a token. A missing user
  is reported as such.
- When creating a `pocketid_user` with a chosen `id` fails without a definite
  answer, the ID is kept in state as an unresolved creation: a user found under
  that ID may be someone else's (created between the provider's check and its
  create). Until it is resolved, the provider refuses to change, delete or
  replace that user, at plan time and at apply time. Check the user, then run
  `terraform state rm` on the resource and either `terraform import` it with
  that ID (if it is the intended user) or choose another `id`.
- `pocketid_group_membership`: when adding the user was accepted, or may have
  been, but the result cannot be confirmed (an unreadable response and a
  failed check, a server error or a lost connection), the membership is now
  kept in state (marked for replacement) with an error saying so, so that
  removing it from the configuration still revokes it. Before, nothing was
  recorded and such a membership could stay active unnoticed.
- Reading a user's groups (to add or remove a `pocketid_group_membership`, to
  refresh one, or to check a write whose response could not be read) now
  requires a response that names the user and lists its groups. An empty or
  malformed answer is an error instead of "no groups", which could have made a
  removal look done, dropped a membership from state, or rebuilt the user's
  group list without the groups it had.
- For a user synchronized from LDAP, only attributes the configuration
  actually changes are refused. A change to `locale`, `groups` or
  `custom_claims` is applied, and a `display_name` that is not configured
  keeps the directory's value instead of being replaced by first and last
  name.
- The error for a refused deletion of an LDAP user no longer suggests setting
  `disabled = true` (Pocket ID ignores that for LDAP users); it points to
  removing the user in the directory and running an LDAP sync, or removing it
  from state.
