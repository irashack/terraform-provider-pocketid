# Configure SCIM provisioning for an OIDC client.
# Pocket-ID will provision users and groups to the external SCIM endpoint.
resource "pocketid_client" "example" {
  name          = "SCIM Enabled App"
  callback_urls = ["https://app.example.com/callback"]
}

resource "pocketid_scim_service_provider" "example" {
  client_id = pocketid_client.example.id
  endpoint  = "https://app.example.com/scim/v2"
  token     = var.scim_bearer_token
}

# The bearer token is sensitive. It is read back from the API on refresh, so a
# token that is cleared or changed outside Terraform shows as a change.
variable "scim_bearer_token" {
  description = "Bearer token used to authenticate against the SCIM endpoint"
  type        = string
  sensitive   = true
}

# Output the SCIM service provider ID (the token is sensitive)
output "scim_service_provider_id" {
  description = "The ID of the SCIM service provider configuration"
  value       = pocketid_scim_service_provider.example.id
}

# Terraform and OpenTofu 1.11 and later can keep the token out of the state
# entirely. token_wo is sent to Pocket-ID when the resource is created and
# whenever token_wo_version changes; any other update keeps the token Pocket-ID
# already holds. Change token_wo_version to rotate the token.
variable "scim_bearer_token_ephemeral" {
  description = "Bearer token for the SCIM endpoint, supplied only for this run"
  type        = string
  sensitive   = true
  ephemeral   = true
}

resource "pocketid_client" "write_only" {
  name          = "SCIM App With a Write-Only Token"
  callback_urls = ["https://other.example.com/callback"]
}

resource "pocketid_scim_service_provider" "write_only" {
  client_id        = pocketid_client.write_only.id
  endpoint         = "https://other.example.com/scim/v2"
  token_wo         = var.scim_bearer_token_ephemeral
  token_wo_version = "2026-10"
}
