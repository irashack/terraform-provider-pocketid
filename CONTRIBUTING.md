# Contributing

This is an independent project: it decides its own scope, fixes and extends what
it needs, and does not track upstream (see [UPSTREAM.md](UPSTREAM.md)). It is
volunteer work with no response-time guarantee. Fixes, features and corrections
are welcome.

## Proposing a change

- Open an [issue](https://github.com/irashack/terraform-provider-pocketid/issues)
  first for anything larger than a fix, so the approach is agreed before the work.
  Report vulnerabilities through [private reporting](SECURITY.md), not an issue.
- Open pull requests against `main-maintenance`. The branch keeps the name it had
  when this was a maintenance fork, and CI and the release workflow are tied to
  it. CI needs no production secrets and accepts pull requests with read-only
  permissions.
- Keep a change small, reproducible and independently reviewable: one logical change
  per commit. A commit message starts with `fix:`, `feat:`, `docs:`, `test:`,
  `build:`, `ci:`, `chore:` or `release:` and says what changed and why. There is no
  bot-enforced format.
- Never attach tokens, client secrets, state, raw plans, private deployment
  configuration or production logs to an issue or pull request.
- Contributions are MIT licensed, like the rest of the repository. Retain
  authorship when you cherry-pick from upstream or any other project.

## What a change must keep true

The conventions in the [README](README.md#conventions) apply to every resource and
data source. Beyond them, a change must not regress these:

- Changes (POST, PUT, DELETE, uploads) are never retried automatically.
- No secret reaches a log, a diagnostic, an error string or non-sensitive state:
  not the API key, a client secret, a SCIM or signup token, a password or a
  one-time token. Response bodies and headers are never echoed. Redirects are not
  followed.
- A full-replace PUT sends back every field the provider does not manage,
  unchanged. A plan shows every change an apply will make.
- A failed multi-step create never claims a rollback it did not confirm, and
  never drops the created object's ID or a generated secret from state while the
  object may exist.
- Access control fails closed: nothing may widen who can sign in to a client or
  what a user is granted unless the plan showed exactly that change.
- A feature that needs a newer Pocket ID checks the server version before any
  change and fails with a clear message.
- State written by the previous release keeps working. A new attribute on an
  existing resource decodes from old state without a state upgrader where the
  framework allows it, and an unchanged configuration plans empty. Prove it with
  a native upgrade script (see [TESTING.md](TESTING.md)) and say so if you
  could not.

## Checks

Make downloads pinned lint, documentation and vulnerability tools through Go on
first use. Run these before opening a pull request:

```sh
make check              # format, vet, race unit tests, script tests, build and lint
make docs-check         # generated documentation matches the checkout
make actionlint         # for workflow changes
make vuln               # reachable Go vulnerabilities
make test-acc-supported # the full acceptance suites on Pocket ID 2.14.0 to 2.17.0
```

`make test-acc-supported` is long; while working, run
`make test-acc-provider POCKETID_VERSION=2.17.0` (both suites on one version) or
`make test-acc` for the client, application-configuration and API-contract
subset, and `make test-acc-matrix` for that subset on every version. CI runs the
full suites on 2.16.0 and 2.17.0. TESTING.md has the details, including the native
Terraform and OpenTofu scripts.

Every behavior change needs a unit test and, wherever the real server's behavior
is what is being relied on, an acceptance test (build tag `acc`, in
`internal/provider` or `internal/datasources`) that fails when the fix is removed.
A test that only restates the implementation does not count.

Documentation under `docs/` is generated: change a schema description, a file in
`templates/` or an example in `examples/`, then run `make docs`. Never edit `docs/`
by hand.

## Fixtures are the only way to run a server

Tests never talk to a running Pocket ID instance of yours, and nothing starts a
server by hand. The only way to run one during tests is the fixture script:

```sh
python3 scripts/disposable-pocketid.py 2.17.0 -- go test -count=1 ./internal/provider -tags=acc
```

It pulls the official image for a version from 2.14.0 to 2.17.0, starts an
isolated container on a free loopback port, seeds a fresh database with one
synthetic administrator and a random token held only in memory, runs the command
with `POCKETID_BASE_URL`, `POCKETID_API_TOKEN` and `POCKETID_TEST_VERSION` set,
and removes the container afterward. It accepts no production URL or database.
Docker is required. Several fixtures can run at once; if one only fails because
the server did not become healthy in time, run it again.

## Changelog fragments

Do not edit `CHANGELOG.md` in a change. Record each user-visible change in
`changelog.d/<topic>.md`, a plain Markdown bullet list written for someone who uses
the provider: what changed, and what they must do about it. Start a breaking
change's bullet with **Breaking:**. Leave out refactoring and test-only changes.
The release integrator merges the fragments into `CHANGELOG.md` and deletes them;
see [changelog.d/README.md](changelog.d/README.md).

## Other notes

Optional local hooks use `pre-commit install`; they run the same Make targets and
do not replace CI. Dependabot proposes weekly grouped Go and Actions updates; Go
tools invoked from Make are versioned explicitly and need manual review. Never copy
a development build over a versioned release in a mirror. The Go module is
`github.com/irashack/terraform-provider-pocketid`; the installable provider address
is in [INSTALL.md](INSTALL.md).
