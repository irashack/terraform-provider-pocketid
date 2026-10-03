# Every signup token that is currently valid, including the ones created in
# the Pocket-ID interface. The list has each token's ID, times, limits, use count
# and groups, but never a token's value: that is available only as `token` on a
# pocketid_signup_token that Terraform created.
data "pocketid_signup_tokens" "all" {}

output "open_signup_tokens" {
  value = [
    for t in data.pocketid_signup_tokens.all.tokens : t.id
    if t.usage_count < t.usage_limit
  ]
}
