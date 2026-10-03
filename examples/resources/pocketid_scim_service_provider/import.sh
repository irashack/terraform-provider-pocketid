# A SCIM service provider is imported with the ID of its OIDC client. For a
# configuration that uses `token`, the first refresh stores the bearer token
# Pocket-ID holds in the state (sensitive, but in plain text in the state file):
terraform import pocketid_scim_service_provider.example "my-client-id"

# For a configuration that uses `token_wo`, add the token_wo_version of that
# configuration, so the token is never stored in the state:
terraform import pocketid_scim_service_provider.write_only "my-client-id,token_wo_version=2026-10"
