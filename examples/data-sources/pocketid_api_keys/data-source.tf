# Warn before the API key this provider authenticates with expires.
#
# The data source lists the keys of the user who owns the provider's key. The
# provider cannot tell which of them it is using, so match it by name: here the
# key was created in the Pocket-ID interface under the name set below. The
# check passes only if such a key exists and stays valid for at least two more
# weeks. A failing check is a warning on every plan and apply, never an error.
# It uses plantimestamp(), which is known while planning, so the warning also
# appears on a plan that is never applied; timestamp() would only be known at
# apply time and the check would then say nothing on a plain `plan`.
#
# A key that has expired can no longer authenticate, so there is no list to read
# then: the warning has to come before that. Pocket-ID renews a key only after it
# has expired, and creating or renewing a key needs a signed-in session, which an
# API key cannot provide. So when the warning appears: in a signed-in session,
# create a replacement key under a new name, put it in the provider's api_token,
# change management_key_name below to the new name, confirm that the provider
# can still reach Pocket-ID, and only then revoke the old key.
locals {
  management_key_name = "terraform"
}

check "pocketid_management_key_expiry" {
  data "pocketid_api_keys" "mine" {}

  assert {
    condition = length([
      for key in data.pocketid_api_keys.mine.keys : key
      if key.name == local.management_key_name && timecmp(key.expires_at, timeadd(plantimestamp(), "336h")) > 0
    ]) > 0
    error_message = "The Pocket-ID API key named \"${local.management_key_name}\" is missing or expires within two weeks. In a signed-in session, create a replacement key, update the provider's api_token and management_key_name, confirm access, then revoke the old key."
  }
}

# The same data source outside a check, to see every key's times. A key's value
# is never returned.
data "pocketid_api_keys" "all" {}

output "api_key_expiry" {
  value = { for key in data.pocketid_api_keys.all.keys : key.name => key.expires_at }
}
