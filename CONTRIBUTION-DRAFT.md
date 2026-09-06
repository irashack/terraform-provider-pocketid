# Draft upstream contribution text — not posted

Confidential client creation against Pocket ID 2.14 requires the plural `/secrets`
endpoint. This backport starts from v2.3.0 and includes the current PR #97 commits,
retaining Mathieu Lemay and Yusaku Mizobuchi's attribution. The original string
ordering finding is already fixed in #97 and is not a new review finding.

Follow-up changes reject invalid/empty version responses, accept legacy fallback
only on the verified missing-route response, disable retries for mutations, and
keep HTTP bodies out of logs and diagnostics. Partial creation checks cleanup
results, retains known identities on uncertainty, and never generates a second
secret automatically. Tests cover 2.9 ordering, actual HTTP methods and response
shapes, public clients, no-rotation lifecycle behavior and failed rollback.

PR #90 adds separate declarative user-ID/custom-secret features. Its version check
and `/secret` changes overlap; the compatibility helper should be reconciled when
those features are integrated. This release does not bring those features in.

Exact integration results and known unrelated failures are in TESTING.md.
