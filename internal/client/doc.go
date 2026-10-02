// Package client is the provider's HTTP client for Pocket ID's admin API.
//
// Layout: one file per API area. Each area file holds that area's request and
// response types next to the methods that use them (there is no shared models
// file), and its tests live in the matching _test.go file. Cross-cutting
// pieces have their own files:
//
//   - transport.go: the Client, the request loop and its limits.
//   - errors.go: typed API errors and the helpers that classify them.
//   - pagination.go: paginated response types.
//   - version.go: the server version and version gates.
//
// A new API area goes in a new file; it does not need to edit an existing one.
package client
