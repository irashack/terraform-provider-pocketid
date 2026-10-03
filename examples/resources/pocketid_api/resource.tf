# An API (protected resource) that clients request access tokens for with
# resource = "https://inventory.example.com". The permission keys are the
# scopes a client may ask for.
resource "pocketid_api" "inventory" {
  name     = "Inventory"
  resource = "https://inventory.example.com"

  permissions = {
    "inventory.read" = {
      name        = "Read inventory"
      description = "List and view items"
    }
    "inventory.write" = {
      name = "Change inventory"
    }
  }
}

# Let every client registered through a Client ID Metadata Document (such as
# an MCP server) request user-delegated tokens with read access only.
resource "pocketid_api" "catalog" {
  name               = "Catalog"
  resource           = "https://catalog.example.com"
  allow_cimd_clients = true

  permissions = {
    "catalog.read" = {
      name                     = "Read the catalog"
      allowed_for_cimd_clients = true
    }
  }
}
