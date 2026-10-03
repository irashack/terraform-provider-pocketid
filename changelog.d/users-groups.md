- A `pocketid_user` or `pocketid_group` deleted outside Terraform (for
  example in the admin web UI) is now removed from state on the next refresh
  and planned for creation again. Before, every plan and refresh failed with
  an error. Destroying one that is already gone now succeeds. Only Pocket
  ID's own "not found" answer for that user or group counts; any other 404
  (a wrong base URL, a proxy's error page) is still an error.
- When creating a `pocketid_user` or `pocketid_group` fails after Pocket ID
  has created the object (for example, setting its custom claims or groups
  fails, or the run is cancelled at that moment), the provider deletes the new
  object and says so only when the deletion is confirmed. If the deletion
  fails or cannot be confirmed, the object's ID is kept in state (marked for
  replacement) and the error says the cleanup failed; before, the provider
  reported "the user was deleted" without checking and lost track of the
  object.
