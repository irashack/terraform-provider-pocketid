resource "pocketid_client" "app" {
  name          = "My App"
  callback_urls = ["https://app.example.com/callback"]
}

# A secret Pocket ID generates; its value is in state as `secret`.
# Changing the trigger rotates it: with create_before_destroy the new secret
# is created first and the old one is revoked at the end of the apply.
# `terraform apply -replace=pocketid_client_secret.app` rotates it too.
resource "terraform_data" "app_secret_rotation" {
  input = "2026-10"
}

resource "pocketid_client_secret" "app" {
  client_id = pocketid_client.app.id

  lifecycle {
    create_before_destroy = true
    replace_triggered_by  = [terraform_data.app_secret_rotation]
  }
}

output "app_client_secret" {
  value     = pocketid_client_secret.app.secret
  sensitive = true
}
