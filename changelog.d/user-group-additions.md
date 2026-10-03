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
