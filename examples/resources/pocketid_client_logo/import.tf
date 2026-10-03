# Client logos are imported as "<client_id>/light" or "<client_id>/dark".
import {
  to = pocketid_client_logo.app_dark
  id = "my-app/dark"
}

resource "pocketid_client_logo" "app_dark" {
  client_id = "my-app"
  variant   = "dark"
  source    = "${path.module}/logos/my-app-dark.png"
}
