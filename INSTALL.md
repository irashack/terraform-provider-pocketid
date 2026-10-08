# Install the provider

This provider uses the source address **registry.terraform.io/irashack/pocketid**
in both Terraform and OpenTofu. It is not published to a provider registry.
Installation uses each tool's native filesystem mirror support; no registry
account, private server, cache substitution, or development override is required.
Write-only attributes need Terraform or OpenTofu 1.11 or later; everything else
works with the versions listed in [TESTING.md](TESTING.md).

```hcl
terraform {
  required_providers {
    pocketid = {
      source  = "registry.terraform.io/irashack/pocketid"
      version = "3.1.1"
    }
  }
}
```

Download the exact version's archive and SHA256SUMS from
[release v3.1.1](https://github.com/irashack/terraform-provider-pocketid/releases/tag/v3.1.1).
Verify the SHA256SUMS file against the immutable digest recorded in the release
notes, then verify the selected archive against that file. Checksums detect
content changes; they are not a registry GPG signature. This release is unsigned.

For example, for `darwin_arm64` (use `linux_amd64` or `linux_arm64` as appropriate):

```sh
version=3.1.1
platform=darwin_arm64
archive=terraform-provider-pocketid_${version}_${platform}.zip
sums=terraform-provider-pocketid_${version}_SHA256SUMS
release=https://github.com/irashack/terraform-provider-pocketid/releases/download/v${version}
curl --fail --location --output "$archive" "$release/$archive"
curl --fail --location --output "$sums" "$release/$sums"
# Set this to the literal SHA256SUMS digest from the v3.1.1 release notes:
expected_manifest_sha256=RELEASE_NOTES_DIGEST
printf '%s  %s\n' "$expected_manifest_sha256" "$sums" | shasum -a 256 -c -
awk -v file="$archive" '$2 == file { print }' "$sums" | shasum -a 256 -c -
mirror="$HOME/.local/share/pocketid-provider-mirror"
mkdir -p "$mirror/registry.terraform.io/irashack/pocketid"
cp "$archive" "$mirror/registry.terraform.io/irashack/pocketid/"
```

Use a CLI configuration file with an **absolute** mirror path:

```hcl
provider_installation {
  filesystem_mirror {
    path    = "/absolute/path/to/pocketid-provider-mirror"
    include = ["registry.terraform.io/irashack/pocketid"]
  }
  direct {
    exclude = ["registry.terraform.io/irashack/pocketid"]
  }
}
```

Set `TF_CLI_CONFIG_FILE` to that file for `terraform init` or `tofu init`.
Do not edit the user's global CLI configuration or overwrite an upstream cache.
Commit `.terraform.lock.hcl` after initialization. To prepare lock hashes for
multiple target platforms, download and verify each corresponding archive into
the packed mirror, then run (substitute `terraform` if desired):

```sh
tofu providers lock -fs-mirror="$mirror" \
  -platform=darwin_arm64 -platform=linux_amd64 -platform=linux_arm64
```

Keep the exact version and checksum pins; do not silently select a newer tag.

## Upgrade from 3.0.1 to 3.1.1

A minor release with no breaking changes and no state upgrade: state written by
3.0.1 plans empty under 3.1.1 (proved with the verified 3.0.1 archive, below).
Verify and add the v3.1.1 archive to the native mirror beside 3.0.1, change only
the exact version pin to `3.1.1`, run `tofu init -upgrade` (or `terraform init
-upgrade`) and commit the lockfile, then require an empty refreshed plan.

What is new is opt-in: `preset` on `pocketid_client_logo` and the
`pocketid_logo_presets` data source, both needing Pocket ID 2.18.0 or later with
its icon library on. Rolling back to 3.0.1 needs any `preset` logos removed from
the configuration first; 3.0.1 does not know the attribute.

## Upgrade from 2.4.104 to 3.0.1

3.0.1 is 3.0.0 (tagged but never released) with a test-fixture fix, and it is a
major release from 2.4.104. Unlike the earlier patch upgrades it does **not** plan
empty for every configuration, because it contains deliberate breaking changes
(listed below and marked **Breaking** in the 3.0.0 section of
[CHANGELOG.md](CHANGELOG.md)). State written by 2.4.103 and 2.4.104 keeps working:
same source address, no `state replace-provider`, no state upgrade step, no
development override. The upgrade proofs start from that state; from an older
release, upgrade to 2.4.104 first and require an empty plan there. Supported Pocket
ID servers are 2.14.0 through 2.17.0; upgrade an older server first.

**Before switching**

1. Back up state with its configured backend and retain the encryption key
   separately. Start from a clean 2.4.104 plan, so every difference you see later
   comes from this upgrade.
2. Optionally, run the native upgrade proofs yourself from a checkout of the 3.0.1
   source. They need Docker, Python 3 and Go, and use a disposable Pocket ID with
   synthetic data, never your instance or your state. Each lets the verified
   published 2.4.104 archive write state, takes that state over with the new build
   and checks the plan and an update:
   - `tests/native/upgrade.py`: a client with federated identities, replay
     protection and a back-channel logout URL set outside Terraform.
   - `tests/native/client_upgrade.py`: homelab-shaped clients (fixed `client_id`,
     `launch_url`, `prevent_destroy`, sorted group lists), a public client, group
     restriction and `generate_secret` in place.
   - `tests/native/upgrade_users_groups.py`: users, groups, a membership and a
     one-time access token.
   - `tests/native/application_config.py`: an SMTP configuration written with the
     plain password by 2.4.104, taken over, then moved to `smtp_password_wo`.

   [TESTING.md](TESTING.md) has the commands and what each proves.
3. Verify and add the v3.0.1 archive to the existing native mirror, leaving earlier
   versions intact. Change only the exact version pin to `3.0.1`, run
   `tofu init -upgrade` through the root's normal entry point and commit the
   lockfile.
4. Run a **refreshed** plan (no `-refresh=false`) and compare every difference with
   the lists below. Do not apply until each is explained. Then apply.

**Breaking changes to look for**

- Go importers: the module is now `github.com/irashack/terraform-provider-pocketid`.
  Configurations are unaffected.
- IDs of users, groups, client secrets and SCIM service providers must be UUIDs,
  and client IDs follow Pocket ID's rule (2 to 128 letters, digits, `.`, `_`, `-`).
  Anything else, such as a mistyped import ID, is refused before a request is sent.
  The `pocketid_group` data source's `id` must be a UUID, and with both `id` and
  `name` set the group with that ID must have that name. Clients registered from a
  Client ID Metadata Document (`client_type = "cimd"`) cannot be imported, looked
  up by ID or managed.
- `pocketid_client.allowed_user_groups`, and the attribute of the same name on the
  `pocketid_client` and `pocketid_clients` data sources, is a **set**. Expressions
  that index into it (`allowed_user_groups[0]`) must change, for example to
  `tolist(...)[0]`. Existing state needs no migration. On Pocket ID 2.14 the
  `pocketid_clients` data source reports it as null; use the `pocketid_client` data
  source there.
- `pocketid_client.client_id` is always the client's real ID. A configured value
  that differs from the existing client's ID **replaces the client** (with
  `prevent_destroy`, the plan fails). Check clients whose `client_id` you changed
  under 2.4.x: Pocket ID ignored the change, and the plan now shows the
  replacement.
