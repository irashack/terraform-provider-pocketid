# Manage the global application configuration of a Pocket-ID instance.
#
# This is a singleton resource: only one should exist per instance. Any
# attribute left unset inherits the current server-side value. Removing the
# resource from your configuration leaves the live configuration untouched.
resource "pocketid_application_config" "this" {
  app_name         = "My Company SSO"
  session_duration = "60"
  accent_color     = "#3b82f6"

  allow_user_signups = "disabled"
  require_user_email = "true"
  disable_animations = "false"
  emails_verified    = "false"
}

# Example: configure SMTP for outgoing email
resource "pocketid_application_config" "with_smtp" {
  app_name = "My Company SSO"

  smtp_host             = "smtp.example.com"
  smtp_port             = "587"
  smtp_from             = "no-reply@example.com"
  smtp_user             = "smtp-user"
  smtp_tls              = "starttls"
  smtp_skip_cert_verify = "false"

  # The password stays out of plan and state (Terraform 1.11+ or OpenTofu
  # 1.11+). It is sent when this resource is created and whenever the
  # version changes; other updates keep the password Pocket ID holds.
  smtp_password_wo         = var.smtp_password # can be an ephemeral value
  smtp_password_wo_version = "1"

  email_login_notification_enabled = "true"
  email_verification_enabled       = "true"
}

# On older Terraform and OpenTofu versions, the plain attribute works too;
# the password is then stored in state (marked sensitive).
# smtp_password = var.smtp_password
