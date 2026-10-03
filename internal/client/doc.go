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
// On this branch the rule is applied to create responses, to every listed
// object's top-level ID (listAll, in its widest form only) and to every
// identifier going out. The read, update and nested-ID call sites live in
// area files that other work packages own; the integration checklist in
// this file (doc.go) lists each one.
package client

// Integration checklist: the checkReturnedID call sites still to adopt.
//
// Signature: c.checkReturnedID(kind, addressed, returned string) error, in
// identifiers.go. Kinds: "user", "user group", "client secret", "SCIM service
// provider", "signup token", "API", "API permission", "API key", "passkey",
// and kindOIDCClient ("OIDC client"), the only kind that is not a UUID.
//
// Addressed-ID semantics. addressed is the ID the request named in its path,
// or the parent the object must belong to; "" when it named none (a list
// item, an object found through its parent, a nested object). For a UUID kind
// the returned ID must be the same UUID, compared without regard to case
// (PostgreSQL answers an upper-case request with the lower-case ID); for an
// OIDC client it must be the same bytes. A method that sends a later request
// for the object uses the returned ID, not the caller's spelling. After a 2xx
// to a PUT the change was made, so a failed check is reported as an unread
// result: fmt.Errorf("%w: %w", ErrResultUnread, err). An object a response may
// legitimately omit is checked only when present; an empty ID is refused.
//
// listAll's own check is preliminary: it holds every listed ID to the widest
// form (any client ID, which includes every UUID) plus the key check. A list
// of UUID objects still checks each top-level ID as its kind, "" addressed,
// in the method that calls listAll or getPage, and every nested ID.
//
// oidc_clients.go (WP-A):
//   - GetClient: (kindOIDCClient, clientID, result.ID); each
//     result.AllowedUserGroups[i].ID as ("user group", "", id); on wave2/client
//     each result.Credentials.Secrets[i].ID as ("client secret", "", id).
//   - UpdateClient: the same checks, wrapped in ErrResultUnread.
//   - CreateClient: checkCreatedID already covers the client and its
//     createdSecret; check each AllowedUserGroups ID as above if present.
//   - ListClients: top-level IDs are client IDs (listAll's form is right);
//     the nested AllowedUserGroups (and Credentials.Secrets) loops as above.
//   - UpdateClientAllowedUserGroups: on wave2/client it decodes the GET
//     itself; decode "id" and check (kindOIDCClient, clientID, id), each group
//     ("user group", "", id), all wrapped in ErrResultUnread.
//
// oidc_client_secrets.go (WP-E):
//   - ListClientSecrets (checkClientSecretList on wave2/secrets-logos, made a
//     method): each ("client secret", "", secret.ID) in place of
//     ValidateUUID.
//   - decodeCreatedSecret (wave2/secrets-logos, made a method): both
//     ValidateUUID("client secret", ...) calls become
//     c.checkCreatedID("client secret", "", ...).
//
// users.go and groups.go (WP-B1):
//   - GetUser: ("user", userID, result.ID); each result.UserGroups[i].ID as
//     ("user group", "", id). UpdateUser: the same, wrapped in ErrResultUnread.
//   - CreateUser: keep the foundation's checkCreatedID when rebasing (the
//     branch went back to ValidateUUID); check each nested UserGroups ID.
//   - ListUsersPage and ListUsers (one page through getPage, which listAll does
//     not cover): each ("user", "", u.ID) and the nested group loop.
//   - ListAllUsers: each ("user", "", u.ID) and the nested group loop.
//   - UpdateUserGroups: decode "id" too and check ("user", userID, id); each
//     group ("user group", "", id); wrap in ErrResultUnread. On
//     wave2/users-groups decodeUserGroupIDs becomes a method: gotID != userID
//     becomes the ("user", userID, gotID) check, group.ID == "" the
//     ("user group", "", group.ID) check.
//   - GetUserGroup: ("user group", groupID, result.ID); each result.Users[i].ID
//     as ("user", "", id). UpdateUserGroup: the same, wrapped in
//     ErrResultUnread.
//   - ListUserGroups: each ("user group", "", g.ID).
//
// scim.go, signup tokens and API keys (WP-D1, signed off at 1c35d76: re-run
// its tests after these edits):
//   - CreateScimServiceProvider: after checkCreatedID, the parent:
//     parent := ""; if result.OidcClient != nil { parent = result.OidcClient.ID },
//     then (kindOIDCClient, req.OidcClientID, parent). An absent parent fails,
//     as it should: oidcClient is always present in 2.14 to 2.17
//     (scimsync/dto.go).
//   - GetClientScimServiceProvider: ("SCIM service provider", "", result.ID)
//     and the parent check against clientID.
//   - UpdateScimServiceProvider: ("SCIM service provider", id, result.ID) and
//     the parent check against req.OidcClientID, wrapped in ErrResultUnread.
//   - GetScimServiceProviderToken: *answer.ID != providerID becomes
//     ("SCIM service provider", providerID, *answer.ID). Its later PUT
//     addresses the provider by the caller's spelling, so either send that
//     PUT to the returned ID or keep the exact comparison there.
//   - CreateSignupToken: ValidateUUID("signup token", result.ID) becomes
//     c.checkCreatedID("signup token", "", result.ID); each nested UserGroups
//     ID ("user group", "", id), returning &result with an
//     ErrResultUnread-wrapped error.
//   - ListSignupTokens: each ("signup token", "", t.ID) and the nested
//     UserGroups loop. ListAPIKeys: each ("API key", "", k.ID).
//
// apis.go (WP-D2):
//   - Delete apiCheckReturnedID and call c.checkReturnedID with the same
//     arguments; keep apiCheckResponse's text-field key check. ListAPIs
//     already checks each API through apiCheckResponse.
//
// app_config.go (WP-C):
//   - GetApplicationConfig and UpdateApplicationConfig: signupDefaultUserGroupIDs
//     is a JSON array of group IDs in a string; parse it and check each as
//     ("user group", "", id) (wrapped in ErrResultUnread after the PUT).
//
// WP-B2 files (user-group-additions):
//   - GetCurrentUser: ("user", "", user.ID) and the nested group loop.
//   - GetUserGroupDetail: !strings.EqualFold(wire.ID, groupID) becomes
//     ("user group", groupID, wire.ID); each user ID ("user", "", id); each
//     allowedOidcClients ID (kindOIDCClient, "", id).
//   - GroupMemberIDs and AllowedClientIDsByGroup: the top-level IDs as their
//     kind ("user" or kindOIDCClient, "" addressed) and the nested group IDs
//     ("user group", "", id).
//   - SearchUserGroups: each ("user group", "", g.ID).
//   - SetGroupMembers: decode "id" and check ("user group", groupID, id); each
//     user ("user", "", id); wrap in ErrResultUnread.
//   - ListUserPasskeys: each ("passkey", "", item.ID).
//
// Resources and data sources call none of this directly (the helpers are
// unexported). Where a resource logs or shows an identifier from import,
// configuration or state before any request (an ImportState, a Read or plan
// diagnostic), it calls r.client.ValidateIdentifier(kind, id) first.
