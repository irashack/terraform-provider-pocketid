# Client secrets are imported as "<client_id>/<secret_id>". The secret's ID
# is listed in Pocket ID (and by GET /api/oidc/clients/<client_id>/secrets).
import {
  to = pocketid_client_secret.app
  id = "my-app/3fa2c1d4-0000-4000-8000-000000000000"
}

# Pocket ID returns a secret's value only when it is created, so after an
# import `secret` is null.
resource "pocketid_client_secret" "app" {
  client_id = "my-app"
}
