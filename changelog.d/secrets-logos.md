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
  Answers from Pocket ID that do not fit its format (a secret list with an
  entry that has no usable ID, a prefix that is not the first four characters
  of the value) are refused with a fixed message rather than stored, printed
  or taken as proof that a secret is gone. This includes the secret list that
  `pocketid_client` reads when it revokes the secret Pocket ID 2.17 creates
  with a client: if revoking that secret cannot be confirmed because the list
  is unusable, the client's own secret is not generated beside it.
  Within one apply, `pocketid_client_secret` and `pocketid_client` take turns
  on a client's secrets, so an uncertain create never names the other
  resource's secret as its own; another apply or a change in Pocket ID's
  interface at the same moment is not covered.
- New resource `pocketid_client_logo`: the light or dark logo of an OIDC
  client (`variant = "light"` or `"dark"`), uploaded from a local file
  (`source`, with a computed `sha256`). The file is uploaded again when its
  content changes, when only its extension changes (Pocket ID takes the image
  type from it), and when the logo was removed or replaced outside Terraform.
  Logo reads bypass any cache in front of Pocket ID, so a cached copy neither
  hides a change nor reports a false one. Files Pocket ID would refuse (an
  unsupported extension, more than 2 MiB, a JPEG or PNG with more than 16
  million pixels) are refused at plan. Destroying the resource removes that
  logo. Import with `<client_id>/light` or `<client_id>/dark`; the first apply
  after an import uploads the file once.
