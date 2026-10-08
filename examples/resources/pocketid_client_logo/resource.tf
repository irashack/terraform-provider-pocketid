resource "pocketid_client" "app" {
  name          = "My App"
  callback_urls = ["https://app.example.com/callback"]
}

# The logo Pocket ID shows for the client.
resource "pocketid_client_logo" "app" {
  client_id = pocketid_client.app.id
  source    = "${path.module}/logos/my-app.svg"
}

# Optional: a logo for dark mode. Without one, Pocket ID shows the light logo
# in dark mode too.
resource "pocketid_client_logo" "app_dark" {
  client_id = pocketid_client.app.id
  variant   = "dark"
  source    = "${path.module}/logos/my-app-dark.png"
}

# Pocket ID 2.18.0 or later: an icon of Pocket ID's icon library (the selfh.st
# icons by default) instead of a file. The dark logo is the icon's white
# variant, which only some icons have.
resource "pocketid_client" "media" {
  name          = "Jellyfin"
  callback_urls = ["https://jellyfin.example.com/sso/OID/redirect/pocketid"]
}

resource "pocketid_client_logo" "media" {
  client_id = pocketid_client.media.id
  preset    = "jellyfin"
}
