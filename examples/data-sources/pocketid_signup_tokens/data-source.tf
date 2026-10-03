# Every signup token that is currently valid, including the ones created in
# the Pocket-ID interface. Each token's value is in the list and is a live
# secret, so it lands in the state: read only what you need.
data "pocketid_signup_tokens" "all" {}

output "open_signup_tokens" {
  value = [
    for t in data.pocketid_signup_tokens.all.tokens : t.id
    if t.usage_count < t.usage_limit
  ]
}
