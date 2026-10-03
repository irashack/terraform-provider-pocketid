# A token that lets one person register and join the "staff" group, valid for a
# week. Every input forces a new token: Pocket-ID cannot change one.
resource "pocketid_group" "staff" {
  name          = "staff"
  friendly_name = "Staff"
}

resource "pocketid_signup_token" "welcome" {
  ttl            = "168h" # 1 second to 744h (31 days); defaults to 1h
  usage_limit    = 1      # 1 to 100; defaults to 1
  user_group_ids = [pocketid_group.staff.id]
}

# The token value is a secret. Hand it to the person through a channel you
# trust, for example as part of the sign-up link.
output "signup_token" {
  value     = pocketid_signup_token.welcome.token
  sensitive = true
}

# Once the token has expired Pocket-ID deletes it. The resource stays in the
# state with expired = true and the next plan does not create a new token; to
# issue a fresh one, replace the resource:
#
#   terraform apply -replace=pocketid_signup_token.welcome
output "signup_token_expired" {
  value = pocketid_signup_token.welcome.expired
}
