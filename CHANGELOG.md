# Changelog

## 3.0.0 — 2026-10-03

The first release of this provider as a project of its own (see
[UPSTREAM.md](UPSTREAM.md)), covering most of Pocket ID's admin API. Supported
Pocket ID servers are 2.14.0 through 2.17.0. It contains deliberate breaking
changes, listed first; state written by 2.4.103 and 2.4.104 keeps working, and
[INSTALL.md](INSTALL.md) has the upgrade steps and what the first plan shows.

### Breaking changes

- **Go importers only:** the module path is
  `github.com/irashack/terraform-provider-pocketid`; configurations are
  unaffected.
- **IDs are checked before use:** user, group, client-secret and SCIM-provider
  IDs must be UUIDs and client IDs follow Pocket ID's rule (2 to 128 letters,
  digits, `.`, `_`, `-`); anything else, such as a mistyped import ID, is
  refused before a request. The `pocketid_group` data source's `id` must be a
  UUID, and with both `id` and `name` set they must name the same group.
  Clients registered from a Client ID Metadata Document cannot be imported,
  looked up by ID or managed. An identifier containing the provider's API key
  is refused before it is used, a request body carrying the key outside a
  secret value is not sent, and an answer carrying it is not used; see
  "Answers Pocket ID did not send as expected".
- **`pocketid_client.allowed_user_groups` is a set** (also on the client data
  sources): `allowed_user_groups[0]` must become `tolist(...)[0]` or a `for`
  expression.
- **`pocketid_client.client_id` replaces the client** when it differs from the
  existing client's ID (with `prevent_destroy` the plan fails).
- **Removing `launch_url` no longer removes the URL;** set `launch_url = ""`.
- **Group restriction fails closed:** a client with groups, or one restricted
  already, stays restricted when `is_group_restricted` is omitted, so removing
  its groups admits nobody; set `is_group_restricted = false` to open it. A
  group ID that names no group fails the apply.
- **`is_public = true` with `pkce_enabled = false` is refused** at plan time.
- **Stricter plan-time validation of users and groups:** `username`, name
  lengths (counted in characters), group `name` (2 to 255) and
  `friendly_name` (2 to 50), custom claims (no empty key or value, no reserved
  name) and a one-time token's `ttl`.
- **Users hold exactly the configured groups and claims:** Pocket ID's signup
  default groups and claims are no longer added to users the provider
  creates; list them if you relied on them. An unknown group ID in `groups`
  or a membership fails instead of being recorded.
- **LDAP-synchronized users and groups:** changing anything Pocket ID would
  silently keep (anything but a user's `locale`, groups and custom claims)
  fails before anything is written.
- **`pocketid_application_config`:** an empty string is refused for settings
  Pocket ID requires or replaces with a non-empty default, and
  `session_duration` must be at least 1.
- **The `pocketid_application_config` data source has no `smtp_password` or
  `ldap_bind_password`;** remove references to them.
- **User data sources report a missing `email` as null,** not `""`.

### Upgrading from 2.4.104

Follow [INSTALL.md](INSTALL.md) ("Upgrade from 2.4.104 to 3.0.0"); run the first
plan with a refresh. What can show up once, and is not a change to Pocket ID:

- New computed attributes (`client_secret_id`, `has_dark_logo`, `client_type`,
  `pkce_supported`, the client settings that were carried through) fill in on
  refresh without a planned change.
- A user that 2.4.x recorded with `""` for an omitted `first_name` or
  `last_name` plans one in-place update to null (also with
  `-refresh=false`); applying it leaves the name empty in Pocket ID. One that
  2.4.x *created* that way is tainted in state and would be replaced once more,
  deleting the account and its passkeys: run `terraform untaint` on it first
  to get the in-place update instead.
- **`-refresh=false` caveats:** an apply from 2.4.x state without a refresh
  shows `generate_secret` going from null to true on every client, an
  in-place update that changes nothing in Pocket ID. An update that would open
  a client restricted in the admin UI since the last refresh (with
  `is_group_restricted` not configured) stops before changing anything and
  asks for a refreshed plan, as does a plan from state in which 2.4.x recorded
  a `client_id` rename that Pocket ID ignored (the refreshed plan shows the
  replacement). A SCIM token, a picture or an image changed outside Terraform
  is only seen by a refresh.

### When a result is uncertain

A change Pocket ID may or may not have made (a lost or unreadable answer, a
server or proxy error, a timeout) is never sent again by itself. What the
resource keeps depends on what the provider could confirm:

- **A create whose answer named no usable object** (lost, unreadable, or
  without an ID the provider can use): nothing is recorded in state, because
  the provider cannot tell what, if anything, it created.
  `pocketid_client_secret`, `pocketid_api` and `pocketid_signup_token` say
  what Pocket ID holds now (secrets by ID and prefix, an API by ID and name;
  signup tokens are listed by the `pocketid_signup_tokens` data source) so
  that you can import or revoke it. A chosen `id` or `client_id` is the
  exception, below.
- **A create whose answer named the new object, but the rest of the answer
  was unusable or a later step failed:** the provider keeps that validated ID
  in state (with nothing else from an unusable answer) and Terraform marks the
  resource tainted, so the next apply replaces it. Examples: a client secret
  whose value or metadata could not be read (the next apply revokes it), a
  signup token not created as requested whose deletion could not be
  confirmed (the next apply deletes it), an API whose permissions or CIMD
  access could not be set, and a user or client whose groups, claims or
  secret could not be set and that was not confirmed removed afterwards.
  When Pocket ID refused such a step outright and the new user or client is
  then confirmed removed, nothing is recorded. An answer that names the new
  object but carries the provider's API key in another value is such an
  unusable answer: a new user's groups are still written and verified (so
  no signup default group stays) before the user is removed, a new group
  is removed, a signup token is deleted, and a client is kept by its ID.
- `pocketid_user` created with a chosen `id`: computed `unresolved_creation`
  is true, and changing, deleting or replacing the user is refused, because a
  user found under that ID may be someone else's. The same holds when
  Pocket ID's answer names another user than the chosen ID: that user is
  never changed or deleted. Check the user, then
  `terraform state rm` the resource and either `terraform import` it with that
  ID (if it is the intended user; the import clears the condition) or choose
  another `id`.
