# Every API, oldest first.
data "pocketid_apis" "all" {}

output "api_resources" {
  value = [for api in data.pocketid_apis.all.apis : api.resource]
}

# APIs open to clients registered through a Client ID Metadata Document.
output "cimd_apis" {
  value = [for api in data.pocketid_apis.all.apis : api.name if api.allow_cimd_clients]
}
