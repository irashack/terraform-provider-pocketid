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
