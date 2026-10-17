# The account the provider's API key belongs to. The provider acts with this
# account's admin rights: fail the plan when it stops being an administrator.
data "pocketid_current_user" "me" {
  lifecycle {
    postcondition {
      condition     = self.is_admin && !self.disabled
      error_message = "The API key's owner is no longer an enabled administrator."
    }
  }
}

output "provider_account" {
  value = data.pocketid_current_user.me.username
}
