// Package client is the provider's HTTP client for Pocket ID's admin API.
//
// Layout: one file per API area. Each area file holds that area's request and
// response types next to the methods that use them (there is no shared models
// file), and its tests live in the matching _test.go file. Cross-cutting
// pieces have their own files:
//
//   - transport.go: the Client, the request loop and its limits.
//   - errors.go: typed API errors and the helpers that classify them.
//   - identifiers.go: the checks on IDs going into requests and coming back
//     in responses.
//   - pagination.go: paginated response types and listAll.
//   - version.go: the server version and version gates.
//
// A new API area goes in a new file; it does not need to edit an existing one.
//
// Identifiers going out. An ID that goes into a request path is validated
// and escaped first (uuidSegment, clientIDSegment). No identifier or
// parameter that contains the API key this client sends ever goes out: every
// request's path and query are checked, raw and decoded, before the request
// is built or logged (checkEndpoint), and ValidateIdentifier lets a caller
// refuse such an identifier from configuration, state or import before it
// logs or shows it.
//
// Identifiers coming back. An object ID in a response is checked before the
// method returns it, because callers keep it in state, log it and put it into
// later requests: a create response's new ID with checkCreatedID, and every
// other one (from reads, updates and lists, nested objects included) with
// checkReturnedID. Both refuse an ID that contains the API key first.
// checkReturnedID then requires the ID the call addressed, when there is
// one (the same UUID, in any case, for a UUID kind; byte for byte for an
// OIDC client), and otherwise a form the server itself can give the kind (a
// UUID; for an OIDC client, any ID Pocket ID's own client-ID rules accept,
// CIMD URLs included). The errors wrap ErrInvalidIdentifier and never
// include the value.
//
// Every area method applies the rule to every ID its response carries,
// nested objects included; returned_ids.go holds the walks several areas
// share (a user with its groups, a group with its members, a client with its
// groups and secrets). Comparisons of an ID held by a caller with one a
// response returned use SameUUID (or checkReturnedID with the held ID as
// addressed), never ==, and a later request for the object addresses the
// ID the server returned. One exception to "every ID": a SCIM service
// provider's oidcClient.id is checked only when present, because Pocket ID
// 2.14.0 to 2.17.0 fill that object only where they load the client with
// the provider, which none of the endpoints this client calls does.
package client
