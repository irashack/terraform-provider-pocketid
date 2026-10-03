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
- New resource `pocketid_api_client_access` (Pocket ID 2.14.0 or later)
  grants one OIDC client access to one API: user-delegated access (tokens on
  behalf of a signed-in user) and client access (client credentials), each
  with a set of permission keys. It writes only its own (API, client) pair,
  so it never touches the client's other grants or other clients' grants.
  Before writing it checks that every permission key exists on the API and
  that a client given client access is not public, because Pocket ID drops
  both without an error; after writing it compares the stored grant with
  the configuration and fails, naming the difference, instead of recording
  access the server did not confirm. Leaving `user_delegated_access` or
  `client_access` unset makes it follow its permissions; setting it to
  `false` while listing permissions, or granting nothing, is refused at plan
  time. Import with `<api_id>/<client_id>`. Clients registered through a
  Client ID Metadata Document cannot be addressed; give them access on the
  API with `allow_cimd_clients`.
- New data sources `pocketid_api` (look up one API by `id` or by its exact
  `resource` identifier) and `pocketid_apis` (every API, oldest first, read
  across all pages), each with the API's permissions keyed by permission key
  and its CIMD access.
- Deleting an API in Pocket ID removes every client's access to it, including
  access granted without permissions; a `pocketid_api_client_access` for it
  disappears from state on the next refresh.
