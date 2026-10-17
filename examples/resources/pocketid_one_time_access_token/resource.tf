# Create a one-time access token for a user, valid for 1 hour
resource "pocketid_one_time_access_token" "example" {
  user_id = pocketid_user.example.id
  ttl     = "1h"
}

# Example with user creation
resource "pocketid_user" "example" {
  username   = "temporary.user"
  email      = "temp@example.com"
  first_name = "Temporary"
  last_name  = "User"
}

# Output the token (marked as sensitive)
output "access_token" {
  description = "The one-time access token for the user"
  value       = pocketid_one_time_access_token.example.token
  sensitive   = true
}

# Use case: Create a token for emergency access, valid for 24 hours
resource "pocketid_user" "emergency" {
  username   = "emergency.access"
  email      = "emergency@example.com"
  first_name = "Emergency"
  last_name  = "Access"
  is_admin   = true
}

resource "pocketid_one_time_access_token" "emergency" {
  user_id = pocketid_user.emergency.id
  ttl     = "24h"
}

# Note: One-time access tokens are useful when users need to authenticate
# from a device where they don't have access to their passkey.
# The token can only be used once and should be treated as a secret. Its value
# is returned only on creation and cannot be read back from the API.

# Use case: Initial user setup, token valid for 7 days
resource "pocketid_user" "new_user" {
  username   = "new.employee"
  email      = "new.employee@example.com"
  first_name = "New"
  last_name  = "Employee"
}

resource "pocketid_one_time_access_token" "onboarding" {
  user_id = pocketid_user.new_user.id
  ttl     = "168h" # 7 days (max ttl is 744h / 31 days)
}
