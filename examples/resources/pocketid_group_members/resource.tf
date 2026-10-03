resource "pocketid_group" "admins" {
  name          = "admins"
  friendly_name = "Administrators"
}

data "pocketid_user" "alice" {
  username = "alice"
}

data "pocketid_user" "bob" {
  username = "bob"
}

# The group's members are exactly these two users. Anyone else who is added to
# the group outside Terraform shows up as a difference in the next plan and is
# removed by the next apply.
resource "pocketid_group_members" "admins" {
  group_id = pocketid_group.admins.id
  user_ids = [data.pocketid_user.alice.id, data.pocketid_user.bob.id]
}

# A group that must have no members.
resource "pocketid_group" "quarantine" {
  name          = "quarantine"
  friendly_name = "Quarantine"
}

resource "pocketid_group_members" "quarantine" {
  group_id = pocketid_group.quarantine.id
  user_ids = []
}
