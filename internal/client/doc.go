// Package client is the provider's HTTP client for Pocket ID's admin API.
//
// Layout: one file per API area. Each area file holds that area's request and
// response types next to the methods that use them (there is no shared models
// file), and its tests live in the matching _test.go file. Cross-cutting
// pieces have their own files:
//
//   - transport.go: the Client, the request loop and its limits.
//   - errors.go: typed API errors and the helpers that classify them.
//   - identifiers.go: the checks on IDs going into paths and coming back in
//     responses.
//   - pagination.go: paginated response types and listAll.
//   - version.go: the server version and version gates.
//
// A new API area goes in a new file; it does not need to edit an existing one.
//
// Identifiers. An ID that goes into a request path is validated and escaped
// first (uuidSegment, clientIDSegment). An object ID that comes back in a
// response is checked before the method returns it, because callers keep it
// in state, log it and put it into later requests: a create response's new ID
// with checkCreatedID, and every other one (from reads, updates and lists,
// nested objects included) with checkReturnedID. Both refuse an ID that
// contains the API key this client sends; checkReturnedID also requires the
// ID the call addressed, when there is one, and otherwise the kind's form (a
// UUID; for an OIDC client, a client ID or a CIMD client's URL). listAll
// applies it to every listed object; IDs nested inside objects are checked by
// the area method that returns them. The errors wrap ErrInvalidIdentifier and
// never include the value.
package client
