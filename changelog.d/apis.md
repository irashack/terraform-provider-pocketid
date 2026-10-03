- New resource `pocketid_api` (Pocket ID 2.14.0 or later) manages an API: a
  protected resource clients request access tokens for with its `resource`
  identifier, its permissions (a map keyed by permission key, each with a
  stable `id`) and its CIMD access (`allow_cimd_clients` and each
  permission's `allowed_for_cimd_clients`, both off unless set). Editing a
  permission's name or description keeps every client's grant of it;
  removing or renaming a key deletes that permission and its grants.
  Changing `resource` replaces the API, and deleting or replacing an API
  removes every client's access to it. Values Pocket ID would refuse or
  store differently (a trailing slash, a reserved or malformed permission
  key, a name that is too long) are refused at plan time, and an API whose
  `resource` another API already holds is refused before anything is
  written; import that one instead.
