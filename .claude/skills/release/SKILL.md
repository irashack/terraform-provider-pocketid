---
name: release
description: Cut a release of the Pocket ID Terraform provider - the order of the gates, the commands for each mechanical step (checks, proofs, tag, public push, draft verification, publishing, registries, evidence) and the mistakes that cannot be undone. Use when asked to release or tag this provider, or to check whether a commit is ready to be tagged.
---

# Releasing the Pocket ID provider

RELEASE_CHECKLIST.md is the procedure and stays authoritative; this is the short path through
it with the commands. Read the checklist's step when a command here surprises you.

Three things cannot be undone, so they come last and only after everything before them passed:
pushing a ref to the public repository, publishing the draft, and a tag once released (never
moved, never rebuilt). A failed gate after tagging but before publishing is fixed on the branch
and the release workflow run again with the same tag only if the tagged source is still right;
otherwise the version number is spent and the next one is used (3.2.0 was).

The private remote is the source of truth. Commit on `main-maintenance` as the maintainer
identity the history uses, never a desk or local address: everything there becomes public.

## 1. Prepare (a pull request on the private remote)

- Merge every `changelog.d/*.md` into a new `CHANGELOG.md` section and delete the fragments.
- Version and supported-range strings: `grep -rn "<last version>" --include='*.md' --include='*.yml' --include='*.tmpl' .`
  (README, INSTALL with a new "Upgrade from" section and the `RECORDED_IN_...` digest
  placeholder, docs/index.md and templates/index.md.tmpl together, .goreleaser.yml, the bug template).
- Checks, all on the commit to be tagged:

  ```
  make check && make docs-check && make actionlint && make vuln
  go vet -tags=acc ./... && go mod tidy -diff && make release-check
  ```

  `make docs-check` compares with the index: commit first. `make release-check` refuses any
  GoReleaser but the pinned one. Run `make vuln` again right before tagging: the vulnerability
  database changes under you (it did an hour after 3.2.2).
- `git archive HEAD | tar -t | grep -c '^docs/.*\.md$'` must equal `find docs -name '*.md' | wc -l`.
- The version must not be an upstream tag: `git ls-remote --tags https://github.com/Trozz/terraform-provider-pocketid`.

## 2. Gates on the merged commit (private remote's CI)

- The push run of `acceptance.yml` on `main-maintenance`: full suites on every supported version.
- `release-proofs.yml`, started by hand with `version` (to release) and `old_version` (last
  published): lifecycle, upgrade and application-configuration proofs with Terraform and OpenTofu.
  A manual run posts no commit status; find it by commit and read its log.
- Both must be green on the exact commit you tag. A failure there prints only a summary:
  reproduce it with `scripts/local-pocketid.py` (full output, no container) before changing anything.

## 3. Tag and publish

```
git tag -a vX.Y.Z -m vX.Y.Z <commit>          # annotated, maintainer identity
git push <private remote> vX.Y.Z
scripts/push-public.py --dry-run main-maintenance vX.Y.Z   # then without --dry-run
```

`push-public.py` is the only way a ref goes to GitHub; it refuses everything but
`main-maintenance` at the private remote's commit and a release tag that is already there.
Wait for the public CI on that commit, then start the Release workflow on `main-maintenance`
with input `tag=vX.Y.Z` (about 40 minutes: it repeats the checks and the acceptance matrix,
builds, signs and leaves a draft).

```
scripts/release-verify.sh X.Y.Z               # draft: archives, manifest, signature; prints the digest
```

Add a "## Verification" section to the draft's notes (the SHA256SUMS digest, the release run,
what the gates in 2 covered: copy the shape of the last release's notes), then publish it.
Do not open pull requests on the public repository.

## 4. After publishing

```
scripts/release-verify.sh X.Y.Z               # again, now the published files
scripts/release-registries.py X.Y.Z           # Terraform within minutes; OpenTofu an hour or more
```

- Native lifecycle check on a published archive, from a filesystem mirror, with both tools
  (`tests/native/lifecycle.py TOOL MIRROR X.Y.Z` under either fixture).
- A pull request recording "Release X.Y.Z evidence" in TESTING.md (what ran, on which commit,
  what did not run and why) and the real digest in INSTALL.md.
- Tell whoever installs a pinned commit that the release is there: the tag, the commit, the
  digest, and whether a plan should be empty.
