# Testing and release validation

How every check runs, what each native script proves, and the evidence recorded
for each release. The sections from "Release 2.4.104 evidence" down are the record
of earlier releases; the support matrix and tool versions in each belong to that
release, not to the current one. The current range is Pocket ID **2.14.0 through
2.18.0**.

## Reproducible checks

```sh
make check                  # format, vet, race unit tests, fixture-script tests, build, lint
make docs-check             # generated documentation matches the checkout
make actionlint             # GitHub Actions syntax and expressions
make vuln                   # reachable Go vulnerabilities
go vet -tags=acc ./...      # the acceptance tests compile
make test-acc               # every acceptance test family (TestAcc*) on POCKETID_VERSION (default 2.18.0)
make test-acc-matrix        # test-acc on 2.14.0, 2.15.0, 2.16.0, 2.17.0 and 2.18.0
make test-acc-provider      # full provider and data-source suites on POCKETID_VERSION, one fixture
make test-acc-supported     # the full suites on every supported version, 2.14.0 to 2.18.0
```

`make test-acc-supported` runs `make test-acc-provider` once per supported version
and is the acceptance gate before a release. CI is lighter to keep its run time
down: the full suites on 2.17.0 and 2.18.0, and `make test-acc` on 2.14.0, 2.15.0
and 2.16.0. The release workflow runs `make check`, `make vuln` and
`make test-acc-matrix` on the tagged source.

The acceptance packages share one fixture and run one after another (`-p 1`), so a
client created by one package cannot crowd another's list reads. Version-specific
behavior is selected inside the tests from `POCKETID_TEST_VERSION`.

## The disposable server fixture

`scripts/disposable-pocketid.py VERSION -- COMMAND` is the only way tests get a
Pocket ID server. VERSION must be one of 2.14.0, 2.15.0, 2.16.0 or 2.17.0. The
fixture pulls the official versioned image, creates an isolated container and
database on a free loopback port, waits for health (HTTP 204 is success), stops the
server, and seeds one synthetic administrator and a hash of a random API token,
following upstream's fixture strategy. It restarts before running COMMAND and
removes its container and anonymous volumes afterward. COMMAND sees
`POCKETID_BASE_URL`, `POCKETID_API_TOKEN`, `POCKETID_TEST_VERSION` and
`POCKETID_TEST_HOST_BIND`, the address a test must bind for a server it runs on the
host to be reached from the container as `host.docker.internal` (the SCIM sync test
serves one). On macOS that name is the runtime's own and loopback is enough; on Linux
the fixture adds the name with `--add-host host.docker.internal:host-gateway` and the
address is `0.0.0.0`. No production URL, database, or user input is accepted.
Credentials pass only through process memory/environment. Failure output is
suppressed because the acceptance harness may include state in errors. For local
diagnosis only, `POCKETID_FIXTURE_FAILURE_LOG` can name a new private file outside
source; never publish that file. Logs/state are not release assets.
`make test-scripts` (part of `make check`) runs the fixture script's own unit tests
without a container.

## Native Terraform and OpenTofu scripts

These run a real `terraform` or `tofu` binary against a fixture, with the provider
installed from a filesystem mirror, and only touch temporary directories and the
fixture. Run each as
`python3 scripts/disposable-pocketid.py VERSION -- python3 tests/native/SCRIPT ARGS`.
TOOL is `terraform` or `tofu`. OpenTofu runs of `lifecycle.py` and
`application_config.py` enforce AES-GCM encryption of state, backups and saved plans
with a random passphrase.

| Script and arguments | What it proves |
|---|---|
| `lifecycle.py TOOL MIRROR [PROVIDER_VERSION]` (the version in the mirror; defaults to 3.0.0) | The provider in the mirror installs; a confidential client is created, refreshed, updated, imported (no secret invented), planned empty and destroyed. On 2.14.0 and later the client has exactly one secret, client-credentials authentication works with it and fails with a wrong one. Under OpenTofu, state, backups and a saved plan are encrypted. |
| `upgrade.py TOOL RELEASED_ARCHIVE NEW_BINARY` | State written by a published release upgrades with an empty plan. The released provider creates a confidential client with two federated identities; an administrator enables replay protection on one outside Terraform. The new build keeps the client ID and secret and each identity's replay protection through an update applied with `-refresh=false`. On 2.17.0 a back-channel logout URL set outside Terraform survives such an update, shows on the next refreshed plan, and plans empty once configured. |
| `client_upgrade.py TOOL RELEASED_ARCHIVE NEW_BINARY` | `pocketid_client` state from a published release survives the client changes: homelab-shaped clients (fixed `client_id`, `launch_url`, `prevent_destroy`, a sorted `allowed_user_groups` list), a public restricted client and one with a generated ID plan empty (list to set, `client_id`, new computed attributes, `generate_secret`, `client_secret_id`). `generate_secret = false` applied without a refresh revokes exactly the stored secret and keeps the restriction; `true` generates a new one; removing the groups leaves the client restricted; a client restricted outside Terraform is not opened by an unrefreshed update; a different `client_id` plans a replacement that `prevent_destroy` refuses. |
| `upgrade_users_groups.py TOOL OLD_BINARY NEW_BINARY` | User and group state from a previous build upgrades with an empty plan. The old build creates groups (with and without custom claims), a user with names, groups and claims, a group membership for a user Terraform does not manage, and a one-time access token. The new build plans no change, applies an unrelated update and plans empty again. |
| `application_config.py TOOL MIRROR TARGET_VERSION [OLD_VERSION]` | The application-configuration flow, below. |

