# Look up an API by its resource identifier, for example one managed in
# another configuration.
data "pocketid_api" "inventory" {
  resource = "https://inventory.example.com"
}

# Or by its ID.
data "pocketid_api" "by_id" {
  id = "3fa2c1d4-0000-0000-0000-000000000000"
}

# Grant a client access with the API's read permissions.
resource "pocketid_api_client_access" "reporting" {
  api_id                     = data.pocketid_api.inventory.id
  client_id                  = "reporting"
  user_delegated_permissions = [for key, permission in data.pocketid_api.inventory.permissions : key if endswith(key, ".read")]
}
