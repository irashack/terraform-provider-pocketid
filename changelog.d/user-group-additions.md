- The `pocketid_group` data source now reads a group by `id` directly instead of
  searching a list, and finds a group by `name` through the server's search
  followed by an exact comparison of every result. A group is found however
  many groups exist, and a failed read is no longer reported as "Group Not
  Found": only Pocket ID's own answer that the group does not exist is.
- **Breaking:** the `id` of a `pocketid_group` data source must be a UUID. Any
  other value (the old documentation example used `grp_1234567890`) is refused
  with an error before a request is sent.
- When both `id` and `name` are set on `pocketid_group`, the group with that ID
  must have that name; before, whichever group matched either was returned.
- New attributes on the group data sources: `pocketid_group` and each entry of
  `pocketid_groups` now report `custom_claims`, `member_ids`,
  `allowed_client_ids` and `user_count`. `pocketid_groups` builds the members
  from one pass over the users and the allowed clients from one pass over the
  clients, not one request per group; against Pocket ID 2.14, whose client
  list does not carry groups, the allowed clients cost one request per group.
- New attribute `custom_claims` on the `pocketid_user` and `pocketid_users`
  data sources. It is an empty map for a user without claims.
- The new collection attributes are empty, never null, when there is nothing in
  them; the existing `groups` attribute of the user data sources stays null for
  a user in no group.
- The `email` of the user data sources is null, not an empty string, for a user
  without an email address.
- New data source `pocketid_version`: the version of the Pocket ID server the
  provider talks to, for preconditions and checks.
- New data source `pocketid_current_user`: the user the provider's API key
  belongs to, with the same attributes as `pocketid_user`. Pocket ID 2.14
  through 2.17 accept an API key on this route.
- New data source `pocketid_user_passkeys`: the passkeys a user has registered
  (identifier, name, registration time, whether each is backed up or synced,
  transports and authenticator model), oldest first. It is read-only and reports
  no credential material. Pocket ID records no time of last use, so none is
  shown. There is deliberately no resource to create or delete passkeys.
- New resource `pocketid_group_members`: owns the whole membership of one group.
  The users in `user_ids` are exactly the group's members; a user added outside
  Terraform shows as a difference and is removed by the next apply. It checks
  what the server holds afterwards, so an ID that names no user is an error
  (Pocket ID would silently skip it). It reads the group's members just before
  it writes and refuses to remove any the plan did not show: creating it for a
  group that already has other members, or applying after someone joined since
  the plan, fails and changes nothing; import the group first
  (`terraform import pocketid_group_members.x <group_id>`) to see them in the
  plan. That check cannot cover a change made by something else between its
  read and its write, an instant later: a member added then is removed by the
  write, and a member removed then is put back by it, which restores access just
  revoked. Pocket ID has no conditional write, so this cannot be closed. Do not
  combine it for one group with `pocketid_group_membership`, with
  `pocketid_user.groups`, or with a second `pocketid_group_members`. On Pocket
  ID 2.17, removing a member can sign that user out of group-restricted clients
  that have a back-channel logout URL.
- New resource `pocketid_user_profile_picture`: sets a user's profile picture
  from a local file (`source`), with a computed `sha256` of the file so that a
  changed file uploads again. Destroying it restores the default picture when
  the stored picture is still the one it uploaded (see below).
  Pocket ID turns the file into a 300x300 PNG, accepts PNG, JPEG, GIF, WebP and
  BMP, and from 2.15 refuses more than 16 million pixels; the provider refuses
  such an image at plan time on every version, and a missing, empty, non-image
  or over-10-MiB file too. The server reports no hash of the stored picture,
  so the provider records the digest of the picture the server serves right
  after the upload (`stored_sha256`) and compares it on every refresh: a
  picture replaced or removed outside Terraform shows as a change, and the next
  apply uploads the file again. What it cannot detect: that the stored picture
  came from this file rather than an identical image. After a Pocket ID upgrade
  that changes how pictures are scaled, one extra upload can be planned. There
  is no import.
