- `pocketid_client` has a new `generate_secret` attribute (default `true`).
  With `generate_secret = false` the resource holds no client secret of its
  own (`client_secret` is null), so a confidential client's secrets can be
  managed with `pocketid_client_secret`. Changing it in place works without
  replacing the client: `true` to `false` revokes the secret this resource
  generated, `false` to `true` generates a new one. If the secret in state
  cannot be told apart from the client's other secrets, the apply stops before
  changing anything and lists the client's secrets (IDs and prefixes, never
  values). A client imported, or created before this attribute existed,
  without a secret in state does not get one generated.
- `pocketid_client` has a new computed `client_secret_id`: the ID of the
  secret stored in `client_secret` (Pocket ID 2.14.0 and later). For existing
  state it is filled in on the next refresh when the secret can be identified
  by the four-character prefix Pocket ID keeps of it; this is not a planned
  change.
- A `pocketid_client` deleted outside Terraform is now removed from state on
  refresh, and the next apply creates it again; destroying one that is already
  gone succeeds. Before, every plan failed. Only Pocket ID's own "OIDC client
  not found" answer counts: any other 404 (a wrong base URL, a proxy page)
  remains an error.
- `pocketid_client.client_id` is now always the client's actual ID: it is
  filled in when omitted, on import, and on refresh. **Breaking:** configuring
  a `client_id` different from the existing client's ID now replaces the
  client (Pocket ID cannot change an ID; before, the new value was recorded in
  state while the server kept the old one). With `prevent_destroy` the plan
  fails instead. `client_id` is validated as Pocket ID does: 2 to 128 letters,
  digits, `.`, `_` or `-`. Existing state plans empty whether or not
  `client_id` was configured.
- **Breaking:** `allowed_user_groups` is now a set on `pocketid_client` and on
  the `pocketid_client` and `pocketid_clients` data sources. The order of the
  group IDs no longer shows as a change, so a `sort()` around the list is no
  longer needed (it still works). Expressions that index into it
  (`allowed_user_groups[0]`) must change, for example to
  `tolist(...)[0]` or a `for` expression. Existing state needs no migration.
- `pocketid_client.launch_url` is now left alone when it is not configured:
  an unrelated update no longer clears a launch URL set in the admin UI, and
  plans no longer show it as "known after apply". **Breaking:** removing
  `launch_url` from the configuration no longer removes the URL; set
  `launch_url = ""` to remove it.
- `has_logo` no longer shows as "known after apply" on every update, and
  `logout_callback_urls = []` and `allowed_user_groups = []` no longer show a
  change on every plan; omitting them and setting them to `[]` both mean none.
