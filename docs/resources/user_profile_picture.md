---
page_title: "pocketid_user_profile_picture Resource - terraform-provider-pocketid"
subcategory: ""
description: |-
  Sets a Pocket-ID user's profile picture from a local image file. Destroying the resource restores the default picture.
  What Pocket ID does with the file. It decodes the image itself (the file name and media type do not matter), scales and crops it to a 300x300 PNG and stores that. It accepts PNG, JPEG, GIF, WebP and BMP, and from Pocket ID 2.15 refuses an image of more than 16 million pixels in total (about 4000x4000). The provider checks at plan time that the file exists, is a regular file of at most 10 MiB, and, for PNG, JPEG and GIF, is a readable image within the pixel limit; it applies the pixel limit on every supported version, 2.14 included. WebP and BMP files are passed to the server with only their size checked.
  Changes to the file. sha256 is the digest of the file's content: when the file changes, the next plan shows a new digest and applying uploads the file again. A change to source alone (the same content at another path) uploads nothing. Use ${path.module}/... for source; a relative path is resolved against Terraform's working directory.
  Drift. Pocket ID reports no hash of the stored picture and does not say whether a user has a custom picture. The provider therefore keeps, in stored_sha256, the digest of the picture the server serves for the user right after the upload, and compares the served picture with it on every refresh. A picture replaced or removed outside Terraform is detected that way, and the next apply uploads the file again. A false difference can appear after a Pocket ID upgrade that changes how pictures are scaled or encoded; the cost is one more upload. If the upload succeeded but the picture could not be read back, stored_sha256 is null, nothing can be compared, and the next plan shows an upload that records it. What cannot be detected: that the stored picture came from this file, as opposed to an identical image uploaded some other way, and any change while the user does not exist.
  Destroy. Destroying removes the picture only while the picture the server serves is still the one recorded in stored_sha256: the provider reads it again, past caches, immediately before it deletes. A refresh never replaces stored_sha256; only an upload does. If the served picture is different (replaced or removed outside Terraform, or encoded differently after a Pocket ID upgrade), or stored_sha256 is null, destroy stops with an error and changes nothing, because the default picture cannot be told apart from a replacement and the provider does not delete what it did not upload. To restore this configuration's picture, apply it again, which uploads the file and records it, and destroy afterwards. To stop managing the picture without touching it, run terraform state rm for the resource. Pocket ID has no conditional delete, so a picture uploaded by someone else between the provider's check and its delete request is removed all the same; that race cannot be closed from here.
  There is no import: the source file is not recoverable from the server. Do not manage the same user's picture with two of these resources.
---

# pocketid_user_profile_picture (Resource)

Sets a Pocket-ID user's profile picture from a local image file. Destroying the resource restores the default picture.

**What Pocket ID does with the file.** It decodes the image itself (the file name and media type do not matter), scales and crops it to a 300x300 PNG and stores that. It accepts PNG, JPEG, GIF, WebP and BMP, and from Pocket ID 2.15 refuses an image of more than 16 million pixels in total (about 4000x4000). The provider checks at plan time that the file exists, is a regular file of at most 10 MiB, and, for PNG, JPEG and GIF, is a readable image within the pixel limit; it applies the pixel limit on every supported version, 2.14 included. WebP and BMP files are passed to the server with only their size checked.

**Changes to the file.** `sha256` is the digest of the file's content: when the file changes, the next plan shows a new digest and applying uploads the file again. A change to `source` alone (the same content at another path) uploads nothing. Use `${path.module}/...` for `source`; a relative path is resolved against Terraform's working directory.

**Drift.** Pocket ID reports no hash of the stored picture and does not say whether a user has a custom picture. The provider therefore keeps, in `stored_sha256`, the digest of the picture the server *serves* for the user right after the upload, and compares the served picture with it on every refresh. A picture replaced or removed outside Terraform is detected that way, and the next apply uploads the file again. A false difference can appear after a Pocket ID upgrade that changes how pictures are scaled or encoded; the cost is one more upload. If the upload succeeded but the picture could not be read back, `stored_sha256` is null, nothing can be compared, and the next plan shows an upload that records it. What cannot be detected: that the stored picture came from *this* file, as opposed to an identical image uploaded some other way, and any change while the user does not exist.

**Destroy.** Destroying removes the picture only while the picture the server serves is still the one recorded in `stored_sha256`: the provider reads it again, past caches, immediately before it deletes. A refresh never replaces `stored_sha256`; only an upload does. If the served picture is different (replaced or removed outside Terraform, or encoded differently after a Pocket ID upgrade), or `stored_sha256` is null, destroy stops with an error and changes nothing, because the default picture cannot be told apart from a replacement and the provider does not delete what it did not upload. To restore this configuration's picture, apply it again, which uploads the file and records it, and destroy afterwards. To stop managing the picture without touching it, run `terraform state rm` for the resource. Pocket ID has no conditional delete, so a picture uploaded by someone else between the provider's check and its delete request is removed all the same; that race cannot be closed from here.

There is no import: the source file is not recoverable from the server. Do not manage the same user's picture with two of these resources.

## Example Usage

```terraform
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
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `source` (String) The path of the image file to upload: PNG, JPEG, GIF, WebP or BMP, at most 10 MiB and, from Pocket ID 2.15, at most 16 million pixels.
- `user_id` (String) The ID of the user (a UUID). Changing this forces a new resource.

### Read-Only

- `id` (String) The resource ID, the same as `user_id`.
- `sha256` (String) The SHA-256 digest of the file's content, in hexadecimal. A changed file changes it, which makes the next apply upload the file again. Unknown until apply when the file does not exist yet at plan time.
- `stored_sha256` (String) The SHA-256 digest of the picture Pocket ID served for the user right after the provider's last upload (the 300x300 PNG it made from the file). It detects a picture changed or removed outside Terraform and is what destroy checks before deleting; a refresh never changes it. Null when it could not be read after the upload.
