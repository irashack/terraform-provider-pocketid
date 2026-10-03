- `pocketid_client` has a new `generate_secret` attribute (default `true`).
  With `generate_secret = false` the resource holds no client secret of its
  own (`client_secret` is null), so a confidential client's secrets can be
  managed with `pocketid_client_secret`. Changing it in place works without
  replacing the client: `true` to `false` revokes the secret this resource
  generated, `false` to `true` generates a new one. If the secret in state
  cannot be told apart from the client's other secrets, the apply stops before
  changing anything and lists the client's secrets (IDs and prefixes, never
  values). A client imported, or created before this attribute existed,
  without a secret in state does not get one generated.
- `pocketid_client` has a new computed `client_secret_id`: the ID of the
  secret stored in `client_secret` (Pocket ID 2.14.0 and later). For existing
  state it is filled in on the next refresh when the secret can be identified
  by the four-character prefix Pocket ID keeps of it; this is not a planned
  change.
