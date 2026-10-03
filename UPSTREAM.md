# Origin and independence

This provider is an independent project. It began in September 2026 as a
maintenance fork of
[Trozz/terraform-provider-pocketid](https://github.com/Trozz/terraform-provider-pocketid)
("upstream"), and from release 3.0.0 it is maintained on its own terms. This
file records where it came from, what independence means in practice, and where
its behavior differs from upstream's. It is not a review of upstream's open
work; the reviews made while this was still a fork (2026-09-06 to 2026-10-02)
are in this file's git history.

## Origin and attribution

- The first release, 2.3.1, started at upstream v2.3.0 (`44c32e0`). The complete
  history, with every upstream author's commits under their own name, is kept in
  this repository's git history.
- [LICENSE](LICENSE) is upstream's MIT license with its notice, "Copyright (c)
  2024 trozz", unchanged. The license requires that notice and permission text
  to stay with every copy or substantial portion of the software, so it stays in
  the source, in every release archive, and in anything derived from this
  repository.
- Release 2.3.1 includes Mathieu Lemay's
  [PR #97](https://github.com/Trozz/terraform-provider-pocketid/pull/97) (secret
  creation on Pocket ID 2.14 and later) and Yusaku Mizobuchi's tests. Release
  2.4.102 includes upstream's #116 (preserve client settings the provider does
  not expose) with its authorship kept, and takes the copy-first shape of #103's
  application-configuration update. Release 2.4.104 adapts #122 (the secret
  Pocket ID 2.17.0 creates with a client).
