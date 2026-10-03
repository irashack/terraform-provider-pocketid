# Adopt a group that already has members: import it first, so that the plan
# shows which members the configuration would remove.
# terraform import pocketid_group_members.admins "3fa2c1d4-0000-4000-8000-000000000001"

import {
  to = pocketid_group_members.admins
  id = "3fa2c1d4-0000-4000-8000-000000000001"
}

resource "pocketid_group_members" "admins" {
  group_id = "3fa2c1d4-0000-4000-8000-000000000001"
  user_ids = ["7b9e0a12-0000-4000-8000-000000000001"]
}
