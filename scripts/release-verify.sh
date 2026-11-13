#!/usr/bin/env bash
# Download a release's assets (draft or published) into a fresh directory and
# check them: every file against SHA256SUMS, and SHA256SUMS.sig against
# release-signing-key.asc alone, in a throwaway keyring. Prints the SHA256SUMS
# digest the release notes and INSTALL.md record.
#
# Usage: scripts/release-verify.sh X.Y.Z      (needs gh with GH_TOKEN, and gpg)
set -euo pipefail
version=${1:?usage: release-verify.sh X.Y.Z}
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "'$version' is not X.Y.Z" >&2; exit 1; }
repo=irashack/terraform-provider-pocketid
key=$(cd "$(dirname "$0")/.." && pwd)/release-signing-key.asc
fingerprint=FE9D704B2C1E667546F0EBB1E031C4DE04D819D6
sums="terraform-provider-pocketid_${version}_SHA256SUMS"

dir=$(mktemp -d)
gh release download "v$version" -R "$repo" -D "$dir" >/dev/null
cd "$dir"
archives=$(ls ./*.zip | wc -l)
[ "$archives" -eq 10 ] || { echo "expected 10 archives, found $archives" >&2; exit 1; }
# Every line of the manifest must check out, and every archive must be on it.
sha256sum --check --strict --quiet "$sums"
for f in ./*.zip ./*_manifest.json; do
  grep -q " $(basename "$f")\$" "$sums" || { echo "$(basename "$f") is not in $sums" >&2; exit 1; }
done
GNUPGHOME=$(mktemp -d)
export GNUPGHOME
gpg --batch --quiet --import "$key"
gpg --batch --with-colons --list-keys | grep -q "^fpr:.*:${fingerprint}:" || { echo "release-signing-key.asc is not the expected key" >&2; exit 1; }
gpg --batch --quiet --verify "$sums.sig" "$sums" 2>/dev/null || { echo "the SHA256SUMS signature does not verify" >&2; exit 1; }
rm -rf "$GNUPGHOME"
echo "verified: $archives archives and the manifest match $sums; signature good ($fingerprint)"
echo "SHA256SUMS digest: $(sha256sum "$sums" | cut -d' ' -f1)"
echo "assets: $dir"
