# Pocket ID provider for Terraform and OpenTofu

Manage [Pocket ID](https://pocket-id.org/) OIDC clients, users, groups, API access
and instance settings as code. This is an independent provider. It began as a
maintenance fork of
[Trozz/terraform-provider-pocketid](https://github.com/Trozz/terraform-provider-pocketid)
and, from release 3.0.0, is maintained on its own: it does not track upstream's
releases, and upstream is welcome to merge anything from it. It keeps upstream's
MIT license and credits. [UPSTREAM.md](UPSTREAM.md) records where it came from
and how its behavior differs. It is not an official Pocket ID or Trozz release.

## Get started

Release **3.0.0** supports Pocket ID **2.14.0 through 2.17.0** and has breaking
changes from 2.4.x; read [Upgrading from 2.4.x](#upgrading-from-24x) first if you
already use this provider. It is not published to a provider registry: install its
verified archive through the native filesystem mirror in [INSTALL.md](INSTALL.md)
before running `terraform init` or `tofu init`. A GitHub release alone does not
make the provider registry-installable.

```hcl
terraform {
  required_providers {
    pocketid = {
      source  = "registry.terraform.io/irashack/pocketid"
      version = "3.0.0"
    }
  }
}

# Supply POCKETID_BASE_URL and POCKETID_API_TOKEN through your environment.
provider "pocketid" {}

resource "pocketid_client" "example" {
  name          = "Example application"
  callback_urls = ["https://app.example.com/oauth/callback"]
}
```

Create an API key in your Pocket ID instance and supply it through your secret
manager or local environment. The base URL is the instance origin, such as
`https://id.example.com`, without `/api`. Keep API keys out of `.tf` files.
A generated client secret is a sensitive value stored in state; with
`generate_secret = false` and a write-only `pocketid_client_secret` (below) none
is. Protect state and saved plans.

Already using `trozz/pocketid`? See
[Moving from upstream's provider](UPSTREAM.md#moving-from-upstreams-provider) and
the [state migration procedure](INSTALL.md#existing-upstream-managed-resources),
and require a plan with no replacements or secret rotation. Keep this provider's
own address; do not overwrite an upstream binary or use development overrides for
normal installations.

## Conventions

These apply across resources. Each resource's documentation names the attributes.

1. **Write-only secrets (`_wo` and `_wo_version`).** Where a secret can be sent
   without being stored, a pair of attributes holds it: `<name>_wo` (write-only,
   sensitive) and `<name>_wo_version` (a string). These are `smtp_password_wo` and
   `ldap_bind_password_wo` on `pocketid_application_config`, `token_wo` on
   `pocketid_scim_service_provider`, and `secret_wo` on `pocketid_client_secret`.
   The value is sent when the resource is created and whenever you change the
   version string; it never enters plan or state, so it can come from an ephemeral
   value, and a change made to it outside Terraform is not detected. Each
   conflicts with its plain attribute (`smtp_password`, `ldap_bind_password`,
   `token`), which stores the secret in state and works with any Terraform or
   OpenTofu version. Write-only attributes need Terraform or OpenTofu 1.11 or later.
2. **Unmanaged unless set.** A setting you leave out is left as the server has it,
   state shows the server's value, and an unrelated update shows no change for
   it. Setting it makes it authoritative. This covers `description`,
   `skip_consent`, `launch_url` and the two token lifetimes on `pocketid_client`
   (set `description = ""` or `launch_url = ""` to remove one) and every
   setting you omit from `pocketid_application_config`. Attributes that
   describe a complete set, such as a user's `groups` and `custom_claims`, stay
   authoritative: omitted means none.
3. **Confirmed absence.** A client, user, group or SCIM service provider leaves
   state, and its destruction succeeds, only when Pocket ID answers that exact
   object does not exist, in its own structured not-found error. Any other 404
   (a wrong `base_url`, a proxy's error page) stays an error, so a
   misconfiguration cannot make resources vanish from state.
4. **Verify what the server did.** Pocket ID silently ignores some input, such
   as a group ID that names no group, so after a write the provider compares
   what the server holds with what was asked and fails naming the difference. It
   does this for group IDs (`allowed_user_groups`, `pocketid_user.groups`,
   `pocketid_group_membership`, `pocketid_group_members` and
   `pocketid_signup_token.user_group_ids`), custom claims, API permission keys
   in `pocketid_api_client_access`, and the settings in `pocketid_application_config`.
5. **State compatibility.** New attributes decode from state written by 2.4.103
   and 2.4.104 without a state upgrader, and an unchanged configuration plans
   empty afterwards, except for the changes the changelog marks as breaking or as
   one-time. The native upgrade scripts in [TESTING.md](TESTING.md) prove this
   against a real server.

## What you can manage

| Capability | Documentation |
|---|---|
| OIDC clients, callbacks, back-channel logout, PKCE, federated identities and group access | [`pocketid_client`](docs/resources/client.md) |
| One secret of a client, rotated without replacing it | [`pocketid_client_secret`](docs/resources/client_secret.md) |
| A client's light or dark logo from a file | [`pocketid_client_logo`](docs/resources/client_logo.md) |
| Pocket ID APIs, their permissions and one client's access to one API | [`pocketid_api`](docs/resources/api.md), [`pocketid_api_client_access`](docs/resources/api_client_access.md) |
| Users and their profile pictures | [`pocketid_user`](docs/resources/user.md), [`pocketid_user_profile_picture`](docs/resources/user_profile_picture.md) |
| Groups and membership | [`pocketid_group`](docs/resources/group.md), [`pocketid_group_membership`](docs/resources/group_membership.md) (adds one user, non-authoritatively), [`pocketid_group_members`](docs/resources/group_members.md) (owns the whole membership) |
| One-time access tokens and signup tokens | [`pocketid_one_time_access_token`](docs/resources/one_time_access_token.md), [`pocketid_signup_token`](docs/resources/signup_token.md) |
| SCIM service providers, a SCIM synchronization and LDAP synchronization | [`pocketid_scim_service_provider`](docs/resources/scim_service_provider.md), [`pocketid_scim_sync`](docs/resources/scim_sync.md), [`pocketid_ldap_sync`](docs/resources/ldap_sync.md) |
| Instance configuration and the application images | [`pocketid_application_config`](docs/resources/application_config.md), [`pocketid_application_image`](docs/resources/application_image.md) |

Data sources:
[`pocketid_client`](docs/data-sources/client.md),
[`pocketid_clients`](docs/data-sources/clients.md),
[`pocketid_user`](docs/data-sources/user.md),
[`pocketid_users`](docs/data-sources/users.md),
[`pocketid_current_user`](docs/data-sources/current_user.md) (the user the API key
belongs to),
[`pocketid_user_passkeys`](docs/data-sources/user_passkeys.md) (read-only),
[`pocketid_group`](docs/data-sources/group.md),
[`pocketid_groups`](docs/data-sources/groups.md),
[`pocketid_api`](docs/data-sources/api.md),
[`pocketid_apis`](docs/data-sources/apis.md),
[`pocketid_signup_tokens`](docs/data-sources/signup_tokens.md),
[`pocketid_api_keys`](docs/data-sources/api_keys.md) (the provider key owner's keys,
for an expiry check),
[`pocketid_application_config`](docs/data-sources/application_config.md) and
[`pocketid_version`](docs/data-sources/version.md).

Deliberately not offered: creating, renewing or revoking API keys, and creating or
deleting passkeys (a lockout risk; passkeys can be listed). See
[examples](examples/README.md) for complete configurations and
[provider settings](docs/index.md) for authentication, TLS and timeouts.

## Compatibility and current limits

Pocket ID **2.14.0 through 2.17.0** is supported, at the exact patch versions the
acceptance suites run against (official images). Untested patch versions are not
automatically certified. **2.13.0 and earlier are not supported or tested**; legacy
parsing safeguards, such as the singular secret endpoint used when a server reports
a version before 2.14.0, are defensive code, not a support promise. Terraform and
OpenTofu are tested natively with OpenTofu 1.12 and Terraform 1.16
([TESTING.md](TESTING.md) has the exact versions); write-only attributes need 1.11
or later.

| Pocket ID | What depends on the version |
|---|---|
| 2.14.0 | The base. `public_keys` on federated identities, `backchannel_logout_url` and `auto_create_oidc_client_secret` are refused before any change. Pocket ID does not refuse images over 16 million pixels itself; the provider does. |
| 2.15.0 | `public_keys` supported. Pocket ID refuses JPEG and PNG images over 16 million pixels. |
| 2.16.0 | As 2.15.0. |
| 2.17.0 | `backchannel_logout_url` and `auto_create_oidc_client_secret` supported; a new confidential client's server-created secret is revoked by the provider. |

`pocketid_client_secret`, `pocketid_api` and `pocketid_api_client_access` need
Pocket ID 2.14.0 or later, which every supported server is. A feature that needs
a newer server than yours (`backchannel_logout_url`, `public_keys`,
`auto_create_oidc_client_secret`) is refused. CI runs the full provider and data-source
acceptance suites on 2.16.0 and 2.17.0 and the client, application-configuration and
API-contract subset on 2.14.0 and 2.15.0; `make test-acc-supported` runs the full
suites on all four and is part of the [release checklist](RELEASE_CHECKLIST.md).

## Upgrading from 2.4.x

3.0.0 is a major release because of deliberate breaking changes. Read the 3.0.0
section of [CHANGELOG.md](CHANGELOG.md), where each is marked **Breaking**, and run
a plan before applying. The ones most configurations meet:

- `pocketid_client.allowed_user_groups` is a set (also on the client data sources),
  so expressions that index into it need `tolist(...)`.
- `pocketid_client.client_id` is the client's real ID; a different configured value
  replaces the client. Removing `launch_url` no longer removes the URL (set `""`).
  Opening a restricted client needs `is_group_restricted = false`.
- Values Pocket ID would refuse fail at plan time: usernames, names, custom claim
  keys and values, one-time token lifetimes, application settings (an empty string
  for a setting with a default, `session_duration` below 1), `is_public = true`
  with `pkce_enabled = false`, and identifiers that are not UUIDs.
- Users created by the provider no longer receive Pocket ID's signup default
  groups and claims; list them in the configuration if you relied on them.
- The `pocketid_application_config` data source has no `smtp_password` or
  `ldap_bind_password`, and the resource no longer copies an unconfigured password
  into state.
- Go importers: the module is `github.com/irashack/terraform-provider-pocketid`.
  Configurations are unaffected.

The compatibility rule above is checked by the native upgrade scripts: they start
from state written by a published release, take it over with the new build and
require an empty plan. They are `tests/native/upgrade.py`, `client_upgrade.py`,
`upgrade_users_groups.py` and `application_config.py`; [TESTING.md](TESTING.md)
says what each proves and how to run it. [INSTALL.md](INSTALL.md) covers installing
a new version into your mirror.

## Development and maintenance

Use the Go version in `go.mod`, Python 3 and Docker for disposable acceptance
tests. The test fixture chooses a free loopback port, seeds a fresh database, and
cleans up. It does not use your running Pocket ID instance.

```sh
make check                  # formatting, vet, unit/race tests, script tests, build and lint
make docs-check             # generated documentation matches the checkout
make test-acc               # client, application-config and API-contract acceptance on Pocket ID 2.17.0
make test-acc-matrix        # the same on every fixture version (2.14.0 to 2.17.0)
make test-acc-provider      # full provider and data-source suites on POCKETID_VERSION (default 2.17.0)
make test-acc-supported     # the full suites on every supported version
```

Pinned tooling commands and contributor guidance are in [CONTRIBUTING.md](CONTRIBUTING.md).
[CI](https://github.com/irashack/terraform-provider-pocketid/actions/workflows/ci.yml)
checks changes; [releases](RELEASE_CHECKLIST.md) are manually requested drafts.
Dependabot groups weekly dependency updates for review. There is no automatic
merge, development-release churn or contributor bot committing to your branch.

Report reproducible bugs through [Issues](https://github.com/irashack/terraform-provider-pocketid/issues).
Use [private security reporting](SECURITY.md) for vulnerabilities. This is a
volunteer project with no response-time guarantee.

## Attribution

[MIT licensed](LICENSE), with upstream's copyright notice and history preserved.
The first release, 2.3.1, started at upstream v2.3.0 (`44c32e0`) and includes
Mathieu Lemay's [PR #97](https://github.com/Trozz/terraform-provider-pocketid/pull/97)
and Yusaku Mizobuchi's tests; [UPSTREAM.md](UPSTREAM.md) lists the other upstream
work taken. [UPSTREAM-README.md](UPSTREAM-README.md) is an archived upstream README,
not this provider's installation guide. See [CHANGELOG.md](CHANGELOG.md) for released
changes.

## Secret and failure behavior

Confidential-client creation generates one secret and keeps it in `client_secret`,
with its ID in `client_secret_id`. Public clients, and clients with
`generate_secret = false` (for use with `pocketid_client_secret`), generate none.
When Pocket ID 2.17.0 creates a secret of its own with a new client, the provider
revokes that one first, so the client keeps only the secret in state; a revoke that
fails is handled like a failed secret generation, below. Only creating the client,
changing `generate_secret` or changing `is_public` generates or revokes a secret;
refresh, import and other updates do not. A refresh may read the client's secret
metadata (IDs and prefixes, never values) to fill in `client_secret_id`. Import
cannot recover an existing create-only secret; the value remains null. Store
provider state securely; a sensitive attribute alone does not encrypt state.

Version errors fail before creating a confidential client. Only Pocket ID's JSON
404 `API endpoint not found` identifies a missing version route. Invalid/empty
versions, authentication failures, unexpected 404s and server/transport failures
are not classified as old servers. Changes (create, update, delete, uploads) are
never retried automatically; reads are retried up to four attempts. HTTP redirects
are not followed with the API key.

A definite rejection can roll back only the object created in that operation, and
the provider says so only when it confirmed the deletion. Cleanup failure or an
ambiguous response retains the object's ID, and any generated secret, in state and
tells you to inspect read-only. **Do not blindly apply a tainted resource:**
Terraform may propose replacement. Inspect the object, then explicitly choose
recovery. No second secret is generated automatically. A user created with a
chosen `id` whose creation had no definite answer is kept in state as unresolved,
and the provider refuses to change or delete it until you resolve it as the error
describes. A lost response to a server-generated-ID creation may leave an object
whose ID was never received; inspect the list before retrying. Existing-ID
conflicts do not trigger deletion. HTTP diagnostics expose status codes, not
response bodies, reason phrases or the text of malformed responses.
