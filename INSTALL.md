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
      version = "2.3.2"
    }
  }
}
```

After publication (currently pending), download the exact version's archive and SHA256SUMS from
[release v2.3.2](https://github.com/irashack/terraform-provider-pocketid/releases/tag/v2.3.2).
Verify the SHA256SUMS file against the immutable digest recorded in the release
notes, then verify the selected archive against that file. Checksums detect
content changes; they are not a registry GPG signature. This release is unsigned.

For example, for `darwin_arm64` (use `linux_amd64` or `linux_arm64` as appropriate):

```sh
version=2.3.2
platform=darwin_arm64
archive=terraform-provider-pocketid_${version}_${platform}.zip
sums=terraform-provider-pocketid_${version}_SHA256SUMS
release=https://github.com/irashack/terraform-provider-pocketid/releases/download/v${version}
curl --fail --location --output "$archive" "$release/$archive"
curl --fail --location --output "$sums" "$release/$sums"
# Set this to the literal SHA256SUMS digest from the v2.3.2 release notes:
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

## Upgrade from fork 2.3.1

After v2.3.2 is published, verify and add its archive to the existing native mirror,
leaving v2.3.1 intact. Change only the exact version pin to `2.3.2`, keep the source
address unchanged, run `tofu init -upgrade` using the root's normal credential and
CLI configuration entry point, and commit the resulting lockfile. Review any other
provider selections before accepting the lockfile. No `state replace-provider`
is needed for this same-address patch upgrade. Require an empty baseline plan
before adding application-configuration ownership.

For SMTP adoption, import `application-configuration` into exactly one
`pocketid_application_config` resource with an initially empty body, refresh, and
require an empty plan. Then set only the SMTP attributes. Omitted fields inherit
the server's existing values, including WebAuthn policy and `cimd_url_allowlist`.
Read SMTP credentials from the existing secret authority, mark inputs sensitive,
and retain enforced encryption for state, backups and any saved plan. Neither
sensitive marking nor provider installation enables encryption by itself.

The API update replaces the full modeled configuration and is not atomic with
its preceding read. Avoid concurrent administrators/configuration writers.
Future server fields outside the tested matrix are not guaranteed to be preserved.
Rollback the provider version and lockfile to 2.3.1 if needed, but do not use that
version to update application configuration on 2.13/2.14: the original HTTP 400
returns. Do not restore a stale state backup over later changes.

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
