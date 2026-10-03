- **Breaking (Go importers only):** the Go module is now
  `github.com/irashack/terraform-provider-pocketid`. Terraform and OpenTofu
  configurations are unaffected; the registry address
  `registry.terraform.io/irashack/pocketid` is unchanged.
- The `pocketid_clients` and `pocketid_groups` data sources, and the
  `pocketid_group` data source's lookup, now see every client and group.
  Before, they saw only the first 20 the server returned.
- Removing `allowed_user_groups` from a `pocketid_client` now applies. Before,
  the update failed with HTTP 400 after the client itself had been updated.
- Cancelling a run (Ctrl-C, or Terraform stopping the provider) now stops the
  provider's requests at once. If a create is interrupted after Pocket ID
  created the object, the object's ID stays in state and nothing is cleaned up
  automatically; inspect it before applying again.
- A read, its retries included, ends after 30 seconds or the provider's
  `timeout` setting, whichever is longer, even if an attempt is still waiting
  for the server. Reads that fail with a rate limit or a server error are
  retried within that time, never waiting more than 10 seconds between
  attempts; if the server asks the provider to wait longer, the error is
  returned at once. Changes (create, update, delete) are sent once and are
  bounded by the `timeout` setting alone.
- **Breaking:** IDs of users, groups, client secrets and SCIM service
  providers must be UUIDs, and OIDC client IDs must follow Pocket ID's rule
  (2 to 128 letters, digits, `.`, `_` or `-`). Any other value, for example a
  mistyped import ID, is refused with an error before a request is sent.
  Clients created from a Client ID Metadata Document (whose ID is a URL)
  cannot be looked up by ID.
- Lists of users, groups and clients are read in creation order. If the pages
  do not add up because the list changed while it was being read (an object
  appears twice, or the count differs from Pocket ID's total), it is read once
  more and then reported as an error. Pocket ID offers no snapshot of a list,
  so a list read while objects are being deleted and created at the same
  moment can still miss one; read it again when nothing else is changing it.
- A one-time access token response that contains no token is now reported as
  an error saying a token may have been created, instead of storing an empty
  token.
- When Pocket ID accepts a change but its response cannot be read (too large,
  or the connection drops while it arrives), the error now says the change was
  made and only its result is unknown. The provider never sends such a change
  again by itself.
- Responses larger than 16 MiB (64 KiB for error responses) are refused
  without being read in full. Error messages and the provider's logs never
  include response content: not the body, not the server's reason phrase, and
  not the text of a malformed response (a fixed description is given instead).
- The provider opens a new connection for every request to Pocket ID instead
  of reusing one. This keeps anything a server sends outside a response out of
  the logs, and makes each attempt exactly one request; the cost is one TCP
  (and TLS) handshake per request, which is small next to a Terraform run.
