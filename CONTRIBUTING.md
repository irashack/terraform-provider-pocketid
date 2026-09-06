# Contributing and upstream return

Keep fixes small, reproducible and independently reviewable. Use `go test ./internal/...`,
`go vet ./...`, and the configured golangci-lint suite. For client changes run the
three official-image acceptance cases in TESTING.md. Never attach tokens, client
secrets, state, raw plans, private deployment configuration, or production logs.

This fork follows upstream's MPL-2.0 license. Retain authorship when cherry-picking.
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
review proceeds. There is no scheduled upstream merge or issue sweep.
