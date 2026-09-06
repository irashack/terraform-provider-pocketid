# Pocket ID provider — maintenance fork

This is **irashack's independent maintenance fork** of
[Trozz/terraform-provider-pocketid](https://github.com/Trozz/terraform-provider-pocketid).
It preserves upstream history, resource schemas, import IDs, license, and contributor attribution.
It is not an official Pocket ID project or Trozz release. No support SLA is promised.

Version **2.3.1** starts from upstream **v2.3.0**
(`44c32e037fa0206d64a59e5eed9d8fbceecca36e`). It brings in Mathieu Lemay's
[PR #97](https://github.com/Trozz/terraform-provider-pocketid/pull/97), including
Yusaku Mizobuchi's test contribution, then validates malformed versions, prevents
mutation retries, protects partial creation, and removes secret-bearing HTTP bodies
from logs and diagnostics. [PR #90](https://github.com/Trozz/terraform-provider-pocketid/pull/90)
was reviewed for overlap; its custom user-ID/secret features are not included.

Use **`registry.terraform.io/irashack/pocketid`**, exact version **`2.3.1`**,
with the native filesystem mirror in [INSTALL.md](INSTALL.md).
**Registry publication is pending.** A GitHub release does not register a provider.
Never install this binary over `trozz/pocketid` or use `dev_overrides` for normal operation.

| Pocket ID | Secret API | Verification |
|---|---|---|
| 2.9.0 | POST `/api/oidc/clients/:id/secret` | Official-image client acceptance |
| 2.13.0 | POST `/api/oidc/clients/:id/secret` | Official-image client acceptance |
| 2.14.0 | POST `/api/oidc/clients/:id/secrets` | Official-image client acceptance |
| Earlier 2.x with missing version route | Singular endpoint only after Pocket ID's exact missing-route response | HTTP regression only; not live-tested |
| Other releases, including later 2.14+ | Semantic-version selection | Not live-tested; compatibility is not a blanket promise |

See [TESTING.md](TESTING.md) for exact tool/platform results and known failures,
[CHANGELOG.md](CHANGELOG.md) for this release, and [CONTRIBUTING.md](CONTRIBUTING.md)
for upstream contribution and return criteria. Original upstream guidance is retained
in [UPSTREAM-README.md](UPSTREAM-README.md); its registry/install claims describe upstream.
Resource documentation remains in [docs/](docs/).

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
