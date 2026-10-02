# Look up a group by ID (a UUID). The group is read directly, so this works
# however many groups exist.
data "pocketid_group" "by_id" {
  id = "3fa2c1d4-0000-4000-8000-000000000001"
}

# Look up a group by its exact name. Every group is considered, not just the
# first page of them.
data "pocketid_group" "developers" {
  name = "developers"
}

# Use the group data in other resources
resource "pocketid_user" "developer" {
  username   = "new.developer"
  email      = "new.developer@example.com"
  first_name = "New"
  last_name  = "Developer"

  groups = [data.pocketid_group.developers.id]
}

# Reference group information
output "developers_group_info" {
  value = {
    id            = data.pocketid_group.developers.id
    name          = data.pocketid_group.developers.name
    friendly_name = data.pocketid_group.developers.friendly_name
  }
}