`MIRROR` is a filesystem mirror holding the provider under
`registry.terraform.io/irashack/pocketid`, either unpacked
(`.../VERSION/PLATFORM/terraform-provider-pocketid_vVERSION`) or as the published
archive. `RELEASED_ARCHIVE` is the verified archive of a published release and must
keep its published file name (`terraform-provider-pocketid_X.Y.Z_OS_ARCH.zip`);
`NEW_BINARY` is the build under test, for example `bin/terraform-provider-pocketid`
from `make build`. `upgrade_users_groups.py` takes binaries, not an archive: pass
the binary unpacked from that archive (or built from the release tag's source) as
`OLD_BINARY`. It serves both under development version numbers (98.0.0 and 99.0.0)
from a temporary mirror.

```sh
make build
python3 scripts/disposable-pocketid.py 2.17.0 -- python3 tests/native/upgrade.py tofu \
  "$PWD/dist/terraform-provider-pocketid_2.4.104_darwin_arm64.zip" "$PWD/bin/terraform-provider-pocketid"
python3 scripts/disposable-pocketid.py 2.17.0 -- python3 tests/native/client_upgrade.py tofu \
  "$PWD/dist/terraform-provider-pocketid_2.4.104_darwin_arm64.zip" "$PWD/bin/terraform-provider-pocketid"
old=$(mktemp -d)
unzip -q "$PWD/dist/terraform-provider-pocketid_2.4.104_darwin_arm64.zip" -d "$old"
python3 scripts/disposable-pocketid.py 2.17.0 -- python3 tests/native/upgrade_users_groups.py tofu \
  "$old/terraform-provider-pocketid_v2.4.104" "$PWD/bin/terraform-provider-pocketid"
```

### Which native proofs a release runs

Each script runs with `terraform` **and** `tofu` (1.11 or later, which
`application_config.py`'s write-only step needs). The upgrade proofs start from
the **last release's published archive**, verified against that release's
SHA256SUMS; never from a build of its source alone. Keep that archive under its
published file name in `dist/` (as in the commands above), and unpack its binary
into a temporary directory for `upgrade_users_groups.py`, which takes binaries.
The new build is `bin/terraform-provider-pocketid` from `make build`, or, for
`lifecycle.py` and `application_config.py`, a binary stamped with the release
version (`-ldflags '-X main.version=3.0.0'`) in a mirror. For 3.0.0:

| Script | Pocket ID | Old provider |
|---|---|---|
| `lifecycle.py` | 2.17.0 and 2.14.0 | none (3.0.0 in the mirror) |
| `upgrade.py` | 2.17.0 | 2.4.104 archive |
| `client_upgrade.py` | 2.17.0 | 2.4.104 archive |
| `upgrade_users_groups.py` | 2.17.0 | binary unpacked from the 2.4.104 archive |
| `application_config.py` | 2.17.0 | 2.4.104 archive in the mirror, `OLD_VERSION` 2.4.104 |

Add a supported version to a row when the release changes behavior that depends
on it, and record in the release's evidence section what ran and what did not.

### The application-configuration script

`application_config.py` seeds non-default settings and passwords through Pocket ID's
own API, then proves, in order:

- The old payload (without the WebAuthn and CIMD settings) is rejected by the real
  validator with no change.
- An empty `pocketid_application_config` imports and plans empty, and an import
  tracks no password in state.
- An SMTP configuration (with `allow_own_account_edit` and
  `email_login_notification_enabled`) changes only those settings. Every other
  returned setting is unchanged, the resource and the data source show the same
  values, and the data source has no password attributes. Refresh and an empty plan
  follow.
- The write-only flow, below.
- Removing every attribute, then removing the resource from state and importing it
  again, plans empty, and destroying the resource leaves the live configuration
  untouched.

The write-only flow: the password moves to `smtp_password_wo` with
`smtp_password_wo_version`. The server gets the new value, the plain password leaves
state, and state holds neither value (the script searches the Terraform state file,
and the OpenTofu saved plan, for both). An unrelated update applied with
`-refresh=false` keeps the password on the server without blanking it. The target
build must support write-only attributes, and the tool must be Terraform or OpenTofu
1.11 or later; a build without them fails at this step.

The two paths differ in who writes the first state:

- Without `OLD_VERSION`, `TARGET_VERSION` itself imports the configuration and
  applies SMTP with the plain `smtp_password`.
- With `OLD_VERSION` (the last release), that older provider applies SMTP with the
  plain `smtp_password`, exactly as a configuration written for it would, and
  stores every password it read, the LDAP one included. The target build then takes
  over after `init -upgrade` and **must plan nothing** for the unchanged
  configuration. `show` cannot decode the data source's stored result (it still has
  the removed password attributes) until `apply -refresh-only` rewrites it. The rest
  is the same.

```sh
mirror=/tmp/pocketid-test-mirror
dir="$mirror/registry.terraform.io/irashack/pocketid"
mkdir -p "$dir/3.0.0/darwin_arm64"
go build -ldflags '-X main.version=3.0.0' -o "$dir/3.0.0/darwin_arm64/terraform-provider-pocketid_v3.0.0" .
cp terraform-provider-pocketid_2.4.104_darwin_arm64.zip "$dir/"   # the verified published archive
for version in 2.14.0 2.15.0 2.16.0 2.17.0; do
  for tool in terraform tofu; do
    python3 scripts/disposable-pocketid.py "$version" -- python3 tests/native/application_config.py "$tool" "$mirror" 3.0.0 2.4.104
  done
done
```

Never install a test build over an existing provider, and substitute your platform
directory. Release packaging uses GoReleaser as documented in RELEASE_CHECKLIST.md.

### Address migration from upstream

`PROVIDER_MIGRATION_TEST=1` (address migration from upstream 2.3.0) was last run on
Pocket ID 2.13.0 for release 2.3.1. It cannot run on a supported server: upstream
2.3.0 cannot create a confidential client on 2.14 or later, and 2.13.0 has left the
fixture allowlist. The case is retained in `lifecycle.py` as a record; it was written
against release 2.3.1, so pass `2.3.1` as `PROVIDER_VERSION` to reproduce it.
The migration case additionally needs the genuine upstream 2.3.0 artifact under
`registry.opentofu.org/trozz/pocketid` in the isolated mirror. It must not be a
renamed binary of this provider. The test uses supported state replacement, checks
encrypted state/backups and saved-plan encryption, preserves the ID and secret, and
requires an empty subsequent plan. No development overrides are used.

## Release 3.0.0 evidence — 2026-10-03

The first release of the project on its own terms (UPSTREAM.md); supported servers
are Pocket ID 2.14.0 through 2.17.0. macOS ARM64, Docker via OrbStack, Go 1.27.1,
golangci-lint 2.13.2, OpenTofu 1.13.1, Terraform 1.16.4. Results below were
produced from commit `29af154`, the last source change before the release commit
(the two later commits change CHANGELOG.md and README.md only).

Checks: `make check` (fmt, vet, race unit tests, script tests, build, golangci-lint
with 0 issues), `make docs-check`, `make vuln` (no reachable vulnerabilities),
`make actionlint` and `go mod tidy` (no diff) all passed. `make release-check` was
not run locally (GoReleaser is not installed on this machine); the release workflow
runs it.

Acceptance, `make test-acc-supported` (the full provider and data-source suites on
one disposable fixture per version; top-level tests, no failures, no skips):

| Pocket ID | `internal/provider` | `internal/datasources` |
|---|---|---|
| 2.14.0 | 173/173 | 114/114 |
| 2.15.0 | 173/173 | 114/114 |
| 2.16.0 | 173/173 | 114/114 |
| 2.17.0 | 173/173 | 114/114 |

Native proofs, 12 of 12 passing, binaries built from `29af154` and stamped 3.0.0 in
a mirror: on 2.17.0 with both OpenTofu and Terraform, `lifecycle.py`, `upgrade.py`,
`client_upgrade.py` (empty plan after the switch), `upgrade_users_groups.py` and
`application_config.py`; `lifecycle.py` also on 2.14.0 with both tools. The old
provider for the upgrade proofs was the 2.4.104 archive kept from that release's
verification (`terraform-provider-pocketid_2.4.104_darwin_arm64.zip`; its unpacked
binary for `upgrade_users_groups.py`); the archive could not be re-verified
against the published SHA256SUMS during this run because the release asset was not
reachable anonymously.

Independent review: every package and the integrated tree were reviewed by Codex
(gpt-6-astra) in several rounds before and during integration; the reviews are
recorded with the release commit's history in the project's issue notes, not here.
Known limits carried into 3.0.0: PostgreSQL-backed servers were covered by source
inspection only (UUID comparison is case-insensitive for that reason); the
admin-key containment rules refuse legitimate values that happen to contain the key
(documented in CHANGELOG.md).

## Release 2.4.104 evidence — 2026-10-02

Pocket ID 2.17.0 support; supported servers move to 2.17.0 and 2.16.0. macOS ARM64,
Docker via OrbStack, Go 1.27.1, golangci-lint 2.13.2, tfplugindocs 0.25.0,
govulncheck 1.7.0, actionlint 1.7.12, OpenTofu 1.12.6, Terraform 1.16.4. The checks
and acceptance results below were produced from commit `67f35dc`, after the review
fixes described at the end of this section; the native results come from commit
`d47b611` and were not repeated after those fixes, which touch only the
cleanup-failure path, messages, tests and Make/CI targets, none of them exercised
by the native scripts.

The 2.16.0 to 2.17.0 server source was compared directly
(`backend/internal/dto`, `controller`, `model`, `appconfig`, `service`). The
management API changes are: `autoCreateOidcClientSecret` (required) in the
application-configuration update; `createdSecret` in the client-create response;
`backchannelLogoutURL` on clients. Users, groups, custom claims, SCIM, LDAP sync,
one-time access tokens, API keys and the version endpoint are unchanged on the wire.

Before the fixes (commit `4dd080d`: the 2.4.103 source with only the fixture
allowlist changed), client and application-config acceptance on 2.17.0 passed 21
of 24: `TestAccResourceApplicationConfig_basic` and `_dataSource` failed (HTTP 400)
and `TestAccResourceClient_secretContinuity` failed (two secrets). With the
application-config and secret-revocation commits, 25 of 25 passed. With only the
revoke disabled in a local build, both
`TestAccResourceClient_onlyProviderSecretAfterCreate` and
`TestAccResourceClient_secretContinuity` fail on 2.17.0.

Final results:

- `make check` (format, vet, unit tests with the race detector, build, lint: 0
  issues), `go vet -tags=acc ./...`, `make actionlint`, `go mod tidy -diff` and
  `make docs-check` are clean. `make vuln`: no reachable vulnerabilities; two
  advisories in required-module code that is not called, GO-2026-6179 and
  GO-2026-6180 in `golang.org/x/mod` v0.38.0 (fixed in v0.40.0).
- `python3 scripts/disposable-pocketid.py VERSION -- go test -v -count=1 -timeout 20m ./internal/provider -tags=acc`:
  **68 of 68** on 2.16.0 and on 2.17.0.
- The same for `./internal/datasources`: **41 of 41** on 2.16.0 and on 2.17.0.
- `make test-acc-provider POCKETID_VERSION=VERSION` (both packages, one fixture):
  **109 of 109** on 2.16.0 and on 2.17.0.
- `make test-acc-matrix` (client resource and data-source, and application-config
  acceptance): **28 of 28** on each of 2.14.0, 2.15.0, 2.16.0 and 2.17.0.
- New acceptance coverage: exactly one secret after creating a confidential client,
  matching the prefix of `client_secret`, and none on a public client, with a
  direct API call first proving 2.17.0 does create a secret of its own;
  `backchannel_logout_url` create, change, unrelated update, import, removal (each
  checked against the server), a value set outside Terraform showing as a planned
  change, plan-time rejection of http for a public client, a fragment and a
  relative URL, and on 2.14.0 to 2.16.0 refusal before any mutation; both client
  data sources reading the URL (on 2.16.0 and 2.17.0).
- `tests/native/upgrade.py` from the **published 2.4.103** darwin_arm64 archive (its
  SHA256SUMS file checked against the digest in the release notes, then the
  archive against that file) to this build, on 2.17.0 with **OpenTofu and
  Terraform**: empty plan after the upgrade; client ID, secret and replay protection
  kept through updates; a back-channel logout URL set outside Terraform kept by an
  update applied with `-refresh=false`, shown by the next refreshed plan, and an
  empty plan once configured. The client 2.4.103 created on 2.17.0 had two
  secrets. The same script with OpenTofu on 2.16.0 passes (one secret).
- A binary stamped 2.4.104 in an unpacked mirror passes `tests/native/lifecycle.py`
  (one secret, client-credentials authentication with it and failure with a wrong
  one, import, empty plan; OpenTofu with enforced state and plan encryption) and
  `tests/native/application_config.py` (old payload rejected, SMTP-only update
  preserving every returned setting, data source, removal) with both tools on
  2.17.0.

Review fixes (commits `d5cf11b` to `67f35dc`):

- After a failed rollback DELETE, a client counts as gone only on Pocket ID's
  structured not-found error for an OIDC client. The v2.14.0, v2.15.0, v2.16.0 and
  v2.17.0 source all return `apperror.NotFound("OIDC client")` from
  `getClientInternal` and `DeleteClient`, serialized by the error handler as
  `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"},...}`.
  Unit cases: a bare, HTML, missing-route and other-resource 404 each keep the
  client ID in state (`TestClientPartialCreation`); a rejected revoke followed by
  a failed cleanup keeps the ID with a bare or HTML 404 and is a verified rollback
  with the structured body; a rejected allowed-groups update after secret
  generation, followed by a failed cleanup, keeps both the ID and the generated
  secret. With the previous any-404 check restored, the four new
  `TestClientPartialCreation` 404 cases fail.
- With the Read assignment removed, both `TestClientReadBackchannelLogoutURL`
  cases fail now that they start from a stale value.
- Data-source acceptance now runs in `test-acc-provider` and, for the client data
  sources, in `test-acc` and `test-acc-matrix`.

Not run: Linux or any other cross-built platform; `make release-check`
(GoReleaser is not installed here); the native lifecycle and application-config
scripts on 2.16.0 and older; the full provider and data-source suites on 2.15.0
and 2.14.0; 2.17.0 with `autoCreateOidcClientSecret` turned off (the server then
returns no secret, the path 2.16.0 exercises); any live instance.

## Release 2.4.103 evidence — 2026-09-23

Adds the non-authoritative `pocketid_group_membership` resource and an `email`
lookup key on the `pocketid_user` data source. No schema change to any existing
resource or data source. Tested with OpenTofu 1.12.6 and Terraform 1.16.0 on
macOS ARM64, Docker via OrbStack.

- `go vet ./...`, `gofmt -l`, `golangci-lint run ./...` (v2.13.2), and
  `go mod tidy -diff` are clean. `go test -race ./internal/...` passes.
- `make docs` (tfplugindocs 0.25.0) regenerates only `docs/resources/group_membership.md`
  (new) and `docs/data-sources/user.md` (the `email` lookup key); `docs-check`
  is clean once committed.
- Reproduced live, then documented rather than changed: a `pocketid_group_membership`
  resource combined with a `pocketid_user` resource for the *same* user, where
  that `pocketid_user` resource never sets `groups`, plans to clear the
  membership on the very next refresh. `pocketid_user.groups` is Optional and
  not Computed, so an omitted `groups` in configuration is authoritative for
  "no groups" on every plan — the same behavior
  `TestAccResourceUser_withGroups`'s existing "Remove all groups" step already
  covers, just triggered here by a resource that never configured `groups` at
  all rather than one that cleared it. `pocketid_group_membership`'s
  acceptance tests therefore target users created directly through the API
  (never through a `pocketid_user` resource), and the resource's docs warn
  against combining it with `pocketid_user.groups` for the same user.
  `pocketid_group` does not manage membership in any form and is unaffected.

### Independent review (Codex, gpt-6-sol) and fixes — 2026-09-23

A read-only review of the three commits above found five issues, each
reproduced as a failing test against the pre-fix code before being fixed:

1. **P1, blocking the real use case (one user added to ~15 groups in one
   apply):** `Client.AddUserToGroup` / `RemoveUserFromGroup`'s read-modify-write
   against `PUT /api/users/{id}/user-groups` raced when two
   `pocketid_group_membership` resources for the same user ran concurrently —
   Terraform's default parallelism is 10. `TestAccResourceGroupMembership_manyGroupsOneApply`
   (one user, five groups, one apply) failed reliably against the unfixed
   code. Fixed by serializing Create/Delete per user ID inside the provider
   process with a package-level map of mutexes
   (`internal/resources/group_membership_resource.go`); a concurrent writer
   *outside* this provider process remains a documented, unfixable race (see
   the resource's own docs and the changelog).
2. **P2:** the `pocketid_user` data source's username/email lookups, and the
   `pocketid_users` list data source, called `ListUsers()` once and silently
   missed any user past the first page (server default 20 items per page).
   Fixed with `Client.ListAllUsers`, which follows pagination fully and also
   passes the search term to the server's `search` filter to narrow each page.
   `pocketid_users` had the identical bug (found while extending the fix, not
   in the original review) and uses the same helper.
3. **P2:** `pocketid_group_membership`'s Read and Delete treated *every* 404
   from checking the user as "gone" — including a 404 whose body means the API
   path itself doesn't exist (`HTTPError.MissingEndpoint`: wrong base URL, or a
   server too old to have the endpoint). Fixed to only treat a
   confirmed-missing user as gone; a missing-endpoint 404 now surfaces as an
   error on both paths.
4. **P2:** the new resource's own example suggested combining it with a
   `pocketid_user` resource that "never sets its own `groups` attribute" —
   directly contradicting the resource's own warning (and finding 1's
   evidence) that an omitted `groups` still authoritatively clears the
   membership. Removed the suggestion from the template, generated docs, and
   the example.
