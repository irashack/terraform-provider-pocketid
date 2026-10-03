# The account the provider's API key belongs to.
data "pocketid_current_user" "me" {}

# The provider acts with this account's admin rights: fail the plan when it
# stops being an administrator.
check "provider_account_is_admin" {
  assert {
    condition     = data.pocketid_current_user.me.is_admin && !data.pocketid_current_user.me.disabled
    error_message = "The API key's owner is no longer an enabled administrator."
  }
}

output "provider_account" {
  value = data.pocketid_current_user.me.username
}
