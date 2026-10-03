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
  (Pocket ID would silently skip it). It never removes members the plan did
  not show: creating it for a group that already has other members, or applying
  after someone joined since the plan, fails and changes nothing; import the
  group first (`terraform import pocketid_group_members.x <group_id>`) to see
  them in the plan. Do not combine it for one group with
  `pocketid_group_membership`, with `pocketid_user.groups`, or with a second
  `pocketid_group_members`. On Pocket ID 2.17, removing a member can sign that
  user out of group-restricted clients that have a back-channel logout URL.
- New resource `pocketid_user_profile_picture`: sets a user's profile picture
  from a local file (`source`), with a computed `sha256` of the file so that a
  changed file uploads again. Destroying it restores the default picture.
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
  request fails without showing whether it was applied, the group is read once
  and what it holds is recorded. It also refuses a group record that lacks its
  users instead of reading it as an empty group, and takes the same
  provider-wide lock as the other membership writers.
- `pocketid_user_profile_picture` reads the stored picture past any cache
  between the provider and Pocket ID, so its recorded digest and its drift
  checks describe the picture the server holds now. Destroy removes the picture
  only if it is still the one recorded after the provider's last upload
  (`stored_sha256`, which a refresh no longer replaces): a picture replaced or
  removed outside Terraform stops the destroy with an error that changes
  nothing. Apply the configuration again to upload the file and destroy
  afterwards, or run `terraform state rm` to leave the picture as it is. When
  the picture could not be read back after an upload, `stored_sha256` stays null
  and the next plan shows an upload that records it. Pocket ID has no
  conditional delete, so a picture uploaded between the provider's check and
  its delete request is still removed.
- In `pocketid_groups`, each group's `user_count` is now the size of its
  `member_ids`, counted from the same pass over the users, instead of the count
  the group list reported from an earlier request; the two could disagree when a
  membership changed while the data source was read. The groups, users and
  clients are still read in separate passes, so the result is not an atomic
  snapshot of the server.
