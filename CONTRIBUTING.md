# Contributing and upstream return

Keep fixes small, reproducible and independently reviewable. Run `make check`,
`make actionlint` for workflow changes, and `make docs` for schema or example changes.
Make downloads pinned lint, documentation and vulnerability tools through Go on first use.
Use `make test-acc-matrix` for client changes and `make test-acc-provider` for broader checks.
See TESTING.md for application-config regression coverage and native migration tests. Never attach tokens, client
secrets, state, raw plans, private deployment configuration, or production logs.

This fork follows upstream's MIT license. Retain authorship when cherry-picking.
The v2.3.1 compatibility backport derives from Mathieu Lemay's PR #97 and Yusaku
Mizobuchi's tests. Markus Schanz's PR #90 was reviewed for shared version-check and
secret-endpoint overlap; its independent features were intentionally excluded.

Prefer returning a focused patch to upstream. CONTRIBUTION-DRAFT.md is prepared
text, not a posted message or a support promise. Before proposing it, compare the
current PR heads: do not repeat resolved findings or absorb unrelated features.

Return consumers to upstream when a stable upstream release passes the same
old/new contract and lifecycle tests and a supported state-provider migration
produces no replacements, preserving client identities and existing secrets.
Upstream remains active; the fork makes this bounded tested fix available while
review proceeds. The current upstream issue/PR review and follow-up criteria are in [UPSTREAM.md](UPSTREAM.md).
There is no automatic upstream merge.

Open PRs against `main-maintenance`. CI needs no production secrets and accepts
fork contributions through `pull_request` with read-only permissions. Optional
local hooks use `pre-commit install`; they do not replace CI. The Go module path
remains upstream's to minimize source divergence; the installable provider identity
is the independent fork address in INSTALL.md. Never copy a development build
over a versioned release in a mirror.
