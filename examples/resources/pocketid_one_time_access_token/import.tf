# One-time access tokens can be imported using the user ID
# terraform import pocketid_one_time_access_token.example "7b9e0a12-0000-4000-8000-000000000001"

# Note: The token value itself cannot be retrieved after creation,
# so it will not be available in the state after import.

# Example import block (Terraform 1.5+):
import {
  to = pocketid_one_time_access_token.example
  id = "7b9e0a12-0000-4000-8000-000000000001"
}
