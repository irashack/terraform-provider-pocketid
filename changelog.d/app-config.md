- `pocketid_application_config` now sends back every setting Pocket ID reports,
  including settings this provider version does not know yet, unchanged. A
  Pocket ID release that adds a required setting no longer makes every update
  of the application configuration fail, as 2.17.0 did with
  `autoCreateOidcClientSecret`.
