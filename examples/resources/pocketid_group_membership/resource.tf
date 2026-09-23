resource "pocketid_group" "family" {
  name          = "family"
  friendly_name = "Family"
}

# Look up an existing user, such as an owner account that is not itself
# managed by a pocketid_user resource, and add just that one user to the
# group without taking ownership of the group's other members.
data "pocketid_user" "owner" {
  email = "owner@example.com"
}

resource "pocketid_group_membership" "owner_family" {
  group_id = pocketid_group.family.id
  user_id  = data.pocketid_user.owner.id
}
