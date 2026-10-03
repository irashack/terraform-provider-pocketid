- `pocketid_application_config` now sends back every setting Pocket ID reports,
  including settings this provider version does not know yet, unchanged. A
  Pocket ID release that adds a required setting no longer makes every update
  of the application configuration fail, as 2.17.0 did with
  `autoCreateOidcClientSecret`.
- `pocketid_application_config` checks every value at plan time with Pocket
  ID's own rules: `"true"`/`"false"` settings, the fixed choices (for example
  `allow_user_signups`, `smtp_tls`, `webauthn_user_verification`), the
  1-to-30-character `app_name`, a whole-number `session_duration`, the JSON
  formats of `signup_default_user_group_ids`, `signup_default_custom_claims` and
  `cimd_url_allowlist`, and a plain e-mail address in `smtp_from`. A value
  Pocket ID would refuse now fails the plan instead of the apply.
- **Breaking:** an empty string is refused at plan time for settings Pocket ID
  requires, and for settings whose empty value Pocket ID replaces with a
  non-empty default (`accent_color`, `signup_default_user_group_ids`,
  `signup_default_custom_claims`, `cimd_url_allowlist`, `ldap_user_search_filter`,
  `ldap_user_group_search_filter`, `ldap_attribute_user_display_name`,
  `ldap_attribute_group_member`). Such a configuration could never apply
  cleanly; set the default the message names, or omit the attribute.
- **Breaking:** `session_duration` must be at least 1 minute. Pocket ID accepts
  0 or a negative number, which ends every session at once and locks every
  user, administrators included, out of the web interface.
- The `signup_default_custom_claims` documentation now gives the format Pocket
  ID requires: a JSON array of `{"key": ..., "value": ...}` objects, not a JSON
  object. Every attribute's documentation names its accepted values and Pocket
  ID's default.
- New attribute `auto_create_oidc_client_secret` on `pocketid_application_config`
  and its data source (Pocket ID 2.17.0 and later). On an older server it is
  null, and configuring it is refused at plan time.
- An update of `pocketid_application_config` no longer shows every setting you
  did not configure as "(known after apply)": the plan shows them unchanged.
  Settings you did not configure are still sent back with the value Pocket ID
  holds at apply time, so a change made outside Terraform since the plan is
  kept. After an update, the provider checks that Pocket ID stored each value
  it changed, and fails naming the setting if it did not.
- New write-only inputs `smtp_password_wo` / `smtp_password_wo_version` and
  `ldap_bind_password_wo` / `ldap_bind_password_wo_version` on
  `pocketid_application_config` (Terraform 1.11+ or OpenTofu 1.11+). The
  password never enters plan or state and can come from an ephemeral value. It
  is sent when the resource is created and whenever the `_wo_version` string
  changes; every other update sends the password Pocket ID holds back
  unchanged. Moving from `smtp_password` to `smtp_password_wo` sends the new
  value in the same update and removes the password from state. Out-of-band
  changes to a password managed this way are not detected.
- `pocketid_application_config` no longer copies a password you did not
  configure into state: `smtp_password` and `ldap_bind_password` are null
  unless you set them (or state from an earlier version already holds them),
  also after `terraform import`. A configured password is still refreshed and
  shown as a change when it differs from the server's.
- **Breaking:** the `pocketid_application_config` data source no longer has
  `smtp_password` or `ldap_bind_password`. Remove references to them. After
  upgrading, `terraform show` cannot read a stored result of this data source
  until the next apply (or `apply -refresh-only`) rewrites it; plans and
  applies are unaffected.
