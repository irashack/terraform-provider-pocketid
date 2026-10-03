# Release procedure

1. Integrate the work. Merge each `changelog.d/*.md` fragment into a new
   `CHANGELOG.md` section, keeping every **Breaking** mark, and delete the
   fragments. Update README, TESTING and INSTALL (which gets an upgrade section for
   the release). Preserve LICENSE and upstream authors.
2. Run the checks: `make check`, `make docs-check`, `make actionlint`, `make vuln`,
   `go vet -tags=acc ./...`, `go mod tidy -diff` and, with GoReleaser 2.18.1
   installed, `make release-check`. Record exact tool versions and any failure.
3. Run the full acceptance suites on every supported server:
   `make test-acc-supported`. It runs `make test-acc-provider` (the provider and
   data-source suites on one fixture) on Pocket ID 2.14.0, 2.15.0, 2.16.0 and
   2.17.0 in turn. This is the local pre-tag gate for the full suites. The
   release workflow does not repeat it: its gate is `make test-acc-matrix`, every
   acceptance test family (`make test-acc`) on all four versions, and CI runs the full suites only on 2.16.0 and 2.17.0. Record the pass counts per
   version and package.
4. Run the native proofs from TESTING.md with Terraform **and** OpenTofu:
   - `tests/native/lifecycle.py` against a build stamped with the release version,
     in a mirror.
   - The upgrade proofs run from the **last released version's published archive**
     (checksums verified against that release's SHA256SUMS), never from a build of
     its source alone: `tests/native/upgrade.py`, `tests/native/client_upgrade.py`,
     `tests/native/upgrade_users_groups.py` (with the binary unpacked from the same
     archive) and `tests/native/application_config.py` with that version as
     `OLD_VERSION`. The new build must take over its state with an empty plan.
   - Run them on 2.17.0 and on every other supported version the release's changes
     depend on. State in TESTING.md what ran and what did not. Same-address
     releases need upgrade/refresh/empty-plan proof without state-provider
     replacement.
5. Review the focused diff and public metadata for credentials, state, private
   configuration and accidental local committer addresses. Document untested
   platforms.
6. Commit reviewed source. Check that the version number is not already an upstream
   tag (`git ls-remote --tags upstream`; upstream has a `release/v3` branch, so a
   3.x tag may appear there) so two different releases are not called the same
   thing. Create a stable version tag after validation.
7. Build with pinned GoReleaser 2.18.1 using `.goreleaser.yml`:
   `goreleaser release --clean --skip=publish`. Check all expected target ZIPs and
   SHA256SUMS. No GPG key or registry publication is implied.
8. Publish the tag/source and release archives, manifest and SHA256SUMS. Include
   the SHA256SUMS digest and exact validation evidence in release notes.
9. Download the published artifacts into a fresh directory, verify all checksums,
   install via a native mirror and repeat native Terraform/OpenTofu lifecycle
   checks. A tag or successful build alone is not a released-provider proof. The
   archive you verify here is the input to the next release's upgrade proofs.

The GoReleaser GitHub workflow is for an explicit manual release run from
`main-maintenance` (the branch the workflow accepts), accepting only an existing
stable tag on that branch. It validates the tagged source, runs `make check`,
`make vuln` and `make test-acc-matrix` (the `TestAcc` tests, not the full
`make test-acc-supported` run of step 3), and creates a draft. Add the checksum-manifest
digest from the run summary and the validation results to its notes before
explicitly publishing. Existing released tags must never be moved or rebuilt in
place. Routine CI runs on branch/PR changes. Automatic development releases,
scheduled sweeps, cleanup and contributor-edit jobs are not enabled in this
project. Do not publish a registry identity until it is actually registered with
the required signing setup.

## Release v3.0.0

A major release: the changelog's 3.0.0 section collects the deliberate breaking
changes (`allowed_user_groups` as a set, an immutable `client_id`, the Go module
path, data sources without password attributes, stricter plan-time validators).
Supported Pocket ID servers are 2.14.0 through 2.17.0, and the project is
maintained independently of upstream (UPSTREAM.md). The state-compatibility rule
covers state written by 2.4.103 and 2.4.104; the upgrade proofs run from the
published 2.4.104 archive on 2.17.0 with both tools, as in step 4, and
`application_config.py` needs a target build with write-only attributes and
Terraform or OpenTofu 1.11 or later. TESTING.md ("Which native proofs a release
runs") lists the 3.0.0 matrix: every `tests/native/*.py` script on 2.17.0 with
Terraform and OpenTofu, `lifecycle.py` also on 2.14.0, the old provider taken
from the verified 2.4.104 archive (its unpacked binary for
`upgrade_users_groups.py`). Publication, as for earlier releases, is
owner-run: push the branch and tag, run the release workflow for `v3.0.0`, verify
the draft's assets and the downloaded binary, then publish. Then follow INSTALL.md's
upgrade section for 3.0.0.

## Earlier releases

The notes below record how earlier releases were published, as they were then.

### Release v2.4.104

A same-address patch on 2.4.103 for Pocket ID 2.17.0: application-configuration
updates, revocation of the secret 2.17.0 creates with a client, and the new optional
`pocketid_client.backchannel_logout_url`. No schema version change; 2.4.103 state
plans empty. `git ls-remote --tags upstream` showed no v2.4.104 on 2026-10-02 (the
newest upstream tag was v2.5.0); check again before tagging. The release workflow's
`make test-acc-matrix` now runs four fixture versions (2.14.0 to 2.17.0). The upgrade
proof is `tests/native/upgrade.py` from the published 2.4.103 archive on 2.17.0 with
both tools; TESTING.md records the local run. Publication, as for earlier releases,
is owner-run: push the branch and tag, run the release workflow for `v2.4.104`,
verify the draft's assets and downloaded binary, then publish. Then follow
INSTALL.md's "Upgrade from fork 2.4.103 to 2.4.104".

### Release v2.4.103

An additive same-address release on 2.4.102: the new `pocketid_group_membership`
resource and an `email` lookup key on the `pocketid_user` data source. No schema
change to any existing resource or data source, so upgrading is a drop-in patch
with an empty plan for existing configurations. Before tagging, check the
number is not an upstream tag (`git ls-remote --tags upstream`).

### Release v2.4.102

v2.4.101 was tagged and pushed but never built or released; pushed tags are not
moved, so its content ships as 2.4.102.

A same-address patch on 2.4.1 carrying upstream #116 and #103's copy-first
application configuration; no schema change. Fork patch
releases on the 2.4 line are numbered from 2.4.101 because upstream has released
different 2.4.0 to 2.4.2. Before tagging a fork release, check that the number is
not an upstream tag (`git ls-remote --tags upstream`). The upgrade proof is
`tests/native/upgrade.py` from the published 2.4.1 archive. Publication runs
`dist/release-v2.4.102/publish.sh` on Cygnus with the login Keychain; it verifies
the draft's assets and runs the downloaded binary through the native lifecycle
before publishing. Then follow INSTALL.md's "Upgrade from fork 2.4.1 to 2.4.102".

### Releases v2.4.0 and v2.4.1

A same-address minor release: new optional attributes, one documented behavior
change, no schema version change and no state-provider replacement. The upgrade
proof for it is `tests/native/upgrade.py` from the published 2.3.2 archive, plus
`tests/native/application_config.py ... 2.4.1 2.3.2`. 2.4.1 is a patch on 2.4.0 from an
independent post-release review. Publication commands:

```sh
git push origin main-maintenance
git push origin v2.4.1
gh workflow run release.yml --repo irashack/terraform-provider-pocketid --ref main-maintenance -f tag=v2.4.1
```

Then verify the draft as described below for v2.3.2, substituting the version, and
follow INSTALL.md's "Upgrade from fork 2.3.2 or 2.4.0 to 2.4.1".

### Prepared patch v2.3.2

The candidate follows maintenance commit `327dafd` and adds the SMTP fix and
current/prior minor-series support policy. The configured release remote is
`origin` (`https://github.com/irashack/terraform-provider-pocketid.git`); this
checkout has no Forgejo remote. Repository authority/remotes were not changed.
Local commit/tag and packaging do not publish or install a provider.

After local validation and tag creation, the remaining publication commands are:

```sh
git push origin main-maintenance
git push origin v2.3.2
gh workflow run release.yml --repo irashack/terraform-provider-pocketid --ref main-maintenance -f tag=v2.3.2
```

Wait for the existing workflow to produce its draft. Verify its assets, add the
**published build's** SHA256SUMS digest and native validation evidence to the
notes, then explicitly publish the draft. Local and CI archive hashes can differ;
consumers must pin the published assets. Follow INSTALL.md for a same-address
2.3.1 → 2.3.2 upgrade; no state-provider replacement or development override.
Registry registration/signing remains separate and pending.