- Removing `launch_url` from the configuration no longer removes the URL; set
  `launch_url = ""`.
- Group restriction fails closed. Removing a client's `allowed_user_groups` leaves
  it admitting nobody; to let every user sign in, set `is_group_restricted = false`
  (the plan warns when it opens a client). A group ID that names no group fails
  the apply.
- `is_public = true` with `pkce_enabled = false` is refused at plan time.
- Values Pocket ID would reject are refused at plan time instead of at apply:
  `username`, `first_name`, `last_name`, `display_name`, group `name` (at least 2
  characters) and `friendly_name`, custom claim keys and values (empty and reserved
  names), and a one-time access token's `ttl`. Check your configuration against
  the rules in the changelog.
- Users created by the provider hold exactly the configured `groups` and
  `custom_claims`. Pocket ID's signup default groups and claims are no longer added;
  list them in the configuration if you relied on them. An unknown group ID in
  `groups` or a membership now fails instead of being recorded.
- A user or group that Pocket ID synchronizes from LDAP, while LDAP is enabled:
  changing anything but a user's `locale`, groups or custom claims fails before
  anything is written, naming the attributes.
- `pocketid_application_config`: an empty string is refused for settings Pocket ID
  requires and for settings it replaces with a non-empty default (`accent_color`,
  `signup_default_user_group_ids`, `signup_default_custom_claims`,
  `cimd_url_allowlist`, `ldap_user_search_filter`, `ldap_user_group_search_filter`,
  `ldap_attribute_user_display_name`, `ldap_attribute_group_member`), and
  `session_duration` must be at least 1. `signup_default_custom_claims` is a JSON
  array of `{"key": ..., "value": ...}` objects.
- The `pocketid_application_config` data source no longer has `smtp_password` or
  `ldap_bind_password`; remove references to them. The resource no longer copies a
  password you did not configure into state. Until the next apply or
  `apply -refresh-only`, `show` cannot decode the data source's stored result.