5. **P3:** the `pocketid_user` data source's exactly-one-of-`id`/`username`/`email`
   rule was enforced only at apply time inside Read. Added a `ConfigValidators`
   (`datasourcevalidator.ExactlyOneOf`) so it fails at plan/validate time too.

New coverage added for all five: `internal/resources/group_membership_resource_test.go`
gained concurrent-adds and mixed-concurrent-add/remove tests for one user (run
under `-race`) and missing-endpoint-vs-missing-user tests for Read and Delete;
`internal/client/list_all_users_test.go` gained pagination and page-2 tests
for `ListAllUsers`; `internal/datasources/user_data_source_test.go` and
`users_data_source_test.go` gained page-2 lookup tests and a `ConfigValidators`
test; `internal/provider/resource_group_membership_test.go` gained
`TestAccResourceGroupMembership_manyGroupsOneApply`.

After this round: the full acceptance suite passed 64 of 64 on both 2.14.0
and 2.15.0 (`./internal/provider -tags=acc`), and 40 of 40 on both
(`./internal/datasources -tags=acc`, still not wired into any Make target or
CI workflow, a pre-existing gap).

### Second independent review round (Codex, gpt-6-sol) — 2026-09-23

Re-review of the fixes above confirmed the lock, pagination, docs, and
`ConfigValidators` changes, and found two more issues:

