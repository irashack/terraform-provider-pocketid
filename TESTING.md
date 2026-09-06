# Maintenance release validation

## Reproducible checks

```sh
go test -race ./internal/...
go vet ./...
golangci-lint run ./...
python3 scripts/disposable-pocketid.py 2.9.0 -- go test -v -count=1 -timeout 15m ./internal/provider -tags=acc -run '^TestAccResourceClient'
python3 scripts/disposable-pocketid.py 2.13.0 -- go test -v -count=1 -timeout 15m ./internal/provider -tags=acc -run '^TestAccResourceClient'
python3 scripts/disposable-pocketid.py 2.14.0 -- go test -v -count=1 -timeout 15m ./internal/provider -tags=acc -run '^TestAccResourceClient'
```

The fixture pulls official versioned Pocket ID images. It creates an isolated
container and database, waits for health (HTTP 204 is success), stops the server,
and seeds one synthetic administrator and a hash of a random API token, following
upstream's fixture strategy. It restarts before tests and removes its container
and anonymous volumes afterward. No production URL, database, or user input is
accepted. Credentials pass only through process memory/environment. Failure
output is suppressed because the acceptance harness may include state in errors.
For local diagnosis only, `POCKETID_FIXTURE_FAILURE_LOG` can name a private file
outside source; never publish that file. Logs/state are not release assets.

Native binary tests use an already populated filesystem mirror:

```sh
python3 scripts/disposable-pocketid.py 2.14.0 -- python3 tests/native/lifecycle.py terraform /absolute/mirror
python3 scripts/disposable-pocketid.py 2.14.0 -- python3 tests/native/lifecycle.py tofu /absolute/mirror
PROVIDER_MIGRATION_TEST=1 python3 scripts/disposable-pocketid.py 2.13.0 -- python3 tests/native/lifecycle.py tofu /absolute/mirror
```

The migration case additionally needs the genuine upstream 2.3.0 artifact under
`registry.opentofu.org/trozz/pocketid` in the isolated mirror. It must not be a
renamed fork binary. The test uses supported state replacement, checks encrypted
state/backups and saved-plan encryption, preserves the ID and secret, and requires
an empty subsequent plan. No development overrides are used.

## Release 2.3.1 evidence — 2026-09-06

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

### Known scope limits

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
