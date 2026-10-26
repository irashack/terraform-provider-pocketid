- 3.2.0 supports Pocket ID 2.19.0 and 2.19.1 (the tested range is now 2.14.0
  through 2.19.1). Pocket ID 2.19.0 replaced two boolean settings with modes.
- New attributes `email_login_notification_mode` (`disabled`, `always`,
  `ipAndUserAgent` or `browserRecognition`) and `email_verification`
  (`disabled`, `optional` or `required`) on `pocketid_application_config` and
  the `pocketid_application_config` data source. They need Pocket ID 2.19.0 or
  later; on an older server they are null and configuring one is refused at plan
  time. Pocket ID migrated `true` to `browserRecognition` and `optional`, and
  `false` to `disabled`.
- `email_verification = "required"` needs `require_user_email = "true"`,
  `emails_verified = "false"`, and `smtp_host` and `smtp_from` set (Pocket ID's
  own rule). The provider refuses it with a message naming what is missing, at
  plan time for configured values and before any change for the rest.
- `email_login_notification_enabled` and `email_verification_enabled` are
  deprecated. They keep working on Pocket ID before 2.19.0. From 2.19.0 on they
  are null in state and in the data source, and configuring one is refused at
  plan time with a message to use the new attribute. State written by 3.1.x
  upgrades without a state upgrade step: the first refresh against a 2.19 server
  sets the old attributes to null and the new ones to the server's values, so
  a configuration that replaced the old attributes with the new ones at those
  values plans empty. Replace the old attributes before, or together with,
  upgrading the server; until then a plan against a 2.19 server fails for any
  configuration that still sets one.
- The provider sends only the keys the server reports: never the removed
  boolean keys to Pocket ID 2.19.0 or later, and never the new keys to an older
  server.
