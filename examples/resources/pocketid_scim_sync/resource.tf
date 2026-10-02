# Push the users and groups an OIDC client may see to its SCIM endpoint.
# Pocket-ID also syncs on its own schedule and after changes; this resource
# runs one more sync during the apply, and fails the apply if the sync fails.
resource "pocketid_client" "example" {
  name          = "SCIM Enabled App"
  callback_urls = ["https://app.example.com/callback"]
}

resource "pocketid_scim_service_provider" "example" {
  client_id = pocketid_client.example.id
  endpoint  = "https://app.example.com/scim/v2"
  token     = var.scim_bearer_token
}

variable "scim_bearer_token" {
  description = "Bearer token used to authenticate against the SCIM endpoint"
  type        = string
  sensitive   = true
}

# Runs once, when the resource is created.
resource "pocketid_scim_sync" "example" {
  service_provider_id = pocketid_scim_service_provider.example.id
}

# Runs again whenever one of the trigger values changes, for example when a
# group's membership is managed in this configuration.
resource "pocketid_group" "staff" {
  name          = "staff"
  friendly_name = "Staff"
}

resource "pocketid_scim_sync" "after_group_changes" {
  service_provider_id = pocketid_scim_service_provider.example.id
  triggers = {
    group = pocketid_group.staff.friendly_name
  }
}
