# Group memberships are imported using "<group_id>/<user_id>"
# terraform import pocketid_group_membership.owner_family "3fa2c1d4-.../7b9e0a12-..."

# Example import block (Terraform 1.5+):
import {
  to = pocketid_group_membership.owner_family
  id = "3fa2c1d4-0000-0000-0000-000000000000/7b9e0a12-0000-0000-0000-000000000000"
}

# The resource block that the import will populate:
resource "pocketid_group_membership" "owner_family" {
  group_id = "3fa2c1d4-0000-0000-0000-000000000000"
  user_id  = "7b9e0a12-0000-0000-0000-000000000000"
}
