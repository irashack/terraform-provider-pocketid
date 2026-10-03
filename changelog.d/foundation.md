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
  attempts (at most four attempts, each a single request on a fresh
  connection); if the server asks the provider to wait longer, the error is
  returned at once. Changes (create, update, delete) are sent once and are
  bounded by the `timeout` setting alone.
- **Breaking:** IDs of users, groups, client secrets and SCIM service
  providers must be UUIDs, and OIDC client IDs must follow Pocket ID's rule
  (2 to 128 letters, digits, `.`, `_` or `-`). Any other value, for example a
  mistyped import ID, is refused with an error before a request is sent.
  Clients created from a Client ID Metadata Document (whose ID is a URL)
  cannot be looked up by ID. The ID Pocket ID returns when it creates an
  object must be a UUID, or exactly the `client_id` you set; any other value
  is reported as an error (the object may exist) and never used or logged.
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
- The ID of an object Pocket ID creates, and the ID of every object in a list
  the provider reads, is checked before the provider uses it: it must have a
  form Pocket ID itself can give an object, and it must never contain the
  API key. A list that holds an ID no Pocket ID object can have fails as a
  whole; clients whose IDs Pocket ID allows but this provider cannot address
  (such as metadata-document clients) are still listed. Such a response is
  reported as an error that does not include the value.
<!-- Integrator: this bullet covers only what the foundation branch applies
(create responses, listed objects' top-level IDs, identifiers going out).
Once the area methods adopt checkReturnedID per the checklist in
internal/client/doc.go, extend it: an ID in a read or update response must
be the object the request named (the same UUID in any letter case; a client
ID exactly), and nested IDs (a user's groups, a client's allowed groups, a
SCIM provider's client, signup default groups) are checked too. -->
- Setting up a connection to Pocket ID now ends with the request: when a run
  is cancelled or the provider's `timeout` passes, a connection still waiting
  for the server's TLS handshake is closed at once. Connecting is also limited
  to 30 seconds and the TLS handshake to 10 seconds whatever the `timeout`.
- An ID from your configuration, state or an import that contains the
  provider's API key (for example an import ID pasted from the wrong field) is
  refused with an error before any request is made, and is never written to
  a URL, the provider's logs or the error. The same applies to search terms
  and other request parameters.
