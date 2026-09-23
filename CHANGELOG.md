# Changelog

## 2.4.103 — 2026-09-23

Adds a non-authoritative group membership resource, for a group whose members
are partly managed outside Terraform (for example, a self-service onboarding
broker). No schema change to any existing resource or data source; 2.4.102
state plans empty.

- New resource `pocketid_group_membership` manages a single `(group_id, user_id)`
  pair. Create adds the user to the group without touching any other member;
  Delete removes only that user; Read drops the resource from state if the pair
  no longer exists (the user was deleted, or the group membership was removed
  outside Terraform) instead of erroring. Both attributes require replacement.
  Import uses `<group_id>/<user_id>`.
- **API mechanism:** Pocket-ID exposes no endpoint to add or remove a single
  group member; the only mutating endpoint is `PUT /api/users/{id}/user-groups`,
  which replaces a user's entire group list. The new resource performs a
  read-modify-write against that endpoint (`Client.AddUserToGroup` /
  `RemoveUserFromGroup` / `UserHasGroupMembership` in `internal/client`): it
  reads the user's current groups, adds or removes the target group, and
  writes the full list back. A concurrent writer of the same user's groups
  between that read and write can have its change silently overwritten; there
  is no compare-and-swap primitive that would close this window. This is
  documented on the resource, not fixed, since Pocket-ID's API gives no way to
  fix it from the client side.
- **Does not combine with `pocketid_user.groups`:** that attribute is already
  authoritative over a user's full group list — including resetting it to
  empty when `groups` is left unset in configuration, which
  `TestAccResourceUser_withGroups`'s "Remove all groups" step already exercised.
  Reproduced live on Pocket ID 2.15.0: a `pocketid_user` resource that never
  sets `groups` plans to clear a membership added by
  `pocketid_group_membership` on the very next refresh. `pocketid_user`'s
  existing behavior is unchanged; this is a documented incompatibility between
  the two resources, not a bug fix. `pocketid_group` does not manage membership
  at all and is safe to use alongside the new resource.
- The `pocketid_user` data source gains an `email` lookup key alongside `id`
  and `username`, so a user can be looked up by email without a
  Terraform-managed `pocketid_user` resource for it — useful for adding an
  existing (e.g. owner) account to groups with `pocketid_group_membership`
  while never touching its `groups` attribute. `pocketid_users` (the list data
  source) is unchanged.

## 2.4.102 — 2026-09-23

2.4.101 was tagged but never built or released; 2.4.102 contains it plus the
application-configuration change below. Use 2.4.102.

Upstream #116, fixing upstream #106: a client update no longer resets settings the
provider does not manage. Also adopts upstream #103's shape for the application
configuration.

- `PUT /api/oidc/clients/:id` replaces a client in full, and the provider never sent
  `description`, `skipConsent`, `accessTokenDurationMinutes` or
  `refreshTokenDurationMinutes`. Every update of a `pocketid_client`, however
  unrelated, cleared the description, turned skip-consent off and returned both token
  lifetimes to their defaults, including values set in the Pocket ID admin UI. The
  provider now reads the client before updating it and sends these back unchanged,
  together with the logo fields. They are still not attributes.
- That read now always happens and also supplies the federated identity values it
  was already made for, so an update issues one read, not two. If the read fails,
  nothing is changed.
- The application-configuration update now starts from a copy of the server's current
  configuration and overlays the planned values, as upstream #103 does. Every modelled
  field was already preserved, so nothing changes today; a field added to the client
  model later is kept without also being listed in the merge. The fork's four WebAuthn
  and CIMD attributes stay.
- No schema or state change; 2.4.1 state plans empty.
- **Version number:** upstream Trozz has released its own, different 2.4.0 to 2.4.2.
  Fork patch releases on the 2.4 line are numbered from 2.4.101 so they cannot share
  a number with an upstream release.

## 2.4.1 — 2026-09-20

Fixes found by an independent review of 2.4.0, each reproduced against the released
binary first. If you use `federated_identities`, prefer this release to 2.4.0.

- Identities sharing issuer, subject and audience now each keep their own
  `replay_protection` when it is omitted. 2.4.0 matched by first hit, so an unrelated
  update gave every twin the first one's value and could silently disable protection.
  The nth occurrence of a key is now paired with the nth prior one, at plan and apply.
- A `replay_protection` that is configured but not known until apply, such as another
  resource's output, is left to the configuration. 2.4.0 planned a value over it and
  Terraform rejected the plan with "planned value does not match config value".
- A `null` element in `public_keys` is rejected at plan time. 2.4.0 dropped it while
  building the request, which failed with "element has vanished" after the server
  had been changed, and `[null]` alone slipped past the 2.15.0 version gate.
- `public_keys` also rejects, at plan time and without echoing the key, two keys with
  the same `kid`, a non-string `kid`, `kty` or `use`, and RSA, EC or OKP keys missing
  their public parameters. The server refused these only at apply.
- Acceptance tests now fail when `POCKETID_TEST_VERSION` is missing or malformed,
  instead of reading it as an older server and skipping the newer checks.
  `tests/native/upgrade.py` additionally covers protection enabled outside Terraform
  and an update applied with `-refresh=false` while state predates the attribute.
- No schema or behaviour change otherwise; 2.4.0 state plans empty.

## 2.4.0 — 2026-09-20

Pocket ID 2.15.0 support. Released 2.3.2 already works on 2.15.0; this release closes
two ways a client update could silently weaken a federated identity.

- Add `replay_protection` to `federated_identities`. Pocket ID replaces the whole
  identity list on every client update and the provider never sent the field, so
  every create and every update, however unrelated, disabled replay protection,
  including on identities an administrator had protected in the UI. An explicit
  value now wins. When omitted, an identity already managed keeps its current
  value, matched by issuer, subject and audience rather than list position.
- **Behaviour change:** a *new* identity with `replay_protection` omitted is created
  with it enabled, as the Pocket ID admin UI does. Earlier releases created it
  disabled. Set `replay_protection = false` for an issuer whose token is presented
  more than once. Existing identities are not changed by upgrading.
- Add `public_keys` to `federated_identities` for Pocket ID 2.15.0's explicit JWKs.
  Without it, a provider update deleted keys configured in the UI. Keys compare by
  JSON semantics because the server re-encodes them. Private or symmetric keys,
  a missing `kid`, a non-signature `use`, and `jwks` together with `public_keys` are
  rejected at plan time without echoing the key. On a server older than 2.15.0 the
  provider refuses before any mutation instead of letting the keys be dropped.
- Correct the `jwks` description: it is a JWKS URL.
- Support matrix is now Pocket ID 2.15.0 and 2.14.0; 2.13.0 leaves support, CI and
  the fixture allowlist. CI runs the full suite on both versions.
- Tests that selected 2.14+ assertions by exact version now compare versions, so they
  run on 2.15.0 and later. Add `tests/native/upgrade.py`, an upgrade proof from a
  released archive.
- Update gRPC to 1.83.2 for reachable advisory GO-2026-6443.
- No schema version change or state upgrader is needed; state from 2.3.2 plans empty.

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
