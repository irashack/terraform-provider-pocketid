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