1. **P2, blocking:** the fix above still accepted *every* 404 except the
   exact `{"error": "API endpoint not found"}` sentinel as "user gone" — a
   proxy or wrong base URL returning a generic (for example HTML) 404 would
   still have been misread as a missing user, and Delete separately accepted
   a 404 from the update-user-groups `PUT` itself, which does not prove the
   user is gone. Fixed by reading the actual Pocket-ID source for the exact
   positive signal (see below) instead of a negative "not this one other
   thing" check, and, in Delete, re-`GET`-ing on a `PUT` 404 and accepting
   only when that re-`GET` positively confirms the user is gone.
2. **P3:** `Client.ListAllUsers` treated a nonempty page reporting
   `pagination.totalPages` absent or zero as "no more pages", which would
   have silently truncated a result behind a server that omitted or
   misreported that field. Fixed to return an error for that case instead of
   guessing.

**Finding the exact confirmation signal:** read `backend/internal/service/user_service.go`,
`backend/internal/apperror/{error,constructors}.go`, `backend/internal/middleware/error_handler.go`,
and `backend/internal/dto/error_dto.go` from the pinned `v2.14.0` and `v2.15.0`
tags via `gh api`/`raw.githubusercontent.com`. `GetUser` returns
`apperror.UserNotFound()` (`New(CodeUserNotFound, http.StatusNotFound, "User
not found")`, `CodeUserNotFound = "user_not_found"`) on `gorm.ErrRecordNotFound`;
`ErrorHandlerMiddleware` serializes any `*apperror.Error` as
`dto.ErrorDto{Error, Code, Details, RequestID}` (`json:"error"`, `json:"code"`,
...). So a confirmed-missing user is `HTTP 404` with body
`{"error": "User not found", "code": "user_not_found", "request_id": "..."}` —
identical on both `v2.14.0` and `v2.15.0`. The "API endpoint not found"
sentinel, by contrast, is a bare `gin.H{"error": ...}` from the router's
unmatched-route handler (`backend/frontend/frontend_included.go`) with no
`code` field, so the two never collide. Added `HTTPError.UserNotFound` (set
only for this exact shape) and `client.IsUserNotFound(err)`; `RemoveUserFromGroup`
now also re-`GET`s on a 404 from its own `PUT` and only treats that as
"already removed" when `IsUserNotFound` confirms it there too.

