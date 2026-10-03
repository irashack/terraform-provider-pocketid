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
- Importing a `pocketid_scim_service_provider` that your configuration manages
  with `token_wo`: import with `<client_id>,token_wo_version=<version>` (the
  version your configuration uses) so the refresh after the import never writes
  the token into the state. The ordinary import ID, `<client_id>`, is for
  configurations that use `token`; its first refresh stores the bearer token
  Pocket ID holds in the state, as before.
- New resource `pocketid_scim_sync`: runs a SCIM synchronization of one
  `pocketid_scim_service_provider` during the apply, like `pocketid_ldap_sync`
  does for LDAP. It is created once, and replaced (so it syncs again) when its
  `triggers` map or `service_provider_id` changes. Pocket ID runs the sync
  inside the request, so the apply waits for it and fails if the SCIM endpoint
  fails; the request is never repeated automatically. A sync that takes longer
  than the provider's `timeout` (30 seconds by default) fails the apply with a
  message that its outcome is unknown: raise `timeout`, and check
  `last_synced_at` on the service provider.
- New resource `pocketid_signup_token` and data source `pocketid_signup_tokens`
  for Pocket ID's signup tokens (people register with them; optionally into
  groups). A token's inputs (`ttl`, `usage_limit`, `user_group_ids`) cannot be
  changed, so changing one replaces the token. The token value is stored in the
  state as a sensitive value. Pocket ID deletes a token when it expires, and
  cannot tell that from an administrator deleting it: such a token stays in the
  state with `expired = true` and the next plan does not create a new one. Use
  `-replace` for a fresh token. Pocket ID silently ignores a group ID that
  names no group; the provider fails and records the token as tainted so the
  next apply replaces it. The data source lists the valid tokens with their
  ID, times, limits, use count and groups. Token values are deliberately not
  exposed by the list: Pocket ID's list includes each token's value, and
  copying every outstanding token, including ones created outside Terraform,
  into the state would let anyone who can read it register accounts. The value
  of a token Terraform created is the `token` attribute of
  `pocketid_signup_token`.
- New data source `pocketid_api_keys`: lists the API keys of the Pocket ID user
  who owns the key the provider uses (name, description, and creation, expiry
  and last-used times; never a key value). Its purpose is a `check` block that
  warns before the provider's own key expires (see the example). Creating,
  renewing and revoking keys is deliberately not offered. If the provider uses
  Pocket ID's static API key (`STATIC_API_KEY`), the list is empty.
