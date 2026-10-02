- `pocketid_scim_service_provider` is removed from state when Pocket ID reports
  that the SCIM service provider (or its client) no longer exists, so the next
  plan creates it again. Before, a refresh failed with an error until the
  resource was removed from state by hand. Destroying a SCIM service provider
  that is already gone now succeeds. Any other failure, including a 404 that
  Pocket ID did not send, is still an error.
- A `token` on `pocketid_scim_service_provider` that was cleared or changed
  outside Terraform now shows as a change on the next plan. Before, the old
  value stayed in state and nothing was repaired. A configuration without a
  `token`, or with `token = ""`, still plans empty; note that the configuration
  is authoritative, so a token set in the Pocket ID interface on a provider
  whose configuration has none is shown as a change that removes it.
- New `token_wo` and `token_wo_version` on `pocketid_scim_service_provider`: a
  write-only variant of `token` that is sent to Pocket ID but never stored in
  the plan or the state, so it can come from an ephemeral value. The token is
  sent when the resource is created and whenever `token_wo_version` changes;
  every other update keeps the token Pocket ID already holds (Pocket ID would
  otherwise clear it). Because the state holds no token, a token changed or
  cleared outside Terraform is not detected: change `token_wo_version` to send
  it again. `token_wo` conflicts with `token` and needs `token_wo_version`.
  Write-only attributes need Terraform or OpenTofu 1.11 or later; `token`
  keeps working everywhere.
- New resource `pocketid_scim_sync`: runs a SCIM synchronization of one
  `pocketid_scim_service_provider` during the apply, like `pocketid_ldap_sync`
  does for LDAP. It is created once, and replaced (so it syncs again) when its
  `triggers` map or `service_provider_id` changes. Pocket ID runs the sync
  inside the request, so the apply waits for it and fails if the SCIM endpoint
  fails; the request is never repeated automatically. A sync that takes longer
  than the provider's `timeout` (30 seconds by default) fails the apply with a
  message that its outcome is unknown: raise `timeout`, and check
  `last_synced_at` on the service provider.
