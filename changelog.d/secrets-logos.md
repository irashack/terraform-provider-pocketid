- New resource `pocketid_client_secret` (Pocket ID 2.14.0 or later): one
  secret of an OIDC client, so a secret can be rotated without replacing the
  client. Pocket ID generates the value (stored in state as the sensitive
  `secret`), or you supply it through the write-only `secret_wo` with
  `secret_wo_version` (Terraform or OpenTofu 1.11 or later), in which case
  nothing secret is stored in state. Optional `expires_at`. Rotate by
  replacing the resource with `create_before_destroy = true`: the new secret
  is created before the old one is revoked. The resource never touches the
  client's other secrets, refuses clearly when a client already holds Pocket
  ID's maximum of 20 secrets, and lists the client's secrets (IDs and
  prefixes) if a create fails with an uncertain outcome. Import with
  `<client_id>/<secret_id>`; the value of an imported secret is unknown.
