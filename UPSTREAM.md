# Upstream review — 2026-09-06

This is a point-in-time review, not an automatic merge policy. Upstream is
[Trozz/terraform-provider-pocketid](https://github.com/Trozz/terraform-provider-pocketid).
The fork release 2.3.1 starts at `44c32e0` (v2.3.0). Upstream `main` was `c3dcfcb`,
two commits ahead of that base: dependency PR #84 and Actions PR #85.

## Open issues and pull requests

All seven open PRs and both open issues were inspected through the GitHub API.
The dependency changes below are in the working source after 2.3.1; they are not
included in the already published 2.3.1 archives. No upstream PR was merged remotely.

| Item | Disposition and next step |
|---|---|
| [#96: client creation on 2.14](https://github.com/Trozz/terraform-provider-pocketid/issues/96) / [#97: compatibility fix](https://github.com/Trozz/terraform-provider-pocketid/pull/97) | Already incorporated in 2.3.1, with stricter version checks, no mutation retries and partial-creation protection. Latest PR head inspected: `97aeedf`. |
| [#92: allowed groups as a set](https://github.com/Trozz/terraform-provider-pocketid/pull/92) | Real recurring-plan bug; highest-priority functional follow-up. The patch changes the resource and two data sources from list to set. It does not add a schema-version migration or an old-binary state upgrade test, and indexed expressions change meaning/validity. Before adoption, test existing 2.3.1 state with native Terraform and OpenTofu, omitted/empty groups, reordered groups, real membership changes, import and secret continuity. Decide and document the versioning contract. Do not silently ship as a schema-preserving patch. |
| [#90: declarative IDs and secrets](https://github.com/Trozz/terraform-provider-pocketid/pull/90) | Defer as a separate feature. Its inspected client code still posts to singular `/secret`, and creation cleanup discards deletion errors. Direct application conflicts with the fork's 2.14 selection and ambiguous-failure guarantees. Separate user-ID support from secret rotation, define multi-secret semantics and test 2.12/2.13/2.14 failure/recovery paths before adoption. |
| [#87: x/net](https://github.com/Trozz/terraform-provider-pocketid/pull/87) | Integrated the proposed `v0.57.0` and its required Go dependency updates, superseding merged #84's `v0.55.0`. |
| [#98: Go minor dependencies](https://github.com/Trozz/terraform-provider-pocketid/pull/98) | Integrated plugin-log `v0.11.0` and testify `v1.12.1`; resolved checksums using Go modules. |
| [#99: gRPC](https://github.com/Trozz/terraform-provider-pocketid/pull/99) | Integrated `v1.83.1` and its genproto update. This supersedes the now-closed #95. |
| [#94: Actions updates](https://github.com/Trozz/terraform-provider-pocketid/pull/94) | Selected checkout, setup-go and attestation revisions used by our two workflows. Did not restore removed upstream jobs to consume their updates. SHA comments corrected where upstream labels were stale. |
| [#25: registry documentation](https://github.com/Trozz/terraform-provider-pocketid/issues/25) | Local docs exist, but this fork is not registry-published. Corrected active examples and provider docs to the fork source/version and linked mirror installation. Registry signing/registration remains a separate release milestone. |

## Actions: retain two workflows

GitHub settings were checked: Actions enabled, both workflows active, and the
existing [Maintenance CI run](https://github.com/irashack/terraform-provider-pocketid/actions/runs/34022442647)
passed. That run predates this cleanup and does not validate these changes.
Issues and private vulnerability reporting were disabled; both were enabled in
this pass. Reports use GitHub Issues and private vulnerability reporting. GitHub
account notification delivery settings were not changed.

| Upstream workflow | Fork decision |
|---|---|
| `ci.yml` | Keep unit/race tests, vet/lint, build and disposable acceptance. Add workflow validation and reachable Go vulnerability checks. Run older client contracts and broader 2.14 acceptance once each. Add timeouts, manual dispatch and cancellation of superseded CI runs. |
| `validation.yml`, `pre-commit.yml`, `conventional-commits.yml` | Consolidate useful format checks into Make/CI. Retire duplicate shell validators and PR-comment plumbing; do not require a bot-enforced commit-title format. Local hooks are optional and use the same targets. |
| `security.yml`, `codeql.yml` | Retain Go vulnerability detection in CI. Do not restore the overlapping Trivy/gosec/CodeQL/SARIF workflow stack during this pass. The existing lint suite covers static checks; this is not a claim that it duplicates every scanner. |
| `release.yml` | Keep manual release, stable-tag/branch validation, tests on tagged source, pinned GoReleaser, checksums and attestations. Produce a draft for final artifact verification. No registry signing key is configured. |
| `pre-release.yml`, `cleanup-prereleases.yml` | Keep removed. Automatic dev releases and scheduled deletion are unnecessary for this fork. |
| `contributors.yml` | Keep removed. Preserve authorship in git and LICENSE without a bot writing to PR branches. |

Dependabot remains the single update proposal mechanism, with weekly grouped
compatible Go updates and Actions updates. Go tools invoked from Make are explicitly
versioned and need manual version review; Dependabot does not update those pins.
Removed unused Codecov configuration, CI-comment scripts, old fixed-port fixture,
tracked scan cache and redundant validators. The disposable fixture is canonical.

Application-config HTTP 400 failures are fixed in candidate 2.3.2 by preserving
the required WebAuthn fields and CIMD allowlist. Both supported server versions
now run application-config acceptance; the full 2.14 suite has no exclusion.

## Pocket ID 2.15.0 — 2026-09-20

Upstream `main` is unchanged since the review above (`c3dcfcb`, no release after
v2.3.0), so nothing there addresses 2.15.0. The fork's release 2.4.0 adds federated
identity `replay_protection` and `public_keys`; upstream has the same silent reset of
both on every client update, which is worth offering back. Evidence is in
[TESTING.md](TESTING.md).

## Upstream 2.4.0–2.4.2 — 2026-09-23

Upstream released 2.4.0, 2.4.1 and 2.4.2 on 2026-09-21 and 22 (`main` at `292900b`).
They merge #90, #92 and #97, fix application-config preservation in #103 by copying the
full server object, and in #116 stop client updates from resetting description,
skip-consent, token lifetimes and logos. The fork took #116 and #103's
copy-first application configuration as 2.4.101. Upstream's
version numbers now overlap the fork's, with different content, so fork patch releases
on the 2.4 line are numbered from 2.4.101.

Upstream had no open pull requests. Still missing upstream and worth offering, most
valuable first:

1. Federated identity `replay_protection` and `public_keys`. Every client update still
   disables replay protection (a server setting since 2.12) and drops explicit keys;
   #116 does not touch the identity list.
2. Mutation retries. Upstream retries POST/PUT/DELETE after a 5xx or connection reset.
   A retried secret POST leaves an orphaned secret that #117's revocation never
   reaches; a retried create duplicates a client or fails on its fixed ID; a retried
   DELETE fails on 404.
3. A failed client creation. Upstream discards the cleanup delete's error and always
   says the client was deleted.
4. Secret endpoint selection falls back to the singular endpoint on any version-read
   error, and only after the client exists.

Upstream's application-config fix starts from a copy of the server configuration,
which is more robust than the fork's field-by-field copy; 2.4.101 adopts that shape
and keeps the fork's four extra attributes. #92 needs no state upgrader (a stored list decodes as a set) but
breaks index expressions on `allowed_user_groups`.

## Remaining work

1. Offer the items above upstream. When they are released there, return to
   `trozz/pocketid` rather than porting #90, #92 and #117 here.
2. Register/sign the fork only if independent distribution continues; until then
   the verified filesystem mirror remains the supported install path.

Return to upstream when a stable upstream release passes the old/new API and
native lifecycle tests, then rehearse supported state-provider replacement with
no client replacement or secret rotation. See [CONTRIBUTING.md](CONTRIBUTING.md).
