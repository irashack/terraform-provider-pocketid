# Warn before the API key this provider authenticates with expires.
#
# The data source lists the keys of the user who owns the provider's key. The
# provider cannot tell which of them it is using, so match it by name: here the
# key was created in the Pocket-ID interface under the name "terraform". The
# check passes only if such a key exists and stays valid for at least two more
# weeks. A failing check is a warning on every plan and apply, never an error.
# It uses plantimestamp(), which is known while planning, so the warning also
# appears on a plan that is never applied; timestamp() would only be known at
# apply time and the check would then say nothing on a plain `plan`.
#
# A key that has expired can no longer authenticate, so there is no list to read
# then: the warning has to come before that. Renew the key in Pocket-ID (it
# needs a signed-in session, which an API key cannot provide) and update the
# provider's api_token.
check "pocketid_management_key_expiry" {
  data "pocketid_api_keys" "mine" {}

  assert {
    condition = length([
      for key in data.pocketid_api_keys.mine.keys : key
      if key.name == "terraform" && timecmp(key.expires_at, timeadd(plantimestamp(), "336h")) > 0
    ]) > 0
    error_message = "The Pocket-ID API key named \"terraform\" is missing or expires within two weeks. Renew it before it stops working."
  }
}

# The same data source outside a check, to see every key's times. A key's value
# is never returned.
data "pocketid_api_keys" "all" {}

output "api_key_expiry" {
  value = { for key in data.pocketid_api_keys.all.keys : key.name => key.expires_at }
}
