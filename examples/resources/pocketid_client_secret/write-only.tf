# Supply this value for each run; Terraform/OpenTofu 1.11+ keeps it out of state.
variable "app_client_secret" {
  description = "Client secret supplied only for this run"
  type        = string
  sensitive   = true
  ephemeral   = true
}

# Raise secret_wo_version to rotate the secret.
resource "pocketid_client_secret" "write_only" {
  client_id         = pocketid_client.app.id
  secret_wo         = var.app_client_secret
  secret_wo_version = "1"

  lifecycle {
    create_before_destroy = true
  }
}