- In the `pocketid_user` and `pocketid_users` data sources, `email` is null, not an
  empty string, for a user without one.

**Differences you can see in the first plan that are not breaking**

- New computed attributes (`client_secret_id`, `has_dark_logo`, `client_type`,
  `pkce_supported`, and the carried settings that were not configured) fill in on
  refresh without a planned change.
- A user that 2.4.x recorded with `""` for an omitted `first_name` or `last_name`
  and that was imported plans one in-place update to null, which changes nothing in
  Pocket ID. One that 2.4.x created that way is tainted in state and would be
  replaced once more, which deletes the account and its passkeys: run `untaint` on
  it before applying to get the in-place update instead.
- A SCIM provider's `token` changed or cleared outside Terraform now shows as a
  change. A configuration without `token` plans empty, but a token set in the Pocket
  ID interface for a provider whose configuration has none shows as a removal.

**`-refresh=false` caveats**

Plan and apply with a refresh for the first run after upgrading; state written by
2.4.x predates several attributes. If you must skip the refresh:

- An apply from such state shows `generate_secret` going from null to true on every
  client. It is an in-place update that changes nothing in Pocket ID.
- An update that would open a client restricted in the admin UI since the last
  refresh, with `is_group_restricted` not configured, stops before changing anything
  and asks for a refreshed plan, as does a plan from state that recorded a
  `client_id` rename Pocket ID ignored.
- Users with omitted names plan the one in-place update above also without a refresh.

**What 3.0.1 refuses that 2.4.104 accepted**

Besides the plan-time rules above:

- An answer from Pocket ID it cannot rely on: one that is not the JSON expected,
  lacks the fields that say what the server holds (a client's, user's or group's
  memberships, a grant's access), names another object, or contains the API key.
  The error quotes nothing from the response; after a change Pocket ID accepted it
  says the result could not be read and to inspect the object before trying again.
  A proxy that rewrites Pocket ID's answers can therefore fail applies that passed
  before.
- An identifier or lookup value containing the provider's API key, from the
  configuration, state or an import ID. It is refused before any request and never
  shown.
- Changes to a resource whose last change had an uncertain result, until that is
  settled: `unresolved_creation` on `pocketid_user` and
  `pocketid_group_membership`, `unresolved_user_ids` on `pocketid_group_members`,
  and an unresolved grant on `pocketid_api_client_access`. A refreshed plan settles
  most cases; otherwise `terraform state rm` the resource and import it again. The
  3.0.0 changelog section "When a result is uncertain" gives the steps per resource.

## Upgrade from 2.4.103 to 2.4.104

A same-address patch that adds one optional attribute,
`pocketid_client.backchannel_logout_url`. Verify and add the v2.4.104 archive to the
existing native mirror, leaving earlier versions intact. Change only the exact
version pin to `2.4.104`, run `tofu init -upgrade` through the root's normal entry
point and commit the lockfile. **Require an empty plan**, with one expected
exception on Pocket ID 2.17.0: a client whose back-channel logout URL was set in the
admin UI plans to remove it, because the attribute is authoritative. Add that URL
to the client's configuration and plan again; do not apply the removal unless you
mean it.

Upgrade the provider before, or together with, upgrading Pocket ID to 2.17.0. On
2.17.0, 2.4.103 and earlier fail every application-configuration update with HTTP
400, clear a client's back-channel logout URL on every client update, and leave a
second valid secret on each confidential client they create. 2.4.104 does not remove
secrets created that way: for such a client, revoke in the admin UI the secret
whose prefix does not match the start of `client_secret` in state.

## Upgrade from 2.4.102 to 2.4.103

An additive same-address patch: same schema for every existing resource and data
source, empty plan. Verify and add the v2.4.103 archive to the existing native
mirror, leaving earlier versions intact. Change only the exact version pin to
`2.4.103`, run `tofu init -upgrade` through the root's normal entry point and
commit the lockfile. **Require an empty plan.**

Adopt the new `pocketid_group_membership` resource, and the `pocketid_user` data
source's new `email` lookup key, only where you choose to; nothing existing
changes behavior. Do not add `pocketid_group_membership` for a user that a
`pocketid_user` resource also manages with (or without) a `groups` attribute:
that attribute is authoritative over the user's full group list and will plan
to remove memberships the new resource added. See the
[`pocketid_group_membership` docs](docs/resources/group_membership.md).

## Upgrade from 2.4.1 to 2.4.102

A drop-in patch: same source address, same schema, empty plan. Verify and add the
v2.4.102 archive to the existing native mirror, leaving earlier versions intact.
Change only the exact version pin to `2.4.102`, run `tofu init -upgrade` through the
root's normal entry point and commit the lockfile. **Require an empty plan.** From
2.3.2 or 2.4.0, read the next section first; its notes on `replay_protection` apply,
and you can go straight to 2.4.102.

Settings already reset by an earlier update are not restored. Check each client's
description, skip-consent and token lifetimes in the admin UI after upgrading.

## Upgrade from 2.3.2 or 2.4.0 to 2.4.1

From 2.4.0 this is a drop-in patch: same schema, empty plan. Skip 2.4.0 if you are
coming from 2.3.2.

Verify and add the v2.4.1 archive to the existing native mirror, leaving earlier
versions intact. Change only the exact version pin to `2.4.1`, keep the source
address, run `tofu init -upgrade` through the root's normal entry point and commit
the lockfile. No `state replace-provider` and no state upgrade step is involved.
**Require an empty plan.** The first refresh records each existing federated
identity's `replay_protection` as the server has it, and nothing is changed.

Earlier releases disabled replay protection on every identity they created or
updated. Upgrading does not turn it back on for you: set `replay_protection = true`
on each identity whose issuer presents a token only once. A *new* identity with
the attribute omitted is now created with it enabled. `public_keys` needs Pocket ID
2.15.0; on an older server the provider refuses before changing anything.

## Upgrade from 2.3.1

After v2.3.2 is published, verify and add its archive to the existing native mirror,
leaving v2.3.1 intact. Change only the exact version pin to `2.3.2`, keep the source
address unchanged, run `tofu init -upgrade` using the root's normal credential and
CLI configuration entry point, and commit the resulting lockfile. Review any other
provider selections before accepting the lockfile. No `state replace-provider`
is needed for this same-address patch upgrade. Require an empty baseline plan
before adding application-configuration ownership.

For SMTP adoption, import `application-configuration` into exactly one
`pocketid_application_config` resource with an initially empty body, refresh, and
require an empty plan. Then set only the SMTP attributes. Omitted fields inherit
the server's existing values, including WebAuthn policy and `cimd_url_allowlist`.
Read SMTP credentials from the existing secret authority, mark inputs sensitive,
and retain enforced encryption for state, backups and any saved plan. Neither
sensitive marking nor provider installation enables encryption by itself.

The API update replaces the full modeled configuration and is not atomic with
its preceding read. Avoid concurrent administrators/configuration writers.
Future server fields outside the tested matrix are not guaranteed to be preserved.
Rollback the provider version and lockfile to 2.3.1 if needed, but do not use that
version to update application configuration on 2.13/2.14: the original HTTP 400
returns. Do not restore a stale state backup over later changes.

## Existing upstream-managed resources

This procedure was last run with upstream 2.3.0 and release 2.3.1. State written by
later upstream releases, and its upgrade to 3.0.0, is not tested; see
[UPSTREAM.md](UPSTREAM.md#moving-from-upstreams-provider).

Back up state with its configured backend, retain the decryption key separately,
and obtain exclusive access. For encrypted OpenTofu state, verify both the
original and backup remain encrypted. Never pipe decrypted state to a file.

Use the exact old source address reported by your state. OpenTofu commonly uses
`registry.opentofu.org/trozz/pocketid`; Terraform commonly uses
`registry.terraform.io/trozz/pocketid`. For example:

```sh
tofu state replace-provider -auto-approve \
  registry.opentofu.org/trozz/pocketid \
  registry.terraform.io/irashack/pocketid
```

The supported state command takes a lock and writes a backup. Change the source
and exact version in configuration, initialize using the mirror, and review a
fresh plan. **Require no replacement or secret rotation.** Inspect rather than
apply if any existing client would change unexpectedly. Do not import an already
managed resource into a second state. Imported secrets remain unavailable; do
not recreate a client to fill that field.

Rollback uses the inverse supported `state replace-provider` operation and the
prior source/version/lockfile under the same exclusive access and backup rules.
Do not restore an old whole-state backup after unrelated resources changed.
Upstream 2.3.0 can still refresh existing clients, but its secret-creation bug
returns on Pocket ID 2.14. After uncertain creation, inspect the client before
any recovery; a tainted resource may otherwise be replaced by the next apply.

Native installation references:
[Terraform CLI configuration](https://developer.hashicorp.com/terraform/cli/config/config-file#provider-installation),
[OpenTofu provider-address migration](https://opentofu.org/docs/cli/commands/state/replace-provider/),
[OpenTofu state and plan encryption](https://opentofu.org/docs/language/state/encryption/).