- `pocketid_client` created with a chosen `client_id`: computed
  `unresolved_creation` is true, and the client is handled like such a user:
  a client found under that ID may be someone else's, so it is not adopted,
  and changing, deleting or replacing it is refused (also the destroy half
  of Terraform's replacement of the tainted resource). A refresh that finds
  no client keeps it with a warning. Check the client, then `terraform state
  rm` the resource and either `terraform import` it (if it is yours) or
  choose another `client_id`.
- `pocketid_group_membership`: computed `unresolved_creation` is true. A
  refresh that sees the user in the group clears it; until then a refresh that
  does not keeps the resource with a warning, and a destroy or replacement that
  cannot see the membership is refused. `terraform state rm` gives up on it.
- `pocketid_group_members`: computed `unresolved_user_ids` lists the users the
  request named. Plans are refused until a refresh reads the group and clears
  it; destroy reads the group and removes those users, or stops while the group
  cannot be read. `terraform state rm` gives up on the group without changing
  it. Replace this resource only destroy-first (the default order).
- `pocketid_api_client_access`: the grant is marked unresolved (`unresolved_write`
  in private state) and keeps the last grant it confirmed; plans and writes are
  refused until a read succeeds. Run `terraform plan` or `terraform apply`
  without `-refresh=false`, or `terraform state rm` the resource and import it
  as `<api_id>/<client_id>`.
- `pocketid_client`: a secret generation whose answer was lost is not repeated
  while the client holds a secret the provider cannot account for (the error
  lists the client's secrets by ID and prefix, to revoke); a revocation that
  could not be confirmed is retried by the next apply, after a refresh checks
  whether the secret still exists.

### Answers Pocket ID did not send as expected

The provider now refuses an answer it cannot rely on instead of reading it as
empty or partial: a body that is not the JSON expected, a list or object
without the fields that say what the server holds (a client's or a user's
groups, a group's members, a grant's access), an ID that is not the object
asked for, a JSON document (a federated identity's public key) that repeats a
member name, or a value the provider takes from the answer that contains the
API key, in plain or escaped JSON (a name, an e-mail address, a claim's key or
value, a URL, a setting or a JSON document inside one, a client secret's
prefix, a time in the form the provider stores or prints it, a count). A
create answer for another object than the one asked for (another chosen user
ID, another API resource identifier) is never used for a follow-up request.
The check applies to the decoded values the provider stores, logs or shows,
never to JSON field names or to fields it does not read, so an answer is not
refused because a field name happens to contain the key. You see a fixed
message that quotes nothing from the response, such as "error unmarshaling
response: the response is not the JSON this provider expects", or, after a
change Pocket ID accepted, "the server accepted the change, but its result
could not be read", with a note to inspect the object before trying again.
Nothing from such an answer reaches state, a log line or a diagnostic. The one
exception is the validated ID of an object the answer shows was created (with
a signup token's secret value, which may be valid), kept so that the object
can be recovered ("When a result is uncertain"). Only secret values, in their
own fields, are not checked for the key: a client secret, a SCIM, signup or
one-time token, and the SMTP and LDAP passwords of the application
configuration go only to sensitive state and are never shown. A custom claim
named like one of them is ordinary text and is checked. A server or proxy that
rewrites Pocket ID's answers can therefore make applies fail that used to
pass; that is deliberate.

Your configuration is held to the same rule where the provider handles it:

- an identifier (from configuration, state or an import ID) that contains the
  key is refused before any request, log line or diagnostic uses it;
- a request whose configured text would carry the key outside a secret
  value is not sent;
- no configured text is written to the provider's log, which records only
  IDs that passed their check, kinds and counts; requests are logged by
  method, route (without its query) and status, and the base URL is never
  logged, since it can carry credentials;
- a base URL that contains the API key is refused when the provider is
  configured, and an image or logo file that cannot be used is reported
  without its path or name;
- a JSON document sent as it is (a federated identity's public key) that
  repeats a member name is refused at plan time and never sent;
- plan-time validation messages name the attribute and its rule (and, inside
  a map or set, the nested attribute), never the configured value or a map
  key. The rules of `pocketid_api`'s permissions that the framework would
  check per entry (a name is required, also for a permission set to null;
  the ID cannot be set) are checked on `permissions` as a whole for that
  reason, and an entry that cannot be read is reported on the collection.

Terraform and OpenTofu themselves still show configured values in plans and
in their own messages; that is outside the provider.

### New resources and data sources

Resources `pocketid_client_secret`, `pocketid_client_logo`, `pocketid_api`,
`pocketid_api_client_access`, `pocketid_application_image`,
`pocketid_group_members`, `pocketid_user_profile_picture`,
`pocketid_signup_token` and `pocketid_scim_sync`; data sources `pocketid_api`,
`pocketid_apis`, `pocketid_api_keys`, `pocketid_current_user`,
`pocketid_signup_tokens`, `pocketid_user_passkeys` and `pocketid_version`.
Each is described in its section below. The release archives carry
`THIRD_PARTY_NOTICES.md` for the code adapted from Pocket ID,
go-playground/validator and the URL-pattern libraries.

### Clients and client secrets

- `pocketid_client` has a new `generate_secret` attribute (default `true`).
  With `generate_secret = false` the resource holds no client secret of its
  own (`client_secret` is null), so a confidential client's secrets can be
  managed with `pocketid_client_secret`. Changing it in place works without
  replacing the client: `true` to `false` revokes the secret this resource
  generated, `false` to `true` generates a new one. If the secret in state
  cannot be told apart from the client's other secrets, the apply stops before
  changing anything and lists the client's secrets (IDs and prefixes, never
  values). A client imported, or created before this attribute existed,
  without a secret in state does not get one generated. If creating a secret
  may have succeeded but its answer was lost, the next apply does not create
  another while the client has a secret the provider cannot account for; it
  lists such secrets (IDs and prefixes) so that you can revoke them.
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
  `client_id` was configured. If an earlier version recorded such an ignored
  change in state, a plan without a refresh now fails and asks for a
  refreshed plan, which shows the replacement.
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
  usable secret. A client that was made public with an earlier provider
  version, and still has the secret that version kept in state, keeps it:
  upgrading plans no change. A revocation that fails is retried by the next
  apply.
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
- An update planned without a refresh (`-refresh=false`) no longer opens a
  client that was group-restricted in the admin UI after the last refresh
  when `is_group_restricted` is not configured; the apply stops before
  changing anything and asks for a refreshed plan.
- On Pocket ID 2.14, the `pocketid_clients` data source reports
  `allowed_user_groups` as null for every client: that version's client list
  gives only the number of allowed groups. Use the `pocketid_client` data
  source for a client's groups there.
- New resource `pocketid_client_secret` (Pocket ID 2.14.0 or later): one
  secret of an OIDC client, so a secret can be rotated without replacing the
  client. Pocket ID generates the value (stored in state as the sensitive
  `secret`), or you supply it through the write-only `secret_wo` with
  `secret_wo_version` (Terraform or OpenTofu 1.11 or later), in which case
  nothing secret is stored in state. Optional `expires_at`. Rotate by
  replacing the resource with `create_before_destroy = true`: the new secret
  is created before the old one is revoked. The resource never touches the
  client's other secrets, refuses clearly when a client already holds Pocket
  ID's maximum of 20 secrets, and lists the client's secrets (IDs and
  prefixes) if a create fails with an uncertain outcome. Import with
  `<client_id>/<secret_id>`; the value of an imported secret is unknown.
  Answers from Pocket ID that do not fit its format (a secret list with an
  entry that has no usable ID, a prefix that is not the first four characters
  of the value) are refused with a fixed message rather than stored, printed
  or taken as proof that a secret is gone. This includes the secret list that
  `pocketid_client` reads when it revokes the secret Pocket ID 2.17 creates
  with a client: if revoking that secret cannot be confirmed because the list
  is unusable, the client's own secret is not generated beside it.
  Within one apply, `pocketid_client_secret` and `pocketid_client` take turns
  on a client's secrets (creating, revoking, refreshing, and deleting or
  cleaning up the client), so an uncertain create never names the other
  resource's secret as its own; another apply or a change in Pocket ID's
  interface at the same moment is not covered.
- New resource `pocketid_client_logo`: the light or dark logo of an OIDC
  client (`variant = "light"` or `"dark"`), uploaded from a local file
  (`source`, with a computed `sha256`). The file is uploaded again when its
  content changes, when only its extension changes (Pocket ID takes the image
  type from it), and when the logo was removed or replaced outside Terraform.
  Logo reads bypass any cache in front of Pocket ID, so a cached copy neither
  hides a change nor reports a false one. Files Pocket ID would refuse (an
  unsupported extension, more than 2 MiB, a JPEG or PNG with more than 16
  million pixels) are refused at plan. Destroying the resource removes that
  logo. Import with `<client_id>/light` or `<client_id>/dark`; the first apply
  after an import uploads the file once.
- New computed attribute `unresolved_creation` on `pocketid_client`: when a
  create with a chosen `client_id` ends without a definite answer, the
  client is kept as an unresolved creation instead of being adopted (see
  "When a result is uncertain" above). Before, the provider adopted a client
  it found under that ID, which might have been another actor's, and the
  replacement of the tainted resource deleted it. Existing state reads it as
  null and plans no change.
- A single client's answer must list its allowed groups (`allowedUserGroups`,
  which Pocket ID 2.14.0 to 2.17.0 always send, as `null` or `[]` when there
  are none). An answer without it is an error instead of an empty set, which
  could have confirmed a group change that was not made or let an update
  skip a group write it needed.

### Users, groups and membership

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
  adding its default groups; with no groups planned, the provider writes an
  empty group list right after the create and checks it, whatever the create
  answer showed, and it replaces the default claims (which Pocket ID always
  adds) the same way. If your instance has signup defaults and you relied on them for
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
  replace that user, at plan time and at apply time, and Terraform's planned
  replacement of the tainted resource fails at its destroy step instead of
  deleting the user. The condition is the new computed attribute
  `unresolved_creation` (true then, null for every other user, including every
  user in state from an earlier provider version, which plans no change). Check
  the user, then run `terraform state rm` on the resource and either
  `terraform import` it with that ID (if it is the intended user; importing
  clears the condition) or choose another `id`. A refresh that finds no user
  with that ID (Pocket ID's own "user not found") no longer removes the
  resource while the creation is unresolved, because the create may still
  commit; it keeps the resource and warns, and only `terraform state rm`
  settles it. An ordinary user that is gone is still removed from state.
- `pocketid_group_membership`: when adding the user was accepted, or may have
  been, but the result cannot be confirmed (an unreadable response and a
  failed check, a server error or a lost connection), the membership is now
  kept in state (marked for replacement) with an error saying so, so that
  removing it from the configuration still revokes it. Before, nothing was
  recorded and such a membership could stay active unnoticed. Because the
  request can still take effect after a refresh that sees the old group list,
  the pair is also marked by the new computed attribute `unresolved_creation`
  (true then, null otherwise, including for every membership in state from an
  earlier provider version, which plans no change): while it is set, a refresh
  that finds the user outside the group, or the user missing, keeps the
  resource with a warning instead of removing it, and a destroy or replacement
  that cannot see the membership is refused instead of recorded as done. A
  refresh that sees the user in the group clears it. To give up on the
  membership, run `terraform state rm` on the resource. A failure before the
  write was sent (the read of the user's groups that precedes it failed) is an
  ordinary error that records nothing, and the next apply creates the
  membership normally.
- Reading a user's groups (to add or remove a `pocketid_group_membership`, to
  refresh one, or to check a write whose response could not be read) now
  requires a response that names the user and lists its groups. An empty or
  malformed answer is an error instead of "no groups", which could have made a
  removal look done, dropped a membership from state, or rebuilt the user's
  group list without the groups it had. The same rule applies to Pocket ID's
  answer to the write itself: a response that names another user or none, or
  lists a group without an ID, is not taken as the result, and the groups are
  read back instead.
- For a user synchronized from LDAP, only attributes the configuration
  actually changes are refused. A change to `locale`, `groups` or
  `custom_claims` is applied, and a `display_name` that is not configured
  keeps the directory's value instead of being replaced by first and last
  name.
- The error for a refused deletion of an LDAP user no longer suggests setting
  `disabled = true` (Pocket ID ignores that for LDAP users); it points to
  removing the user in the directory and running an LDAP sync, or removing it
  from state.
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
  around each read, write and verification, and `pocketid_group_membership`
  and `pocketid_user` take the same lock around theirs, so within one apply
  the three never overwrite each other's membership changes. The lock
  coordinates one provider process only, not another Terraform run or
  anything outside Terraform.
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
  group (a failed create leaves it tainted, or `-replace`), the provider
  cannot tell the old resource's cleanup from the members the new resource has
  just recorded, so in the other order the old resource's destroy removes users
  the new resource's state still lists. A failed update does not taint the
  resource: the corrected configuration applies in place. The description and
  the errors that keep a resource after a failed create or update say so.
- `pocketid_group_members`: a refresh reads the group once, and a destroy reads
  it before it writes (the initial snapshot) and, if its own removal request
  fails without showing whether it was applied, once more to verify. None of
  those reads proves that an earlier request with an unknown outcome has
  finished. The cleanup covers the grants visible at those reads, not later
  commits: a request still pending when a refresh clears
  `unresolved_user_ids`, or when destroy finishes, can be applied afterwards and
  leave the user it adds in the group with nothing managing it.
- `pocketid_group_members`: the promise that destroy keeps members it did not
  add applies to members that are in neither `user_ids` nor
  `unresolved_user_ids`. Pocket ID does not record who made a user a member, so
  a user listed in `unresolved_user_ids` who is a member when destroy reads the
  group is removed, including one that an administrator granted independently of
  the request that failed.
- `pocketid_group_membership` and `pocketid_user` now take the same lock as
  `pocketid_group_members` (see above) for the whole read, write and check of
  a user's groups, in place of a lock per user that did not exclude the other
  resources. A signup token whose create answer lists a group with an
  unusable ID is deleted again, like one with the wrong groups.

### Application configuration and images

- `pocketid_application_config` now sends back every setting Pocket ID reports,
  including settings this provider version does not know yet, unchanged. A
  Pocket ID release that adds a required setting no longer makes every update
  of the application configuration fail, as 2.17.0 did with
  `autoCreateOidcClientSecret`.
- `pocketid_application_config` checks every value at plan time with Pocket
  ID's own rules: `"true"`/`"false"` settings, the fixed choices (for example
  `allow_user_signups`, `smtp_tls`, `webauthn_user_verification`), the
  1-to-30-character `app_name`, a whole-number `session_duration`, the JSON
  formats of `signup_default_user_group_ids`, `signup_default_custom_claims` and
  `cimd_url_allowlist` (each URL pattern compiled as Pocket ID compiles it), and
  a plain e-mail address in `smtp_from`. A value
  Pocket ID would refuse now fails the plan instead of the apply.
- **Breaking:** an empty string is refused at plan time for settings Pocket ID
  requires, and for settings whose empty value Pocket ID replaces with a
  non-empty default (`accent_color`, `signup_default_user_group_ids`,
  `signup_default_custom_claims`, `cimd_url_allowlist`, `ldap_user_search_filter`,
  `ldap_user_group_search_filter`, `ldap_attribute_user_display_name`,
  `ldap_attribute_group_member`). Such a configuration could never apply
  cleanly; set the default the message names, or omit the attribute.
- **Breaking:** `session_duration` must be at least 1 minute. Pocket ID accepts
  0 or a negative number, which ends every session at once and locks every
  user, administrators included, out of the web interface.
- The `signup_default_custom_claims` documentation now gives the format Pocket
  ID requires: a JSON array of `{"key": ..., "value": ...}` objects, not a JSON
  object. Every attribute's documentation names its accepted values and Pocket
  ID's default.
- New attribute `auto_create_oidc_client_secret` on `pocketid_application_config`
  and its data source (Pocket ID 2.17.0 and later). On an older server it is
  null, and configuring it is refused at plan time.
- An update of `pocketid_application_config` no longer shows every setting you
  did not configure as "(known after apply)": the plan shows them unchanged.
  Settings you did not configure are still sent back with the value Pocket ID
  holds at apply time, so a change made outside Terraform since the plan is
  kept. Pocket ID replaces the whole configuration on every update and offers
  no conditional write, so a change an administrator makes in the moment
  between the provider reading the configuration and writing it back (a
  password rotation included) can still be overwritten. After an update, the provider checks that Pocket ID stored each value
  it changed, and fails naming the setting if it did not.
- New write-only inputs `smtp_password_wo` / `smtp_password_wo_version` and
  `ldap_bind_password_wo` / `ldap_bind_password_wo_version` on
  `pocketid_application_config` (Terraform 1.11+ or OpenTofu 1.11+). The
  password never enters plan or state and can come from an ephemeral value. It
  is sent when the resource is created and whenever the `_wo_version` string
  changes; every other update sends the password Pocket ID holds back
  unchanged. Moving from `smtp_password` to `smtp_password_wo` sends the new
  value in the same update and removes the password from state. Out-of-band
  changes to a password managed this way are not detected.
- `pocketid_application_config` no longer copies a password you did not
  configure into state: `smtp_password` and `ldap_bind_password` are null
  unless you set them (or state from an earlier version already holds them),
  also after `terraform import`. A configured password is still refreshed and
  shown as a change when it differs from the server's.
- **Breaking:** the `pocketid_application_config` data source no longer has
  `smtp_password` or `ldap_bind_password`. Remove references to them. After
  upgrading, `terraform show` cannot read a stored result of this data source
  until the next apply (or `apply -refresh-only`) rewrites it; plans and
  applies are unaffected.
- New resource `pocketid_application_image` uploads one of Pocket ID's
  application images (`logo_light`, `logo_dark`, `email_logo`, `background`,
  `favicon`, `default_profile_picture`) from a local file. Its computed
  `sha256` follows the file's content, so a changed file is uploaded again (as
  is the same file under another extension, since Pocket ID serves the type the
  file name gives), and an image replaced or removed outside Terraform is shown
  by the next plan and uploaded again. The file type must be one Pocket ID accepts for that image
  (checked at plan time). Destroying the logos, the background or the default
  profile picture removes them; Pocket ID cannot remove the e-mail logo or the
  favicon, so destroying those leaves the image in place, with a warning.
  Uploads are sent once, never retried, and limited by the provider to 10 MiB
  and, for JPEG and PNG, to 16 million pixels on every server version (Pocket
  ID 2.15.0 and later refuse larger JPEG and PNG images themselves; 2.14.0
  does not).
- `pocketid_application_config` refuses an update, before sending anything,
  when Pocket ID's current configuration leaves out a setting the update would
  have to send back unchanged (a password among them), or lists one without a
  string value. Sending an empty value would have reset that setting, clearing
  a password. A response that leaves out a setting the update sent is reported
  as an error.
- `pocketid_application_image` reads each image with a URL of its own (a
  random `nocache` query parameter) and asks caches to revalidate
  (`Cache-Control: no-cache`, `Pragma: no-cache`), so a cache in front of
  Pocket ID (which allows caching images for 15 minutes, and serving them
  stale for a day) cannot hand back an older copy after an upload or during
  a refresh, as long as it keys on the whole query string or honors those
  headers, as most do by default. If your cache does neither, exclude
  `/api/application-images/` from caching for the address the provider uses;
  otherwise the provider can record an older image and upload yours again
  later. Image reads, like other reads, are retried after a transient failure
  (each attempt with a new `nocache` value); the upload or deletion before
  them never is. A read shows what Pocket ID served at that moment, not
  proof that nothing replaced the image meanwhile.

### SCIM, signup tokens and API keys

- `pocketid_scim_service_provider` is removed from state when Pocket ID reports
  that the SCIM service provider (or its client) no longer exists, so the next
  plan creates it again. Before, a refresh failed with an error until the
  resource was removed from state by hand. Destroying a SCIM service provider
  that is already gone now succeeds. Any other failure, including a 404 that
  Pocket ID did not send, is still an error.
- A `token` on `pocketid_scim_service_provider` that was cleared or changed
  outside Terraform now shows as a change on the next plan. Before, the old
  value stayed in state and nothing was repaired. A configuration without a
  `token`, or with `token = ""`, still plans empty; note that the configuration
  is authoritative, so a token set in the Pocket ID interface on a provider
  whose configuration has none is shown as a change that removes it.
- New `token_wo` and `token_wo_version` on `pocketid_scim_service_provider`: a
  write-only variant of `token` that is sent to Pocket ID but never stored in
  the plan or the state, so it can come from an ephemeral value. The token is
  sent when the resource is created and whenever `token_wo_version` changes;
  every other update keeps the token Pocket ID already holds (Pocket ID would
  otherwise clear it); the update is refused, and nothing is sent, if Pocket
  ID's read of that token fails or does not return this provider with a token. Because the state holds no token, a token changed or
  cleared outside Terraform is not detected: change `token_wo_version` to send
  it again. `token_wo` conflicts with `token` and needs `token_wo_version`.
  Write-only attributes need Terraform or OpenTofu 1.11 or later; `token`
  keeps working everywhere.
- Importing a `pocketid_scim_service_provider` that your configuration manages
  with `token_wo`: import with `<client_id>,token_wo_version=<version>` (the
  version your configuration uses) so the refresh after the import never writes
  the token into the state. The ordinary import ID, `<client_id>`, is for
  configurations that use `token`; its first refresh stores the bearer token
  Pocket ID holds in the state, as before.
- New resource `pocketid_scim_sync`: runs a SCIM synchronization of one
  `pocketid_scim_service_provider` during the apply, like `pocketid_ldap_sync`
  does for LDAP. It is created once, and replaced (so it syncs again) when its
  `triggers` map or `service_provider_id` changes. Pocket ID runs the sync
  inside the request, so the apply waits for it and fails if the SCIM endpoint
  fails; the request is never repeated automatically. A sync that takes longer
  than the provider's `timeout` (30 seconds by default) fails the apply with a
  message that its outcome is unknown: raise `timeout`, and check
  `last_synced_at` on the service provider.
- New resource `pocketid_signup_token` and data source `pocketid_signup_tokens`
  for Pocket ID's signup tokens (people register with them; optionally into
  groups). A token's inputs (`ttl`, `usage_limit`, `user_group_ids`) cannot be
  changed, so changing one replaces the token. The token value is stored in the
  state as a sensitive value. Pocket ID deletes a token when it expires, and
  cannot tell that from an administrator deleting it: such a token stays in the
  state with `expired = true` and the next plan does not create a new one. Use
  `-replace` for a fresh token. Pocket ID silently ignores a group ID that
  names no group; the provider fails, deletes the token it just made (checking
  Pocket ID's list that it is gone) and records nothing. If that deletion
  cannot be confirmed, the token's ID stays in the state, tainted, so the next
  apply deletes it, and the error says the token may be valid until it expires. The data source lists the valid tokens with their
  ID, times, limits, use count and groups. Token values are deliberately not
  exposed by the list: Pocket ID's list includes each token's value, and
  copying every outstanding token, including ones created outside Terraform,
  into the state would let anyone who can read it register accounts. The value
  of a token Terraform created is the `token` attribute of
  `pocketid_signup_token`.
- New data source `pocketid_api_keys`: lists the API keys of the Pocket ID user
  who owns the key the provider uses (name, description, and creation, expiry
  and last-used times; never a key value). Its purpose is a `check` block that
  warns before the provider's own key expires (see the example, which compares
  with `plantimestamp()` so that the warning also shows on a plan that is not
  applied). Creating,
  renewing and revoking keys is deliberately not offered. If the provider uses
  Pocket ID's static API key (`STATIC_API_KEY`), the list is empty.

