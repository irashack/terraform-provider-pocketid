# A public browser application uses PKCE and has no client secret.
resource "pocketid_client" "spa" {
  name          = "Example SPA"
  callback_urls = ["https://spa.example.com/callback"]
  is_public     = true
  pkce_enabled  = true
}

# A confidential application with a stable client ID.
resource "pocketid_client" "web" {
  name                 = "Example Web Application"
  client_id            = "example-web"
  callback_urls        = ["https://app.example.com/callback"]
  logout_callback_urls = ["https://app.example.com/logout"]
  pkce_enabled         = true
}