New coverage: nine `internal/client/group_membership_test.go` cases covering
a confirmed-missing user (no `PUT` attempted), a generic 404 on the initial
`GET` (error), a `PUT` 404 with the user still present on re-`GET` (error), a
`PUT` 404 with the user confirmed gone on re-`GET` (success), and a `PUT` 500
(no re-`GET` attempted, error); `internal/client/list_all_users_test.go`
gained a malformed-pagination case; `internal/resources/group_membership_resource_test.go`
gained the same generic-404 and `PUT`-404 cases at the resource level for
Read and Delete; `internal/provider/resource_group_membership_test.go` gained
`TestAccResourceGroupMembership_deleteWhenUserAlreadyGone` (deletes the user
out of band, then confirms the whole lifecycle - refresh and, when reached,
Delete - stays error-free).

Final counts after both review rounds:

- `go vet ./...`, `go vet -tags=acc ./...`, `gofmt -l`, `golangci-lint run
  ./...` (0 issues), `go mod tidy -diff`, and `govulncheck` (0 reachable, same
  2 pre-existing unused-module advisories) are all clean. `go test -race
  ./internal/...` passes.
- `make docs` / `docs-check`: no drift (this round changed no schema).
- The full acceptance suite (`./internal/provider -tags=acc`) passes on both
  2.14.0 and 2.15.0: **65 of 65 tests** (64 after the first review round,
  plus `TestAccResourceGroupMembership_deleteWhenUserAlreadyGone`).
