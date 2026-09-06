# Maintenance release procedure

1. Run TESTING.md's client matrix, meaningful Go checks, native lifecycle tests,
   and the migration rehearsal. Record exact versions and any failures.
2. Review the focused diff and public metadata for credentials, state, private
   configuration and accidental local committer addresses. Preserve LICENSE and
   upstream authors. Update README/CHANGELOG and document untested platforms.
3. Commit reviewed source. Create a stable version tag after validation.
4. Build with pinned GoReleaser 2.18.1 using `.goreleaser.yml`:
   `goreleaser release --clean --skip=publish`. Check all expected target ZIPs and
   SHA256SUMS. No GPG key or registry publication is implied.
5. Publish the tag/source and release archives, manifest and SHA256SUMS. Include
   the SHA256SUMS digest and exact validation evidence in release notes.
6. Download the published artifacts into a fresh directory, verify all checksums,
   install via a native mirror and repeat native Terraform/OpenTofu lifecycle
   checks. A tag or successful build alone is not a released-provider proof.

The existing GoReleaser GitHub workflow is retained for an explicit manual release
run from `main-maintenance`, accepting only an existing stable tag on that branch.
It validates the tagged source, runs the client matrix, and creates a draft. Add the
checksum-manifest digest from the run summary and native validation results to its
notes before explicitly publishing. Existing released tags must never be moved or
rebuilt in place. Routine CI runs on branch/PR changes. Upstream's automatic development
releases, scheduled sweeps, cleanup and contributor-edit jobs are not enabled in
this maintenance fork. Do not publish a registry identity until it is actually
registered with the required signing setup.
