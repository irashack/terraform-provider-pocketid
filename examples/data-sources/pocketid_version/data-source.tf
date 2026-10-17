# Refuse to plan against a server older than the features the configuration
# uses (compare numbers, not text, so that 2.9 is older than 2.14).
data "pocketid_version" "server" {
  lifecycle {
    postcondition {
      condition = (
        tonumber(split(".", self.version)[0]) > 2 ||
        (tonumber(split(".", self.version)[0]) == 2 && tonumber(split(".", self.version)[1]) >= 14)
      )
      error_message = "This configuration needs Pocket ID 2.14 or later."
    }
  }
}

output "pocket_id_version" {
  value = data.pocketid_version.server.version
}
