resource "pocketid_api" "inventory" {
  name     = "Inventory"
  resource = "https://inventory.example.com"

  permissions = {
    "inventory.read"  = { name = "Read inventory" }
    "inventory.write" = { name = "Change inventory" }
    "inventory.sync"  = { name = "Synchronize inventory" }
  }
}

# A web application that calls the API on behalf of signed-in users.
resource "pocketid_client" "shop" {
  name          = "Shop"
  callback_urls = ["https://shop.example.com/callback"]
}

resource "pocketid_api_client_access" "shop" {
  api_id                     = pocketid_api.inventory.id
  client_id                  = pocketid_client.shop.id
  user_delegated_permissions = ["inventory.read", "inventory.write"]
}

# A confidential back-end job that calls the API as itself with the client
# credentials grant.
resource "pocketid_client" "sync_job" {
  name          = "Inventory sync"
  callback_urls = ["https://sync.example.com/callback"]
}

resource "pocketid_api_client_access" "sync_job" {
  api_id             = pocketid_api.inventory.id
  client_id          = pocketid_client.sync_job.id
  client_permissions = ["inventory.sync"]
}