### APIs

- New resource `pocketid_api` (Pocket ID 2.14.0 or later) manages an API: a
  protected resource clients request access tokens for with its `resource`
  identifier, its permissions (a map keyed by permission key, each with a
  stable `id`) and its CIMD access (`allow_cimd_clients` and each
  permission's `allowed_for_cimd_clients`, both off unless set). Editing a
  permission's name or description keeps every client's grant of it;
  removing or renaming a key deletes that permission and its grants.
  Changing `resource` replaces the API, and deleting or replacing an API
  removes every client's access to it. Values Pocket ID would refuse or
  store differently (a trailing slash, a reserved or malformed permission
  key, a name that is too long) are refused at plan time, and an API whose
  `resource` another API already holds is refused before anything is
  written; import that one instead. If a create ends without a definite
  answer from Pocket ID, nothing is recorded in state, because an API that
  holds the identifier afterwards may be someone else's; the error names it
  (ID and name) so you can import it if it is yours.
- New resource `pocketid_api_client_access` (Pocket ID 2.14.0 or later)
  grants one OIDC client access to one API: user-delegated access (tokens on
  behalf of a signed-in user) and client access (client credentials), each
  with a set of permission keys. It writes only its own (API, client) pair,
  so it never touches the client's other grants or other clients' grants.
  Before writing it checks that every permission key exists on the API and
  that a client given client access is not public, because Pocket ID drops
  both without an error; after writing it compares the stored grant with
  the configuration and fails, naming the difference, instead of recording
  access the server did not confirm. Leaving `user_delegated_access` or
  `client_access` unset makes it follow its permissions; setting it to
  `false` while listing permissions, or granting nothing, is refused at plan
  time. Import with `<api_id>/<client_id>`. Clients registered through a
  Client ID Metadata Document cannot be addressed; give them access on the
  API with `allow_cimd_clients`. If a write ends without a confirmed result
  (the connection failed, or the response was lost) and the grant cannot be
  read back either, the resource keeps the last grant it confirmed instead of
  recording "no access", is marked unresolved, and refuses plans and writes
  until a read succeeds: run `terraform plan` or `terraform apply` without
  `-refresh=false`, or `terraform state rm` the resource and import it again.
  A grant whose creation ended that way is recorded without its access flags
  and permissions until a refresh reads them, and Terraform marks it tainted:
  the marker does not apply there, because the replacement Terraform plans for
  a tainted resource first revokes whatever grant the server holds for the pair
  and then writes the configured grant again. An answer from Pocket ID that
  does not describe the grant (empty, `null` or missing fields) is handled
  the same way instead of being read as "no grant": a write's answer
  triggers the read-back, and a grant list with such an entry fails the
  refresh rather than dropping the resource from state.
- Identifiers in Pocket ID's answers about APIs and grants are checked before
  anything uses them: an ID that is not a UUID, is not the one asked for, or
  contains the provider's own API key is refused, as is an API name,
  resource identifier, creation time or permission text that contains the key. The error
  is fixed text that never repeats the value, and nothing from that answer
  reaches a diagnostic, the state or a data source. An answer that cannot be
  decoded is reported the same way, without Go's decoding error, which can
  quote a number from the response.
- Identifiers you give the provider are checked the same way before anything
  uses them: an API ID in an import ID, in state or in configuration, and an
  OIDC client ID in an import ID, in state or in configuration, must be valid
  and must not contain the provider's own API key. Such an identifier is
  refused with a fixed message that does not show it, before any request is
  sent, and a diagnostic prints an identifier from state only after it passes
  the check. For a grant this includes the combined ID `<api_id>/<client_id>`,
  which a key can span without being in either half: create and update refuse
  it before any request, read refuses it without changing state, and a delete,
  which needs only the two checked halves, still works.
- Text that contains the provider's own admin API key is deliberately
  unsupported in a `pocketid_api`: its `name`, `resource`, and each permission
  key, `name` and `description`. Pocket ID would accept such a value, but the
  provider never stores that credential in state or prints it, so it refuses
  the value at plan time (and again before creating or updating) with a
  message that does not show it, and nothing is sent. The `resource` lookup of
  the `pocketid_api` data source refuses such a value too, and so does a
  permission key in `pocketid_api_client_access`.
- New data sources `pocketid_api` (look up one API by `id` or by its exact
  `resource` identifier) and `pocketid_apis` (every API, oldest first, read
  across all pages), each with the API's permissions keyed by permission key
  and its CIMD access.
- Deleting an API in Pocket ID removes every client's access to it, including
  access granted without permissions; a `pocketid_api_client_access` for it
  disappears from state on the next refresh.

### Connections, identifiers and errors

- **Breaking (Go importers only):** the Go module is now
  `github.com/irashack/terraform-provider-pocketid`. Terraform and OpenTofu
  configurations are unaffected; the registry address
  `registry.terraform.io/irashack/pocketid` is unchanged.
- The `pocketid_clients` and `pocketid_groups` data sources, and the
  `pocketid_group` data source's lookup, now see every client and group.
  Before, they saw only the first 20 the server returned.
- Removing `allowed_user_groups` from a `pocketid_client` now applies. Before,
  the update failed with HTTP 400 after the client itself had been updated.
- Cancelling a run (Ctrl-C, or Terraform stopping the provider) now stops the
  provider's requests at once. If a create is interrupted after Pocket ID
  created the object, the object's ID stays in state and nothing is cleaned up
  automatically; inspect it before applying again.
- A read, its retries included, ends after 30 seconds or the provider's
  `timeout` setting, whichever is longer, even if an attempt is still waiting
  for the server. Reads that fail with a rate limit or a server error are
  retried within that time, never waiting more than 10 seconds between
  attempts (at most four attempts, each a single request on a fresh
  connection); if the server asks the provider to wait longer, the error is
  returned at once. Changes (create, update, delete) are sent once and are
  bounded by the `timeout` setting alone.
- **Breaking:** IDs of users, groups, client secrets and SCIM service
  providers must be UUIDs, and OIDC client IDs must follow Pocket ID's rule
  (2 to 128 letters, digits, `.`, `_` or `-`). Any other value, for example a
  mistyped import ID, is refused with an error before a request is sent.
  Clients created from a Client ID Metadata Document (whose ID is a URL)
  cannot be looked up by ID. The ID Pocket ID returns when it creates an
  object must be a UUID, or exactly the `client_id` you set; any other value
  is reported as an error (the object may exist) and never used or logged.
- Lists of users, groups and clients are read in creation order. If the pages
  do not add up because the list changed while it was being read (an object
  appears twice, or the count differs from Pocket ID's total), it is read once
  more and then reported as an error. Pocket ID offers no snapshot of a list,
  so a list read while objects are being deleted and created at the same
  moment can still miss one; read it again when nothing else is changing it.
- A one-time access token response that contains no token is now reported as
  an error saying a token may have been created, instead of storing an empty
  token.
- When Pocket ID accepts a change but its response cannot be read (too large,
  or the connection drops while it arrives), the error now says the change was
  made and only its result is unknown. The provider never sends such a change
  again by itself.
- Responses larger than 16 MiB (64 KiB for error responses) are refused
  without being read in full. Error messages and the provider's logs never
  include response content: not the body, not the server's reason phrase, and
  not the text of a malformed response (a fixed description is given instead).
- The provider opens a new connection for every request to Pocket ID instead
  of reusing one. This keeps anything a server sends outside a response out of
  the logs, and makes each attempt exactly one request; the cost is one TCP
  (and TLS) handshake per request, which is small next to a Terraform run.
- The ID of an object Pocket ID creates, and the ID of every object in a list
  the provider reads, is checked before the provider uses it: it must have a
  form Pocket ID itself can give an object, and it must never contain the
  API key. A list that holds an ID no Pocket ID object can have fails as a
  whole; clients whose IDs Pocket ID allows but this provider cannot address
  (such as metadata-document clients) are still listed. Such a response is
  reported as an error that does not include the value.
- The same check now covers every ID in a read or an update answer, nested
  ones included (a user's groups, a group's members, a client's allowed
  groups and listed secrets, a SCIM provider's client when Pocket ID names
  it, the application configuration's signup default groups, passkeys,
  signup tokens, API keys): an object the request named must come back as
  that object. A UUID may come back in another letter case (PostgreSQL
  answers an upper-case request with the lower-case ID) and is the same
  object; an OIDC client ID must come back exactly. Where the provider sends
  a later request for the object (the SCIM token kept on an update, a user's
  groups written after reading them, an API's updates), it addresses the ID
  the server returned, and state records the server's spelling for client
  secrets, signup tokens and APIs read in another case. Lookups that compare
  an ID you hold with a listed one (is a secret still listed after its
  revocation, is a user in a group) compare UUIDs without regard to case, so
  a revocation or a removal is never taken as done because of the case alone.
- Setting up a connection to Pocket ID now ends with the request: when a run
  is cancelled or the provider's `timeout` passes, a connection still waiting
  for the server's TLS handshake is closed at once. Connecting is also limited
  to 30 seconds and the TLS handshake to 10 seconds whatever the `timeout`.
- An ID from your configuration, state or an import that contains the
  provider's API key (for example an import ID pasted from the wrong field) is
  refused with an error before any request is made, and is never written to
  a URL, the provider's logs or the error. The same applies to search terms
  and other request parameters.
- The server's version (from `/api/version/current`) is refused if it contains
  the provider's API key, so it never reaches `pocketid_version` or a
  diagnostic.
- The same rule now covers text, not only IDs: an answer from Pocket ID
  whose decoded values contain the provider's API key in any field the
  provider stores, logs or shows (a username, a name or e-mail address, a
  custom claim's key or value, a group, API-key or passkey name, a URL, a
  setting, a time, a count), in plain or escaped JSON, is not used; the error
  is fixed text, and after a change Pocket ID accepted it says the result
  could not be read. JSON field names are never taken for data, so a key
  that happens to equal one does not make answers unusable. A create whose
  answer names the new object with a usable ID but carries the key elsewhere
  keeps only that ID, so the user's groups are still corrected and the user
  or group rolled back, a signup token deleted, and a client kept for
  recovery. A request whose configured text would carry the key (a name, a
  claim, a URL, a setting, ...) is not sent. Only secret values are exempt,
  and only in their own fields: a client secret, a SCIM, signup or one-time
  token and the SMTP and LDAP passwords of the application configuration go
  only to sensitive state and are never shown, so the provider sends and
  accepts them as they are; a custom claim named like one is ordinary text.
- An identifier a request carries in its body (the groups of a user or a
  client, a group's members, a signup token's groups, a chosen user or client
  ID, a SCIM provider's client) is refused before anything is sent if it
  contains the provider's API key, so no later error can name it. An
  identifier from your configuration, state or an import ID is checked the same
  way, together with the ID joined from it in state (`<group_id>/<user_id>`,
  `<client_id>/<secret_id>`, `<client_id>/light`), before any request or
  diagnostic, by every resource that is given one and by the user, group,
  passkeys and client data sources' lookups. Such a value is refused with a
  fixed message that does not show it. Custom-claim keys and the members of a
  `pocketid_group_members` or a signup token's `user_group_ids` that break
  Pocket ID's rules are reported on the attribute as a whole, without naming
  the offending key or element.
- Every plan-time validation message names the attribute and its rule and
  never the configured value: Terraform's and this provider's validators
  alike, for strings, numbers, lists, sets and maps, nested attributes
  included. A map key or set element is never named; a message about an
  attribute inside one names that attribute and the collection ("name in
  permissions"). Some messages read differently as a result (for example a
  JWK, a URL or a setting now names the rule it breaks rather than quoting
  the value). An identifier from state (a user, group, client, secret, SCIM
  provider or signup token ID, a user's groups) is checked before any log
  line, diagnostic or request uses it, and the provider's log records no
  configured text, only checked IDs, kinds and counts.

## 2.4.104 — 2026-10-02

Pocket ID 2.17.0 support. On 2.17.0, 2.4.103 cannot update the application
configuration, leaves a second valid secret on every confidential client it
creates, and clears a client's back-channel logout URL on every client update.
Supported servers are now Pocket ID 2.17.0 and 2.16.0. One new optional attribute;
2.4.103 state plans empty.

- **Application configuration:** 2.17.0 requires `autoCreateOidcClientSecret` in
  every configuration update, so each `pocketid_application_config` create or
  update failed with HTTP 400. The setting is now read with the rest of the
  configuration and sent back unchanged. It is not an attribute. A server that
  does not report it (before 2.17.0) is not sent it.
- **Client creation:** with that setting on (the default), 2.17.0 generates a
  secret for each new confidential client and returns it once. The provider
  generated a second one and stored only that, so the server's secret stayed
  valid without Terraform knowing it existed. The provider now revokes the
  server's secret, by ID and before generating its own, so a new client has
  exactly one secret: the one in state. The revoke is never retried. If it
  fails, a read of the client's secrets decides: a secret confirmed gone counts
  as revoked; otherwise a rejected revoke rolls the new client back, and an
  ambiguous one keeps the client ID in state and says the secret, named by ID,
  may still be valid. The secret's value is never decoded, stored or reported.
- **Rollback verification of a half-created client:** when the cleanup DELETE
  after a failed creation step itself fails, the client now counts as gone only
  on Pocket ID's own not-found error for an OIDC client (`code: "not_found"`,
  `details.resource: "OIDC client"`, the same from 2.14.0 to 2.17.0). Earlier
  releases accepted any 404, so a proxy's or a wrong path's 404 while the client
  still existed dropped its ID from state and orphaned it, with any secret
  already generated. Now the ID, and that secret, stay in state and the error
  says the client's absence could not be confirmed.
- **Clients created by 2.4.103 or earlier on 2.17.0 keep their extra secret.** This
  release does not remove it. In the admin UI, revoke the secret whose prefix
  does not match the start of the client's `client_secret`.
- **New `backchannel_logout_url`** on `pocketid_client` (Pocket ID 2.17.0 or later),
  also exposed by the `pocketid_client` and `pocketid_clients` data sources. Pocket
  ID posts an OpenID Connect Back-Channel Logout token to it when a user's access
  to the client is revoked. Like `logout_callback_urls`, the attribute is
  authoritative: omitted means no URL, and an empty server value reads as null.
  It must be an absolute http or https URL without a fragment, and https for a
  public client; both are checked at plan time. A value on an older server is
  refused before any change. Pocket ID replaces a client in full on update, so
  2.4.103 cleared a URL set in the admin UI on every client update; now an update
  that does not change the attribute sends back the server's current value, even
  when planned without a refresh.
- **Upgrading with a URL already set in the admin UI:** the first refreshed plan
  after upgrading proposes to remove it. Add it to the configuration first.
- **Pocket ID 2.17.0 behaviour to know about:** it sends logout tokens, to
  clients that have a back-channel logout URL, after changes this provider can
  make: disabling or deleting a user, removing a user from a group (including
  through `pocketid_user.groups` and `pocketid_group_membership`), deleting a
  group, changing a client's allowed groups, and deleting a client.
- `make test-acc-matrix` covers Pocket ID 2.14.0, 2.15.0, 2.16.0 and 2.17.0, now
  including the client data-source test. `make test-acc-provider` runs the
  provider and data-source acceptance suites together; CI runs it on 2.16.0 and
  2.17.0. Data-source acceptance was previously not in any target or CI job. Upstream's #122 is adapted; #117 is not
  ported because the fork has no in-place secret rotation. See
  [UPSTREAM.md](UPSTREAM.md).

## 2.4.103 — 2026-09-23

Adds a non-authoritative group membership resource, for a group whose members
are partly managed outside Terraform (for example, a self-service onboarding
broker). No schema change to any existing resource or data source; 2.4.102
state plans empty.

- New resource `pocketid_group_membership` manages a single `(group_id, user_id)`
  pair. Create adds the user to the group without touching any other member;
  Delete removes only that user. Both attributes require replacement. Import
  uses `<group_id>/<user_id>`.
- **API mechanism:** Pocket-ID exposes no endpoint to add or remove a single
  group member; the only mutating endpoint is `PUT /api/users/{id}/user-groups`,
  which replaces a user's entire group list. The new resource performs a
  read-modify-write against that endpoint (`Client.AddUserToGroup` /
  `RemoveUserFromGroup` / `UserHasGroupMembership` in `internal/client`): it
  reads the user's current groups, adds or removes the target group, and
  writes the full list back.
- **Concurrent applies of this provider's own resources are serialized, and
  are the intended use case:** adding one user to several groups (a real
  target is around 15) in a single `apply` dispatches each
  `pocketid_group_membership` Create concurrently — Terraform's default
  parallelism is 10 — and two unsynchronized read-modify-write cycles for the
  same user race: the second `PUT` can be built from a snapshot taken before
  the first lands, silently dropping the first addition. Reproduced live
  (`TestAccResourceGroupMembership_manyGroupsOneApply` failed reliably without
  the fix) and fixed by serializing Create/Delete per user ID within the
  provider process (a package-level map of mutexes in
  `internal/resources/group_membership_resource.go`, since the framework does
  not guarantee the same Go value handles every call for one resource type).
  A writer *outside this provider process* — another Terraform run, or an
  external process such as an onboarding broker — racing the same read then
  write is a separate, remaining window with no fix possible from the client
  side (Pocket-ID has no compare-and-swap primitive); this is documented on
  the resource.
- Read and Delete only treat a *positively confirmed* missing user as the
  membership being gone (dropping it from state, or from Read, and treating
  Delete as already-satisfied). Confirmation means Pocket-ID's own
  structured `user_not_found` error code on a `GET /api/users/{id}` — found
  by reading the pinned v2.14.0/v2.15.0 source (`apperror.UserNotFound()`,
  serialized by `middleware.ErrorHandlerMiddleware` as `{"error": "User not
  found", "code": "user_not_found", ...}`; identical on both versions). Any
  other 404 — a generic proxy or load-balancer not-found page, a wrong base
  URL, or the fork's own "API endpoint not found" sentinel for a server too
  old to have the endpoint — surfaces as an error on both paths instead.
  Delete's own update-user-groups `PUT` can also 404 without proving the
  user is gone (for example, a transient routing problem); a 404 there
  triggers exactly one re-`GET`, and only a positive confirmation from that
  re-`GET` is accepted as success.
- **Does not combine with `pocketid_user.groups`:** that attribute is already
  authoritative over a user's full group list — including resetting it to
  empty when `groups` is left unset in configuration, which
  `TestAccResourceUser_withGroups`'s "Remove all groups" step already exercised.
  Reproduced live on Pocket ID 2.15.0: a `pocketid_user` resource that never
  sets `groups` plans to clear a membership added by
  `pocketid_group_membership` on the very next refresh. `pocketid_user`'s
  existing behavior is unchanged; this is a documented incompatibility between
  the two resources, not a bug fix. `pocketid_group` does not manage membership
  at all and is safe to use alongside the new resource. (The new resource's
  own example previously suggested a `pocketid_user` resource that "never sets
  its own groups attribute" as a safe pairing; that contradicted the warning
  above and has been removed — an *unmanaged* user, created outside any
  `pocketid_user` resource, is the supported case.)
- The `pocketid_user` data source gains an `email` lookup key alongside `id`
  and `username`, enforced as exactly-one-of at plan/validate time via a
  `ConfigValidators` (previously only checked at apply time, inside Read) —
  useful for adding an existing (e.g. owner) account to groups with
  `pocketid_group_membership` while never touching its `groups` attribute.
- **Pagination fix:** the username and email lookups on `pocketid_user`, and
  the full listing in `pocketid_users`, called `ListUsers()` once and
  silently missed every user past the first page (the server's default page
  size is 20). Both now page through the complete result set with a new
  `Client.ListAllUsers`, which also passes the looked-up value to the
  server's `search` filter to narrow each page fetched. `ListAllUsers`
  treats a nonempty page reporting no valid `pagination.totalPages` as
  malformed and returns an error, rather than silently assuming that page
  was the last one and truncating the result without any signal.

## 2.4.102 — 2026-09-23

2.4.101 was tagged but never built or released; 2.4.102 contains it plus the
application-configuration change below. Use 2.4.102.

Upstream #116, fixing upstream #106: a client update no longer resets settings the
provider does not manage. Also adopts upstream #103's shape for the application
configuration.

- `PUT /api/oidc/clients/:id` replaces a client in full, and the provider never sent
  `description`, `skipConsent`, `accessTokenDurationMinutes` or
  `refreshTokenDurationMinutes`. Every update of a `pocketid_client`, however
  unrelated, cleared the description, turned skip-consent off and returned both token
  lifetimes to their defaults, including values set in the Pocket ID admin UI. The
  provider now reads the client before updating it and sends these back unchanged,
  together with the logo fields. They are still not attributes.
- That read now always happens and also supplies the federated identity values it
  was already made for, so an update issues one read, not two. If the read fails,
  nothing is changed.
- The application-configuration update now starts from a copy of the server's current
  configuration and overlays the planned values, as upstream #103 does. Every modelled
  field was already preserved, so nothing changes today; a field added to the client
  model later is kept without also being listed in the merge. The fork's four WebAuthn
  and CIMD attributes stay.
- No schema or state change; 2.4.1 state plans empty.
- **Version number:** upstream Trozz has released its own, different 2.4.0 to 2.4.2.
  Fork patch releases on the 2.4 line are numbered from 2.4.101 so they cannot share
  a number with an upstream release.

## 2.4.1 — 2026-09-20

Fixes found by an independent review of 2.4.0, each reproduced against the released
binary first. If you use `federated_identities`, prefer this release to 2.4.0.

- Identities sharing issuer, subject and audience now each keep their own
  `replay_protection` when it is omitted. 2.4.0 matched by first hit, so an unrelated
  update gave every twin the first one's value and could silently disable protection.
  The nth occurrence of a key is now paired with the nth prior one, at plan and apply.
- A `replay_protection` that is configured but not known until apply, such as another
  resource's output, is left to the configuration. 2.4.0 planned a value over it and
  Terraform rejected the plan with "planned value does not match config value".
- A `null` element in `public_keys` is rejected at plan time. 2.4.0 dropped it while
  building the request, which failed with "element has vanished" after the server
  had been changed, and `[null]` alone slipped past the 2.15.0 version gate.
- `public_keys` also rejects, at plan time and without echoing the key, two keys with
  the same `kid`, a non-string `kid`, `kty` or `use`, and RSA, EC or OKP keys missing
  their public parameters. The server refused these only at apply.
- Acceptance tests now fail when `POCKETID_TEST_VERSION` is missing or malformed,
  instead of reading it as an older server and skipping the newer checks.
  `tests/native/upgrade.py` additionally covers protection enabled outside Terraform
  and an update applied with `-refresh=false` while state predates the attribute.
- No schema or behaviour change otherwise; 2.4.0 state plans empty.

## 2.4.0 — 2026-09-20

Pocket ID 2.15.0 support. Released 2.3.2 already works on 2.15.0; this release closes
two ways a client update could silently weaken a federated identity.

- Add `replay_protection` to `federated_identities`. Pocket ID replaces the whole
  identity list on every client update and the provider never sent the field, so
  every create and every update, however unrelated, disabled replay protection,
  including on identities an administrator had protected in the UI. An explicit
  value now wins. When omitted, an identity already managed keeps its current
  value, matched by issuer, subject and audience rather than list position.
- **Behaviour change:** a *new* identity with `replay_protection` omitted is created
  with it enabled, as the Pocket ID admin UI does. Earlier releases created it
  disabled. Set `replay_protection = false` for an issuer whose token is presented
  more than once. Existing identities are not changed by upgrading.
- Add `public_keys` to `federated_identities` for Pocket ID 2.15.0's explicit JWKs.
  Without it, a provider update deleted keys configured in the UI. Keys compare by
  JSON semantics because the server re-encodes them. Private or symmetric keys,
  a missing `kid`, a non-signature `use`, and `jwks` together with `public_keys` are
  rejected at plan time without echoing the key. On a server older than 2.15.0 the
  provider refuses before any mutation instead of letting the keys be dropped.
- Correct the `jwks` description: it is a JWKS URL.
- Support matrix is now Pocket ID 2.15.0 and 2.14.0; 2.13.0 leaves support, CI and
  the fixture allowlist. CI runs the full suite on both versions.
- Tests that selected 2.14+ assertions by exact version now compare versions, so they
  run on 2.15.0 and later. Add `tests/native/upgrade.py`, an upgrade proof from a
  released archive.
- Update gRPC to 1.83.2 for reachable advisory GO-2026-6443.
- No schema version change or state upgrader is needed; state from 2.3.2 plans empty.

## 2.3.2 — 2026-09-06

- Add `webauthn_user_verification`, `webauthn_allow_synced_passkeys`,
  `webauthn_authenticator_attachment`, and `cimd_url_allowlist` to the application
  configuration resource, data source, and API mappings.
- Preserve current server values when attributes are omitted or unknown; no
  security defaults are invented. Explicit values, including clearing the CIMD
  allowlist, remain intentional configuration changes.
- Fix HTTP 400 application-configuration updates on Pocket ID 2.13.0 and 2.14.0.
  Regression tests prove old-payload rejection and SMTP-only updates preserve
  every unrelated returned server setting, including secret values.
- Verify import, refresh, data-source values, attribute removal and no-change
  plans with native Terraform and OpenTofu; verify enforced OpenTofu state,
  backup and saved-plan encryption in disposable fixtures.
- Adopt current/prior minor-series support, pinned to tested Pocket ID 2.14.0 and
  2.13.0 for this release. Drop 2.9.0 from support, CI and disposable fixtures.
- Preserve the OIDC client-secret compatibility implementation.

Release preparation and publication commands: [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md).

### Maintenance cleanup included in 2.3.2

- Consolidate upstream dependency updates (#87, #98, #99) and selected Actions pins (#94).
- Rewrite installation and contributor guidance for the fork; correct MIT license
  references, reporting contacts and example provider addresses.
- Consolidate local checks and disposable test fixtures; expand CI and validate
  manually requested release tags before creating a draft.
- Record the upstream backlog, including group-order drift (#92), declarative
  secrets/IDs (#90), application-config failures and registry publication.

## 2.3.1 — 2026-09-06

Based on upstream v2.3.0. Backports PR #97 with original contributor attribution.

- Select the singular/plural client-secret endpoint using validated semantic versions.
- Reject invalid version responses; legacy fallback requires a verified missing route.
- Do not automatically retry mutations or follow redirects with an API key.
- Preserve known client identity after uncertainty or failed cleanup; report rollback
  honestly. Refuse fixed-ID creation when an existing object is found.
- Remove HTTP request/response bodies and arbitrary server errors from diagnostics/logs.
- Preserve resource schemas, import IDs and create-only secret behavior.
- Publish under the fork's own identity with native mirror installation. Registry
  publication is pending; checksums are provided and archives are unsigned.

See TESTING.md for official-image results and deferred application-configuration
acceptance failures, CONTRIBUTING.md for upstream reconciliation, and upstream's
[release history](https://github.com/Trozz/terraform-provider-pocketid/releases)
for changes through v2.3.0.