- `./internal/datasources -tags=acc` passes on both 2.14.0 and 2.15.0: **40 of
  40 tests** (unchanged by this round; still not wired into any Make target
  or CI workflow, a pre-existing gap).
- Not run: Linux or any other cross-built platform, Pocket ID 2.16, native
  Terraform/OpenTofu lifecycle/upgrade rehearsal (no schema change, so none is
  required by INSTALL.md's own criteria), and any live instance.

## Release 2.4.102 evidence — 2026-09-23

Upstream #116 cherry-picked with authorship kept. Its pre-update read is merged with
the existing federated-identity read, so an update makes one read. The
application-configuration payload now starts from a copy of the server
configuration, as in upstream #103.
Tested with OpenTofu 1.12.6 and Terraform 1.16.0 on macOS ARM64.

- `make check`, `make vuln`, `make docs-check` and `go mod tidy -diff` pass.
- The full acceptance suite passes on both 2.14.0 and 2.15.0. It now includes
  upstream's `TestAccResourceClient_preservesUnmanagedFields`: it sets description,
  skip-consent and both token lifetimes through the API, renames the client, and
  checks that all four survive. With only the `preserveUnmanagedClientFields` call
  removed, it fails on 2.15.0.
- `TestApplicationConfigSMTPPreservesSettings` still sets every modelled field and
  requires an SMTP-only update to send each one back unchanged.
- A binary stamped 2.4.102 in a packed mirror passes `tests/native/lifecycle.py` and
  `tests/native/application_config.py`, both fresh and upgraded from the published
  2.4.1 archive, with Terraform and OpenTofu on both 2.14.0 and 2.15.0.
- `tests/native/upgrade.py` from the **published 2.4.1** darwin_arm64 archive (checked
  against its release's SHA256SUMS digest), with both tools on 2.15.0: empty plan,
  client ID, secret and replay protection kept through an update.

Not run: Linux or any other cross-built platform, Pocket ID 2.16, and any live instance.

## Release 2.4.1 evidence — 2026-09-20

An independent read-only review of published 2.4.0 (Codex, gpt-6-astra) reported three
defects and one validation gap. Each defect was first reproduced as a failing
acceptance test against the 2.4.0 code on Pocket ID 2.15.0, with the predicted cause:

| Case | 2.4.0 result |
|---|---|
| Two identities sharing issuer/subject/audience, `replay_protection` omitted, unrelated rename | second identity silently went `true` → `false` |
| `replay_protection = terraform_data.flag.output` (unknown while planning) | "Provider produced invalid plan ... planned value cty.True does not match config value" |
| `public_keys = [valid, null]` | "inconsistent result after apply ... element 1 has vanished", after the server was changed |

Same platform and tool versions as 2.4.0. With the fixes:

- `make check`, `make vuln`, `make actionlint`, `go mod tidy -diff` and the generated-doc
  check pass. The full acceptance suite (56 tests) passes on both 2.14.0 and 2.15.0.
- The public-key acceptance test now also round-trips an RSA key carrying `alg`, so the
  server's re-encoding is shown not to read as drift for RSA as well as EC keys.
- `tests/native/upgrade.py` from the **published 2.3.2** archive, Terraform and OpenTofu:
  an administrator enables replay protection on one of two identities outside
  Terraform; the new build applies an unrelated update with `-refresh=false` while state
  still predates the attribute, and each identity keeps its own server value; later
  plans are empty. From the **published 2.4.0** archive: empty plan, and identity,
  secret and replay protection kept through an update.
- A binary stamped 2.4.1 passes `tests/native/lifecycle.py` with both tools on both
  versions, and `tests/native/application_config.py` upgraded from 2.3.2 and from 2.4.0
  with both tools on 2.15.0.

The review also checked, and found sound: rebuilding the planned list with the nested
custom type, distinct-identity reordering, unknown identity fields, create/replace/
import, whole-list null/unknown handling, semantic JSON equality on Create, Update and
Read, the private-parameter list, pre-release and missing-version handling in the
2.15.0 gate, and the absence of a state upgrader. Two limits it noted stand: if a
future server reorders keys or adds or drops JWK members when re-encoding, that would
read as drift; and an explicit `subject = ""` or `audience = ""` reads back as null,
which predates these releases.

Not run: Linux or any other cross-built platform, and any live instance.

## Release 2.4.0 evidence — 2026-09-20

The supported matrix moves to **Pocket ID 2.14.0 and 2.15.0** (official Linux ARM64
images); 2.13.0 is retired from support, CI and the fixture allowlist. Local
platform darwin_arm64, Go 1.27.1, golangci-lint 2.13.2, Terraform 1.16.0 and
OpenTofu 1.12.6.

- The 2.14.0..2.15.0 server source was compared directly. The management API gains
  two read-only routes and one writable field, `publicKeys` on a federated identity;
  the client list now returns `allowedUserGroups` instead of a count, which this
  provider never read. The application-configuration DTO is byte-identical, and
  `/api/version/current` moved modules with the same path, auth and response.
- Released 2.3.2, unchanged, passes the full acceptance suite on 2.15.0.
- `make check`, `make actionlint`, `go mod tidy -diff` and `make vuln` pass.
  `make vuln` first reported reachable GO-2026-6443 in gRPC 1.83.1, the transport
  between Terraform and the provider; 1.83.2 clears it, and the suite below was
  re-run on 2.15.0 afterward. Two advisories remain in unused required-module code.
- The full acceptance suite (53 tests) passes on both 2.14.0 and 2.15.0. On 2.14.0 `public_keys` is refused before any mutation; on
  2.15.0 keys written with padding and a different member order survive create,
  an unrelated update, a key-set change, import and removal with empty follow-up
  plans, which proves the server's re-encoding does not read as drift.
- `replay_protection`: an explicit `false` holds; an omitted value keeps an existing
  identity's setting through an unrelated update and through inserting a new
  identity ahead of it, and a new identity gets `true`.
- The secret-continuity check and `tests/native/lifecycle.py` selected their 2.14+
  assertions with `== "2.14.0"`, so they were silently skipped on any later server.
  They now compare versions and run on 2.15.0.
- Upgrade from the released archive, with native **Terraform and OpenTofu** on 2.15.0:

```sh
make build
python3 scripts/disposable-pocketid.py 2.15.0 -- python3 tests/native/upgrade.py tofu \
  "$PWD/dist/terraform-provider-pocketid_2.3.2_darwin_arm64.zip" "$PWD/bin/terraform-provider-pocketid"
```

  Released 2.3.2 creates a confidential client with a federated identity. The new
  build then plans nothing, keeps the client ID and secret, records the server's
  `replayProtection` on refresh, and leaves it unchanged through an unrelated update.

- A binary stamped 2.4.0 in an isolated unpacked mirror, beside the published 2.3.2
  archive, passes `tests/native/lifecycle.py` and `tests/native/application_config.py`
  with native **Terraform and OpenTofu on both versions**: install, create, refresh,
  update, import, empty plan, delete, one secret and usable client authentication;
  old-payload HTTP 400, SMTP-only update preserving every unrelated returned
  setting, data-source reads and removal; and the same SMTP lifecycle started under
  2.3.2 and upgraded to 2.4.0. OpenTofu runs enforce state, backup and saved-plan
  encryption.

Not run: Linux or any other cross-built platform, and any live instance.

## Release 2.3.2 evidence — 2026-09-06

The supported matrix is **Pocket ID 2.13.0 and 2.14.0**, using official Linux
ARM64 images. Version 2.9.0 is retired from support and the fixture allowlist.
The tool/platform versions below remain the same as 2.3.1. This candidate also
includes the dependency and maintenance cleanup at `327dafd`.

- `make check`, `make actionlint`, `make vuln`, `go mod tidy -diff` and
  generated documentation checks pass. The vulnerability scan found no reachable
  advisories or affected imported packages; two advisories remain in unused
  required-module code.
- Focused resource/client/data-source tests and `go test -race ./internal/...`
  pass. The HTTP regression exercises GET/merge/PUT/response with null and unknown
  attributes, nondefault passkey policy, restrictive CIMD allowlist, and every
  unrelated modeled setting. Explicit `false` and explicit empty values remain
  distinct from omission.
- All client and application-configuration acceptance tests pass on both supported
  versions, including the two application-config failures deferred in 2.3.1.
  The full 2.14 provider acceptance suite also passes, with no exclusions.
  Existing OIDC client-secret code is unchanged.
- Native **Terraform and OpenTofu on both versions** first submit the old payload
  to the actual server validator and require HTTP 400 with no changes. They then
  import an empty configuration resource, refresh, require an empty plan, apply
  SMTP-only changes, compare every returned server setting, verify resource and
  data-source mappings, refresh/reimport and require empty plans again. Removing
  attributes or destroying the singleton resource preserves server configuration.
- Upgrading the real v2.3.1 archive to the candidate passes on both supported
  server versions with both native tools, including refresh and an empty plan.
- Native OpenTofu verifies enforced encryption for state, backups and a saved
  no-change plan. Synthetic credentials exist only in isolated temporary test
  environments; no production credentials or endpoints are used. SMTP delivery
  and browser/passkey login are outside these provider tests.

Pinned contract references: [v2.14.0 request schema](https://github.com/pocket-id/pocket-id/blob/v2.14.0/backend/internal/dto/app_config_dto.go),
[v2.14.0 controller](https://github.com/pocket-id/pocket-id/blob/v2.14.0/backend/internal/controller/app_config_controller.go),
and [v2.13.0 request schema](https://github.com/pocket-id/pocket-id/blob/v2.13.0/backend/internal/dto/app_config_dto.go).

To repeat the native SMTP regression, build the release binary in an isolated
unpacked mirror (do not install over an existing provider), then run:

```sh
mkdir -p /tmp/pocketid-test-mirror/registry.terraform.io/irashack/pocketid/2.3.2/darwin_arm64
go build -ldflags '-X main.version=2.3.2' -o /tmp/pocketid-test-mirror/registry.terraform.io/irashack/pocketid/2.3.2/darwin_arm64/terraform-provider-pocketid_v2.3.2 .
for version in 2.14.0 2.15.0; do
  for tool in terraform tofu; do
    python3 scripts/disposable-pocketid.py "$version" -- python3 tests/native/application_config.py "$tool" /tmp/pocketid-test-mirror 2.3.2
  done
done
```

For upgrade coverage, also place the verified v2.3.1 archive in the same isolated
mirror and append `2.3.1` to the native test command. It imports with that old
binary, changes the version pin to 2.3.2, initializes, then requires refresh and
an empty plan before the SMTP update.

Substitute the platform directory for your test host. Release packaging uses
GoReleaser as documented in RELEASE_CHECKLIST.md. Production adoption is separate.

## Historical release 2.3.1 evidence (not the current support matrix)

Local platform: **darwin_arm64**. Go **1.27.1**, golangci-lint **2.13.2**,
Terraform **1.16.0**, OpenTofu **1.12.6**, GoReleaser **2.18.1**.
Official Pocket ID images **2.9.0, 2.13.0, 2.14.0** run as Linux ARM64 containers.

- The #96 regression failed on the untouched upstream v2.3.0 client (HTTP 404),
  then passed with the compatibility patch.
- Unit coverage includes actual methods/paths and old/new response bodies,
  semantic ordering including 2.9, verified/unverified missing routes, malformed
  or empty version bodies, authentication/server/transport failures, disconnected
  and timed-out secret responses, empty/malformed secrets, no mutation retries,
  public-client preflight, fixed-ID conflicts, partial creation, failed cleanup,
  uncertain cleanup confirmed absent, and trace-log/diagnostic secret exclusion.
- Client acceptance covers create/read/import/update/delete and secret continuity
  on the listed image versions. On 2.14, continuity additionally checks exactly
  one secret using the read-only metadata endpoint.
- Native Terraform and OpenTofu exercise create, refresh, metadata update, import,
  empty plan and deletion. On 2.14 they also prove client-credentials authentication
  succeeds with the generated secret and fails with an intentionally wrong secret.
  Import returns no secret and does not rotate the existing one.
- OpenTofu native tests enable AES-GCM state **and plan** encryption. They check
  state/backup envelopes and that a saved plan is encrypted and readable with the
  key. This is separate from sensitive marking.

### Historical 2.3.1 scope limits (application-config failures fixed in 2.3.2)

A full upstream acceptance run on **2.13.0** found two failing tests:
`TestAccResourceApplicationConfig_basic` and
`TestAccResourceApplicationConfig_dataSource` (HTTP 400 on configuration update).
They concern application-wide configuration, not client-secret creation, and are
left as a bounded backlog. The release does not claim the entire suite is green.
The broader 2.14 provider acceptance run excludes only these application-config
tests; client, group, user, one-time-token, LDAP-disabled and SCIM tests pass.
Duplicate-user/group assertions were updated to expect status-only HTTP 409
rather than arbitrary server text, consistent with secret-safe diagnostics.

Pocket ID releases earlier than 2.9 and later than 2.14.0 were not live-tested.
Terraform/OpenTofu versions other than those named were not tested. Cross-built
Windows, FreeBSD, Darwin AMD64, Linux 386 and ARM variants are not runtime-tested
locally. Linux CI results, when available, belong to their linked workflow run;
a successful cross-build alone is not a live compatibility result.

## Maintenance cleanup verification — 2026-09-06 (unreleased source)

After the dependency updates recorded in UPSTREAM.md, local darwin_arm64 checks
passed using Go 1.27.1 (now the preferred toolchain in go.mod):

- `make check`: format, vet, unit/race tests, build and golangci-lint 2.13.2.
- `make actionlint`: workflow syntax and expression validation, actionlint 1.7.12.
- `make vuln`: govulncheck 1.7.0 found no reachable vulnerabilities and none in
  imported packages; two advisories remain in required modules outside the
  imported package graph. This is a point-in-time scan, not a blanket guarantee.
- `make test-acc-matrix`: client lifecycle on official Pocket ID 2.9.0, 2.13.0
  and 2.14.0 images, with each disposable container removed afterward.
- `make test-acc-provider`: broader 2.14 acceptance, explicitly excluding
  `TestAccResourceApplicationConfig*` as documented above.
- `make docs`: generated docs from the actual provider schema with tfplugindocs
  0.25.0. Example Terraform formatting and local README links passed.
- GoReleaser 2.18.1 configuration validation and `go mod tidy -diff` passed.

These are source checks, not a new release or native binary migration rehearsal.
The changed GitHub workflows have been checked locally but not run on GitHub.
CI selects the preferred toolchain from go.mod; the lower `go` directive remains
the language/module minimum rather than the CI toolchain selection.