- Two 3.0.0 changes follow proposals made upstream, adapted to this provider's
  conventions: `allowed_user_groups` as a set (#92), and choosing a user's ID
  when creating it (#90). The rest of #90, a configurable client secret, is not
  adopted; see below.
- Dependency and workflow updates that upstream's pull requests proposed were
  taken or replaced by this repository's own pins.

## Independent maintenance from 3.0.0

- **No tracking.** This project does not follow upstream's releases, rebase onto
  them or merge them on a schedule. Release numbers are this project's own: its
  2.4.0 and 2.4.1 already shared numbers with upstream releases of the same name
  but not their content, and a 3.x release of either project says nothing about
  the other.
- **Upstream changes are ideas.** They are read like any other source of ideas
  and taken when they fit this project's conventions, with authorship kept.
  None is owed.
- **Upstream may merge back.** Everything here is MIT licensed. Upstream is
  welcome to take any of it, with or without asking. This project does not keep
  patches for upstream, wait for its review, or plan to return users to it.
- **Issues go here.** Report bugs in this repository's
  [Issues](https://github.com/irashack/terraform-provider-pocketid/issues) and
  vulnerabilities through [private reporting](SECURITY.md). Upstream's contact
  addresses and support commitments do not apply. It is a volunteer project
  with no response-time guarantee.
- **Names.** The provider address is `registry.terraform.io/irashack/pocketid`
  and the Go module is `github.com/irashack/terraform-provider-pocketid` (it was
  upstream's module path until 3.0.0). This is not an official Pocket ID or
  Trozz release.

## Moving from upstream's provider

Use the state-replacement procedure in
[INSTALL.md](INSTALL.md#existing-upstream-managed-resources) and require a plan
with no replacements or secret rotation. The only upstream state this project
has run against is upstream 2.3.0, on Pocket ID 2.13.0, which 3.0.0 no longer
supports (see [TESTING.md](TESTING.md)). State written by later upstream
releases, and its upgrade to 3.0.0, is not tested. Expect the differences below
in the first plan, and read the 3.0.0 section of [CHANGELOG.md](CHANGELOG.md).

## Behavioral differences

This describes what this provider does. Where upstream's behavior is known from
the review of its 2.4.x and 2.5.0 releases (2026-09-23 and 2026-10-02) it is
named; otherwise assume upstream still behaves as its v2.3.0 base did, which has
not been re-checked against every later release. Resources and data sources that
exist only here are listed in [README.md](README.md#what-you-can-manage), not
compared one by one.

**Failure handling**

- Changes (POST, PUT, DELETE and file uploads) are never retried. Reads are
  retried up to four attempts, each one request on a fresh connection.
  Upstream retries POST, PUT and DELETE after a server error or a connection
  reset, which can leave an orphaned secret or a duplicate object.
- When a create fails after Pocket ID made the object, the provider deletes it
  and says so only when the deletion is confirmed; otherwise the object's ID
  (and any generated secret) stays in state and the error says so. Upstream
  discards the cleanup delete's error and reports the object as deleted.
- A client, user, group or SCIM service provider that no longer exists leaves
  state, and its delete succeeds, only on Pocket ID's own structured not-found
  answer. Any other 404 (a wrong base URL, a proxy's error page) is an error.
- After Pocket ID accepts a change that silently ignores part of it (a group ID
  that names no group, a permission key an API does not have, custom claims it
  stores differently), the provider compares what the server holds and fails
  naming the difference.
- Lists of users, groups and clients are read completely, not as the first page.
  Identifiers are checked before a request is sent: users, groups, client
  secrets and SCIM service providers need UUIDs, and client IDs follow Pocket
  ID's rule.
- Redirects are not followed with the API key. Errors and logs never contain a
  response body, reason phrase or malformed-response text.

**Secrets**

- `pocketid_client.client_secret` is computed. It cannot be configured, unlike
  upstream's `client_secret` input (#90). A custom or rotating secret is a
  separate `pocketid_client_secret` (with write-only `secret_wo`), and
  `generate_secret = false` keeps `pocketid_client` from holding one.
- The secret Pocket ID 2.17.0 creates with a new confidential client is revoked,
  so the client holds exactly the secret in state.
- `smtp_password_wo`, `ldap_bind_password_wo`, `token_wo` (SCIM) and `secret_wo`
  keep secrets out of plan and state. The `pocketid_application_config` data
  source no longer has `smtp_password` or `ldap_bind_password`, and the resource
  does not copy a password you did not configure into state. The
  `pocketid_signup_tokens` and `pocketid_api_keys` data sources never expose a
  token or key value.

**Clients**

- `allowed_user_groups` is a set (as in upstream's #92). `is_group_restricted`
  is explicit and fails closed: removing a client's groups leaves it admitting
  nobody, and opening a restricted client needs `is_group_restricted = false`.
  A group ID that names no group fails the apply.
- `client_id` always equals the client's real ID and is validated. A different
  configured value replaces the client; before, the new value was recorded while
  the server kept the old ID.
- `launch_url`, `description`, `skip_consent` and the two token lifetimes are
  left alone when not configured. Federated identities keep `replay_protection`
  and `public_keys`, and `backchannel_logout_url` is managed. Upstream's client
  update, as examined on 2026-10-02, resets the first two on every update and
  clears a back-channel logout URL set in the admin UI.
- `is_public = true` with `pkce_enabled = false` is refused at plan time; a
  public client may use pushed authorization requests. Changing `is_public`
  generates or revokes the held secret in place. Clients registered from a
  Client ID Metadata Document are refused rather than managed.
- A client's logo is a `pocketid_client_logo` uploaded from a file, not the
  `logo_url` and `dark_logo_url` attributes of upstream's #121. With a logo URL
  Pocket ID saves the client first and can then fail the download without
  returning the client's ID, which does not fit this provider's rule that a
  failed create never loses track of what it made.

**Users and groups**

- A user holds exactly the `groups` and `custom_claims` configured, including
  none. Pocket ID's signup default groups and claims are no longer added to
  users the provider creates.
- A user's `id` can be chosen when it is created and cannot change afterward.
  Changing it is a plan-time error; upstream's #90 replaces the user, which
  deletes their passkeys.
- Plan-time validators follow Pocket ID's own rules for usernames, names, group
  names, custom claim keys and values, and one-time token lifetimes. `email` is
  optional for instances that do not require one.
- For a user or group Pocket ID synchronizes from LDAP, an update that Pocket ID
  would silently ignore fails before anything is written, naming the attributes.
- `pocketid_group_members` owns a group's whole membership; the existing
  `pocketid_group_membership` stays non-authoritative. They must not be combined
  for one group.

**Application configuration**

- Every setting Pocket ID reports is sent back unchanged, including ones this
  provider version does not know, so a Pocket ID release that adds a required
  setting does not break updates (2.17.0's `autoCreateOidcClientSecret` did).
  Unset settings stay as the server has them, with no change in the plan.
- Values are checked at plan time with Pocket ID's rules, and what the server
  stored is checked afterward. An empty value that Pocket ID would replace with
  a default is refused, and `session_duration` must be at least 1.
- Application images are the separate resource `pocketid_application_image`,
  sent without retries. A user's profile picture is the separate resource
  `pocketid_user_profile_picture`.

**Servers**

- Supported Pocket ID servers are 2.14.0 through 2.17.0. Upstream's own range is
  its own to state.
