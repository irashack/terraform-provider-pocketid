data "pocketid_user" "owner" {
  username = "owner"
}

# Warn when the owner could be locked out by losing one device. A check block
# reports a warning and lets the run continue; use a postcondition in the data
# source's lifecycle block to fail the plan instead.
data "pocketid_user_passkeys" "owner" {
  user_id = data.pocketid_user.owner.id
}

check "owner_has_two_passkeys" {
  assert {
    condition     = length(data.pocketid_user_passkeys.owner.passkeys) >= 2
    error_message = "The owner has fewer than two passkeys registered."
  }
}

# Passkeys that sync (for example through a platform keychain or a password
# manager) survive the loss of one device.
output "owner_synced_passkeys" {
  value = [
    for passkey in data.pocketid_user_passkeys.owner.passkeys : passkey.name
    if passkey.backup_state
  ]
}
