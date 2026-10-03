data "pocketid_user" "owner" {
  username = "owner"
}

data "pocketid_user_passkeys" "owner" {
  user_id = data.pocketid_user.owner.id
}

# Fail the plan when the owner could be locked out by losing one device.
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
