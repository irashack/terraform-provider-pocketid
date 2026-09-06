# Changelog

## 2.3.2 — 2026-09-06

- Add `webauthn_user_verification`, `webauthn_allow_synced_passkeys`,
  `webauthn_authenticator_attachment`, and `cimd_url_allowlist` to the application
  configuration resource, data source, and API mappings.
- Preserve current server values when attributes are omitted or unknown; no
  security defaults are invented. Explicit values, including clearing the CIMD
  allowlist, remain intentional configuration changes.
- Fix HTTP 400 application-configuration updates on Pocket ID 2.13.0 and 2.14.0.
  Regression tests prove old-payload rejection and SMTP-only updates preserve
  every unrelated returned server setting, including secret values.
- Verify import, refresh, data-source values, attribute removal and no-change
  plans with native Terraform and OpenTofu; verify enforced OpenTofu state,
  backup and saved-plan encryption in disposable fixtures.
- Adopt current/prior minor-series support, pinned to tested Pocket ID 2.14.0 and
  2.13.0 for this release. Drop 2.9.0 from support, CI and disposable fixtures.
- Preserve the OIDC client-secret compatibility implementation.

Release preparation and publication commands: [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md).

### Maintenance cleanup included in 2.3.2

- Consolidate upstream dependency updates (#87, #98, #99) and selected Actions pins (#94).
- Rewrite installation and contributor guidance for the fork; correct MIT license
  references, reporting contacts and example provider addresses.
- Consolidate local checks and disposable test fixtures; expand CI and validate
  manually requested release tags before creating a draft.
- Record the upstream backlog, including group-order drift (#92), declarative
  secrets/IDs (#90), application-config failures and registry publication.

## 2.3.1 — 2026-09-06

Based on upstream v2.3.0. Backports PR #97 with original contributor attribution.

- Select the singular/plural client-secret endpoint using validated semantic versions.
- Reject invalid version responses; legacy fallback requires a verified missing route.
- Do not automatically retry mutations or follow redirects with an API key.
- Preserve known client identity after uncertainty or failed cleanup; report rollback
  honestly. Refuse fixed-ID creation when an existing object is found.
- Remove HTTP request/response bodies and arbitrary server errors from diagnostics/logs.
- Preserve resource schemas, import IDs and create-only secret behavior.
- Publish under the fork's own identity with native mirror installation. Registry
  publication is pending; checksums are provided and archives are unsigned.

See TESTING.md for official-image results and deferred application-configuration
acceptance failures, CONTRIBUTING.md for upstream reconciliation, and upstream's
[release history](https://github.com/Trozz/terraform-provider-pocketid/releases)
for changes through v2.3.0.
