# Group memberships are imported as "<group_id>/<user_id>"; the resource's
# group_id and user_id must name the same group and user. Import block
# (Terraform 1.5+, OpenTofu):
import {
  to = pocketid_group_membership.owner_family
  id = "3fa2c1d4-0000-4000-8000-000000000001/7b9e0a12-0000-4000-8000-000000000001"
}
