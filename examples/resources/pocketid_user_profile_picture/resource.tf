data "pocketid_user" "alice" {
  username = "alice"
}

# Set Alice's profile picture from a file next to the configuration. When the
# file's content changes, the next apply uploads it again; destroying the
# resource restores the default picture.
resource "pocketid_user_profile_picture" "alice" {
  user_id = data.pocketid_user.alice.id
  source  = "${path.module}/pictures/alice.png"
}

output "alice_picture_digest" {
  value = pocketid_user_profile_picture.alice.sha256
}
