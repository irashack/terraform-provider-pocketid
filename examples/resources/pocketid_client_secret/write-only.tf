# A secret whose value never enters plan or state (Terraform or OpenTofu
# 1.11+). The value comes from an ephemeral resource and goes to the consumer
# through another write-only argument. Raise the version to rotate: both
# version arguments change in the same apply, so both receive the same value.
locals {
  app_secret_version = 1
}

ephemeral "random_password" "app" {
  length  = 48
  special = false
}

resource "pocketid_client_secret" "app" {
  client_id         = pocketid_client.app.id
  secret_wo         = ephemeral.random_password.app.result
  secret_wo_version = tostring(local.app_secret_version)

  lifecycle {
    create_before_destroy = true
  }
}

resource "vault_kv_secret_v2" "app" {
  mount                = "kv"
  name                 = "apps/my-app/oidc"
  data_json_wo         = jsonencode({ client_secret = ephemeral.random_password.app.result })
  data_json_wo_version = local.app_secret_version
}
