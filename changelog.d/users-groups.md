- A `pocketid_user` or `pocketid_group` deleted outside Terraform (for
  example in the admin web UI) is now removed from state on the next refresh
  and planned for creation again. Before, every plan and refresh failed with
  an error. Destroying one that is already gone now succeeds. Only Pocket
  ID's own "not found" answer for that user or group counts; any other 404
  (a wrong base URL, a proxy's error page) is still an error.
