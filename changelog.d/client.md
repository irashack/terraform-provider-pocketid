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
- `pocketid_client` now rejects `is_public = true` with `pkce_enabled = false`
  at plan time. Pocket ID always turns PKCE on for a public client, so such a
  configuration used to plan a change on every run.
- A public client may now set `requires_pushed_authorization_requests = true`.
  Pocket ID has stored it for public clients since 2.10.0; on an older server
  the apply stops before changing anything.
- `pocketid_client` has a new `is_group_restricted` attribute, and group
  restriction now fails closed. When it is omitted, a client is restricted if
  it has `allowed_user_groups` or is restricted already, so removing a
  client's groups leaves it admitting nobody instead of opening it to every
  user. **Breaking:** to let every user sign in to a client that is
  restricted, set `is_group_restricted = false`; the plan warns when it opens
  a client. `is_group_restricted = true` with no groups (nobody may sign in)
  is now representable and stable, also for clients restricted in the admin
  UI. `is_group_restricted = false` together with groups is an error.
- An `allowed_user_groups` ID that names no group now fails the apply and is
  named in the error (Pocket ID drops such IDs silently); a new client is
  rolled back.
- On Pocket ID 2.17, restricting a client no longer signs out (back-channel
  logout) the users who are about to be allowed: the provider writes the
  groups before it turns the restriction on.
- Changing `is_public` on a `pocketid_client` now works in place: a client
  that becomes confidential gets a secret generated (with
  `generate_secret = true`), and one that becomes public has the secret this
  resource generated revoked. Before, a client made confidential had no
  usable secret.
- `pocketid_client` refuses to import or manage a client registered from a
  Client ID Metadata Document (`client_type = "cimd"`, whose ID is the
  document's URL): that document owns its registration. The
  `pocketid_clients` data source lists such clients. Importing any other ID
  Pocket ID cannot have now fails with a clear message before a request is
  sent.
- `pocketid_client` exposes `description`, `skip_consent`,
  `access_token_duration_minutes` and `refresh_token_duration_minutes`, which
  it used to carry through without showing them. When omitted, the client
  keeps its current values (for example ones set in the admin UI) and state
  shows them; when set, they are managed. `description = ""` removes a
  description. New computed attributes: `has_dark_logo`, `client_type` and
  `pkce_supported`. For existing state they appear on the next refresh; that
  is not a planned change.
- The `pocketid_client` and `pocketid_clients` data sources now also report
  `description`, `skip_consent`, the token lifetimes,
  `requires_pushed_authorization_requests`, `has_dark_logo`, `client_type`,
  `pkce_supported`, `is_group_restricted`, `federated_identities` and the
  client's `secrets` (ID, prefix, creation and expiry time, whether active;
  never a value).
- The `pocketid_client` data source examples used `client_id`, which is not
  an argument of that data source; they now use `id`.
