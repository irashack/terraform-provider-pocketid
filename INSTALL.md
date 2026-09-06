# Install the maintenance fork

This release uses the source address **registry.terraform.io/irashack/pocketid**
in both Terraform and OpenTofu. Registry publication is pending. Installation
uses each tool's native filesystem mirror support; no registry account, private
server, cache substitution, or development override is required.

```hcl
terraform {
  required_providers {
    pocketid = {
      source  = "registry.terraform.io/irashack/pocketid"
      version = "2.3.1"
    }
  }
}
```

Download the exact version's archive and SHA256SUMS from
[release v2.3.1](https://github.com/irashack/terraform-provider-pocketid/releases/tag/v2.3.1).
Verify the SHA256SUMS file against the immutable digest recorded in the release
notes, then verify the selected archive against that file. Checksums detect
content changes; they are not a registry GPG signature. This release is unsigned.

For example, for `darwin_arm64` (use `linux_amd64` or `linux_arm64` as appropriate):

```sh
version=2.3.1
platform=darwin_arm64
archive=terraform-provider-pocketid_${version}_${platform}.zip
sums=terraform-provider-pocketid_${version}_SHA256SUMS
release=https://github.com/irashack/terraform-provider-pocketid/releases/download/v${version}
curl --fail --location --output "$archive" "$release/$archive"
curl --fail --location --output "$sums" "$release/$sums"
# Set this to the literal SHA256SUMS digest from the v2.3.1 release notes:
expected_manifest_sha256=REPLACE_WITH_RELEASE_DIGEST
printf '%s  %s\n' "$expected_manifest_sha256" "$sums" | shasum -a 256 -c -
awk -v file="$archive" '$2 == file { print }' "$sums" | shasum -a 256 -c -
mirror="$HOME/.local/share/pocketid-provider-mirror"
mkdir -p "$mirror/registry.terraform.io/irashack/pocketid"
cp "$archive" "$mirror/registry.terraform.io/irashack/pocketid/"
```

Use a CLI configuration file with an **absolute** mirror path:

```hcl
provider_installation {
  filesystem_mirror {
    path    = "/absolute/path/to/pocketid-provider-mirror"
    include = ["registry.terraform.io/irashack/pocketid"]
  }
  direct {
    exclude = ["registry.terraform.io/irashack/pocketid"]
  }
}
```

Set `TF_CLI_CONFIG_FILE` to that file for `terraform init` or `tofu init`.
Do not edit the user's global CLI configuration or overwrite an upstream cache.
Commit `.terraform.lock.hcl` after initialization. To prepare lock hashes for
multiple target platforms, download and verify each corresponding archive into
the packed mirror, then run (substitute `terraform` if desired):

```sh
tofu providers lock -fs-mirror="$mirror" \
  -platform=darwin_arm64 -platform=linux_amd64 -platform=linux_arm64
```

Keep the exact version and checksum pins; do not silently select a newer tag.

## Existing upstream-managed resources

Back up state with its configured backend, retain the decryption key separately,
and obtain exclusive access. For encrypted OpenTofu state, verify both the
original and backup remain encrypted. Never pipe decrypted state to a file.

Use the exact old source address reported by your state. OpenTofu commonly uses
`registry.opentofu.org/trozz/pocketid`; Terraform commonly uses
`registry.terraform.io/trozz/pocketid`. For example:

```sh
tofu state replace-provider -auto-approve \
  registry.opentofu.org/trozz/pocketid \
  registry.terraform.io/irashack/pocketid
```

The supported state command takes a lock and writes a backup. Change the source
and exact version in configuration, initialize using the mirror, and review a
fresh plan. **Require no replacement or secret rotation.** Inspect rather than
apply if any existing client would change unexpectedly. Do not import an already
managed resource into a second state. Imported secrets remain unavailable; do
not recreate a client to fill that field.

Rollback uses the inverse supported `state replace-provider` operation and the
prior source/version/lockfile under the same exclusive access and backup rules.
Do not restore an old whole-state backup after unrelated resources changed.
Upstream 2.3.0 can still refresh existing clients, but its secret-creation bug
returns on Pocket ID 2.14. After uncertain creation, inspect the client before
any recovery; a tainted resource may otherwise be replaced by the next apply.

Native installation references:
[Terraform CLI configuration](https://developer.hashicorp.com/terraform/cli/config/config-file#provider-installation),
[OpenTofu provider-address migration](https://opentofu.org/docs/cli/commands/state/replace-provider/),
[OpenTofu state and plan encryption](https://opentofu.org/docs/language/state/encryption/).
