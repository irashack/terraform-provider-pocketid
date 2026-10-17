# Client secrets are imported as "<client_id>/<secret_id>". The secret's ID
# is listed in Pocket ID (and by GET /api/oidc/clients/<client_id>/secrets).
# The resource's client_id must be the same client ("my-app" here), or the plan
# replaces the imported secret.
import {
  to = pocketid_client_secret.app
  id = "my-app/3fa2c1d4-0000-4000-8000-000000000000"
}
