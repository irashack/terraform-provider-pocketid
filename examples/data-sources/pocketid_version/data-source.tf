data "pocketid_version" "server" {}

# Refuse to plan against a server older than the features the configuration
# uses (compare numbers, not text, so that 2.9 is older than 2.14).
locals {
  server_minor = tonumber(split(".", data.pocketid_version.server.version)[1])
}

check "pocket_id_is_recent_enough" {
  assert {
    condition     = local.server_minor >= 14
    error_message = "This configuration needs Pocket ID 2.14 or later."
  }
}

output "pocket_id_version" {
  value = data.pocketid_version.server.version
}