- `pocketid_group_members` keeps the members Pocket ID actually applied when a
  request is only partly applied (for example when `user_ids` names a user that
  does not exist, the others are still added): it reports the error and records
  the group's real members, so destroying the resource removes them. When a
  request fails without showing whether it was applied, the group is read once;
  if it holds the requested members that is recorded, and otherwise see
  `unresolved_user_ids` below. It also refuses a group record that lacks its
  users instead of reading it as an empty group. It holds a provider-wide lock
  around each write; `pocketid_group_membership` and `pocketid_user` are to take
  the same lock once their changes are integrated, and until then they do not
  wait for it. The lock coordinates one provider process only, not another
  Terraform run or anything outside Terraform.
- `pocketid_user_profile_picture` reads the stored picture past any cache
  between the provider and Pocket ID, so its recorded digest and its drift
  checks describe the picture the server holds now. Destroy restores the default
  picture only if the picture served is still the one recorded after the
  provider's last upload (`stored_sha256`, which a refresh no longer replaces).
  If it is different (replaced or removed outside Terraform, or encoded
  differently after a Pocket ID upgrade), destroy sends no request, leaves the
  picture as it is, warns, and removes the resource from the state. When
  `stored_sha256` is null (the picture could not be read back after an upload)
  destroy stops with an error: apply again, which uploads the file and records
  it, then destroy, or run `terraform state rm`; the next plan shows that upload.
  Two windows cannot be closed. `stored_sha256` is read right after the upload,
  so a different picture uploaded by someone else in between becomes the
  recorded one: it proves the bytes the provider observed, not who uploaded
  them, and a later destroy removes it. And Pocket ID has no conditional
  delete, so a picture uploaded between the provider's check and its delete
  request is removed too.
- In `pocketid_groups`, each group's `user_count` is now the size of its
  `member_ids`, counted from the same pass over the users, instead of the count
  the group list reported from an earlier request; the two could disagree when a
  membership changed while the data source was read. The groups, users and
  clients are still read in separate passes, so the result is not an atomic
  snapshot of the server.
- `pocketid_group` looked up by `name` now fails with an error if the group is
  renamed between finding it and reading its details, instead of returning a
  group that no longer has that name. Read again to resolve it.
- `pocketid_user_profile_picture` now decodes a PNG, JPEG or GIF source file
  completely at plan time, after the dimension check. A file with a valid header
  but cut-short or damaged image data is refused with an error before anything
  is uploaded, where before the plan passed and Pocket ID refused the upload
  during the apply.
- New computed attribute `unresolved_user_ids` on `pocketid_group_members`, null
  for a normal resource. When a request to set the members fails without
  showing whether it was applied (a lost or unreadable answer, a server or proxy
  error, a timeout) and the group then does not show the requested members, or
  cannot be read, the request may still take effect later. The resource now
  keeps its identity, records the members it read, and lists the requested users
  in `unresolved_user_ids`. Plans for it are refused until a refresh reads the
  group and clears the list; destroy reads the group first and removes the
  listed users that are members, or stops with an error and keeps the resource if
  the group cannot be read. Before, such a failure could leave a user that the
  late request added in the group with nothing managing it, or let destroy
  succeed without removing that user. Existing state needs no change and an
  unchanged configuration still plans empty. To give up on a group without
  changing it, run `terraform state rm`.
- **Replace `pocketid_group_members` only by destroying the old resource
  first**, which is Terraform's default order. Do not set
  `create_before_destroy = true` on it, directly or through ordering inherited
  from a resource that depends on it. When the resource is replaced for the same
  group (tainted by a failed create or update, or `-replace`), the provider
  cannot tell the old resource's cleanup from the members the new resource has
  just recorded, so in the other order the old resource's destroy removes users
  the new resource's state still lists. The description and the errors that
  leave a tainted resource behind say so.
- `pocketid_group_members`: a refresh and a destroy each read the group once,
  so they observe a snapshot and neither proves that an earlier request with an
  unknown outcome has finished. The cleanup covers the grants visible at those
  reads, not later commits: a request still pending when a refresh clears
  `unresolved_user_ids`, or when destroy finishes, can be applied afterwards and
  leave the user it adds in the group with nothing managing it.
- `pocketid_group_members`: the promise that destroy keeps members it did not
  add applies to members that are in neither `user_ids` nor
  `unresolved_user_ids`. Pocket ID does not record who made a user a member, so
  a user listed in `unresolved_user_ids` who is a member when destroy reads the
  group is removed, including one that an administrator granted independently of
  the request that failed.
