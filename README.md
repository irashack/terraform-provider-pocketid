# Pocket ID provider for Terraform and OpenTofu

Manage [Pocket ID](https://pocket-id.org/) OIDC clients, users, groups and access
settings as code. This is **irashack's maintenance fork** of
[Trozz/terraform-provider-pocketid](https://github.com/Trozz/terraform-provider-pocketid),
with compatibility fixes for Pocket ID 2.14 and safer client-creation failure handling.
It is independently maintained, not an official Pocket ID or Trozz release.

## Get started

The latest fork release is [2.3.1](https://github.com/irashack/terraform-provider-pocketid/releases/tag/v2.3.1).
**Registry publication is pending:** install its verified archive using the native
filesystem mirror in [INSTALL.md](INSTALL.md) before running `terraform init` or
`tofu init`. A GitHub release alone does not make the provider registry-installable.

```hcl
terraform {
  required_providers {
    pocketid = {
      source  = "registry.terraform.io/irashack/pocketid"
      version = "2.3.1"
    }
  }
}

# Supply POCKETID_BASE_URL and POCKETID_API_TOKEN through your environment.
provider "pocketid" {}

resource "pocketid_client" "example" {
  name          = "Example application"
  callback_urls = ["https://app.example.com/oauth/callback"]
}
```

Create an API key in your Pocket ID instance and supply it through your secret
manager or local environment. The base URL is the instance origin, such as
`https://id.example.com`, without `/api`. Keep API keys out of `.tf` files.
Client secrets are sensitive outputs stored in state; protect state and saved plans.

Already using `trozz/pocketid`? Follow the [state migration procedure](INSTALL.md#existing-upstream-managed-resources)
and require a plan with no replacements or secret rotation. Keep the fork's own
provider address; do not overwrite an upstream binary or use development overrides
for normal installations.

## What you can manage

| Capability | Documentation |
|---|---|
| OIDC clients, callbacks, PKCE and group access | [Client resource](docs/resources/client.md) |
| Users and group membership | [User resource](docs/resources/user.md), [group resource](docs/resources/group.md) |
| One-time access tokens | [Token resource](docs/resources/one_time_access_token.md) |
| SCIM service providers and LDAP synchronization | [SCIM](docs/resources/scim_service_provider.md), [LDAP](docs/resources/ldap_sync.md) |
| Instance configuration | [Application configuration](docs/resources/application_config.md) — known acceptance failures below |
| Look up existing objects | [Data sources](docs/data-sources/) |

See [examples](examples/README.md) for complete configurations and
[provider settings](docs/index.md) for authentication, TLS and timeouts.

## Compatibility and current limits

| Pocket ID | Secret API | Verification for release 2.3.1 |
|---|---|---|
| 2.9.0 | Singular `/secret` | Official-image client acceptance |
| 2.13.0 | Singular `/secret` | Official-image client acceptance |
| 2.14.0 | Plural `/secrets` | Official-image client acceptance and native Terraform/OpenTofu lifecycle |
| Earlier 2.x with missing version route | Singular endpoint after the exact missing-route response | HTTP regression only |
| Other releases | Semantic-version selection | Not live-tested |

[TESTING.md](TESTING.md) records tested tool versions and scope. Application-config
acceptance has two known HTTP 400 failures. Multiple `allowed_user_groups` can show
ordering drift; upstream's proposed list-to-set migration needs compatibility work.
User-chosen IDs and client secrets from upstream PR #90 are not included.
These limitations are tracked in the [upstream review](UPSTREAM.md).

## Development and maintenance

Use the Go version in `go.mod`, Python 3 and Docker for disposable acceptance tests.
The test fixture chooses a free loopback port, seeds a fresh database, and cleans up.
It does not use your running Pocket ID instance.

```sh
make check                  # formatting, vet, unit/race tests, build and lint
make test-acc               # client lifecycle on Pocket ID 2.14.0
make test-acc-matrix        # client lifecycle on all three supported test images
make test-acc-provider     # broader 2.14 suite, excluding known application-config failures
```

Pinned tooling commands and contributor guidance are in [CONTRIBUTING.md](CONTRIBUTING.md).
[CI](https://github.com/irashack/terraform-provider-pocketid/actions/workflows/ci.yml)
checks changes; [releases](RELEASE_CHECKLIST.md) are manually requested drafts.
Dependabot groups weekly dependency updates for review. There is no automatic
merge, development-release churn or contributor bot committing to your branch.

Report reproducible fork bugs through [Issues](https://github.com/irashack/terraform-provider-pocketid/issues).
Use [private security reporting](SECURITY.md) for vulnerabilities. This is a
volunteer project with no response-time guarantee.

## Attribution

[MIT licensed](LICENSE), preserving upstream's copyright and history. Release 2.3.1
starts at upstream v2.3.0 (`44c32e0`) and includes Mathieu Lemay's
[PR #97](https://github.com/Trozz/terraform-provider-pocketid/pull/97) and Yusaku
Mizobuchi's tests, followed by validation, retry and partial-creation fixes.
The [upstream review](UPSTREAM.md) records remaining work and return criteria.
[UPSTREAM-README.md](UPSTREAM-README.md) is an archived upstream README, not the fork's
installation guide. See [CHANGELOG.md](CHANGELOG.md) for released changes.

## Secret and failure behavior

Confidential-client creation generates one secret. Public clients generate none.
Refresh, import and metadata-only updates never call the secret endpoint. Import
cannot recover an existing create-only secret; the value remains null. Store
provider state securely; a sensitive attribute alone does not encrypt state.

Version errors fail before creating a confidential client. Only Pocket ID's JSON
404 `API endpoint not found` identifies a missing version route. Invalid/empty
versions, authentication failures, unexpected 404s and server/transport failures
are not classified as old servers. No non-GET mutation is automatically retried;
HTTP redirects are not followed with the API key.

A definite rejection can roll back only the client created in that operation.
Cleanup failure or an ambiguous secret response retains its ID in state and tells
you to inspect read-only. **Do not blindly apply a tainted resource:** Terraform
may propose replacement. Inspect the client and secret metadata, then explicitly
choose recovery. No second secret is generated automatically. A lost response to
server-generated-ID client creation may leave an object whose ID was never
received; inspect the client list before retrying. Existing-ID conflicts do not
trigger deletion. HTTP diagnostics expose status codes, not arbitrary server bodies.
