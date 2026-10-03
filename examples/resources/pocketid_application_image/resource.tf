# One resource per application image. The file is uploaded again whenever its
# content changes, or when the image is changed outside Terraform.
resource "pocketid_application_image" "logo_light" {
  kind   = "logo_light"
  source = "${path.module}/branding/logo-light.svg"
}

resource "pocketid_application_image" "logo_dark" {
  kind   = "logo_dark"
  source = "${path.module}/branding/logo-dark.svg"
}

# The favicon (SVG, PNG or ICO) and the e-mail logo (PNG or JPEG) cannot be
# removed through Pocket ID's API: destroying these resources leaves the
# uploaded images in place.
resource "pocketid_application_image" "favicon" {
  kind   = "favicon"
  source = "${path.module}/branding/favicon.ico"
}

resource "pocketid_application_image" "email_logo" {
  kind   = "email_logo"
  source = "${path.module}/branding/email-logo.png"
}
