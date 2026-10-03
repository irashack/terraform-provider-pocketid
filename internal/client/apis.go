package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// API is a protected resource (an OAuth 2.0 resource server, RFC 8707) that
// Pocket ID issues access tokens for: GET /api/apis/{id} (api.apiResponseDto,
// unchanged from v2.14.0 to v2.17.0).
//
// Resource is the identifier clients send as the resource parameter and that
// becomes the token audience. Pocket ID accepts it only on creation
// (apiCreateDto; apiUpdateDto has no such field) and stores it without
// trailing slashes. Permission IDs stay the same while their key does: client
// grants refer to them.
type API struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Resource    string          `json:"resource"`
	CreatedAt   string          `json:"createdAt"`
	Permissions []APIPermission `json:"permissions"`
	// AllowCIMDClients opens the API to every client registered through a
	// Client ID Metadata Document, for user-delegated access with the
	// permissions marked AllowedForCIMDClients.
	AllowCIMDClients bool `json:"allowCimdClients"`
}

// APIPermission is one permission (scope) of an API.
type APIPermission struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	// Description is nil when the permission has none; an empty string is
	// kept as one.
	Description           *string `json:"description,omitempty"`
	AllowedForCIMDClients bool    `json:"allowedForCimdClients"`
}

// APICreateRequest is the body of POST /api/apis.
type APICreateRequest struct {
	Name     string `json:"name"`
	Resource string `json:"resource"`
}

// APIUpdateRequest is the body of PUT /api/apis/{id}. The resource identifier
// cannot be changed.
type APIUpdateRequest struct {
	Name string `json:"name"`
}

// APIPermissionInput is one permission in PUT /api/apis/{id}/permissions.
// Description is always sent: an omitted or null description clears it.
type APIPermissionInput struct {
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type apiPermissionsUpdateRequest struct {
	Permissions []APIPermissionInput `json:"permissions"`
}

type apiCIMDAccessRequest struct {
	Enabled       bool     `json:"enabled"`
	PermissionIDs []string `json:"permissionIds"`
}

// APIClientGrant is what one OIDC client may do with one API
// (api.apiClientGrantDto). User-delegated access lets the client request
// tokens for the API on behalf of a signed-in user; client access lets it
// request them for itself with the client credentials grant. Each permission
// list holds permission IDs of that API.
type APIClientGrant struct {
	UserDelegatedAccess        bool     `json:"userDelegatedAccess"`
	ClientAccess               bool     `json:"clientAccess"`
	UserDelegatedPermissionIDs []string `json:"userDelegatedPermissionIds"`
	ClientPermissionIDs        []string `json:"clientPermissionIds"`
}

// IsEmpty reports whether the grant gives nothing. Pocket ID stores no rows
// for such a grant: writing one is the same as removing the client's access.
func (g APIClientGrant) IsEmpty() bool {
	return !g.UserDelegatedAccess && !g.ClientAccess && len(g.UserDelegatedPermissionIDs) == 0 && len(g.ClientPermissionIDs) == 0
}

// ClientAPIGrant is one entry of GET /api/api-access/{clientId}/apis
// (api.clientApiGrantDto): an API the client may reach, the grants written for
// the client, and what it receives through the API's CIMD opt-in. The CIMD
// fields are not a grant to this client; they are managed on the API.
type ClientAPIGrant struct {
	API API `json:"api"`
	APIClientGrant
	CIMDGrantedAccess        bool     `json:"cimdGrantedAccess"`
	CIMDGrantedPermissionIDs []string `json:"cimdGrantedPermissionIds"`
}

// apiKind names an API in identifier errors, as ResourceAPI names it in
// Pocket ID's not-found error (apperror.NotFound("API")).
const apiKind = "API"

// CreateAPI creates an API. Only the name and resource identifier are
// accepted here; permissions and CIMD access are separate requests.
func (c *Client) CreateAPI(ctx context.Context, req *APICreateRequest) (*API, error) {
	body, err := c.doRequest(ctx, "POST", "/api/apis", req)
	if err != nil {
		return nil, err
	}
	result, err := decodeAPI(body)
	if err != nil {
		return nil, fmt.Errorf("API creation returned an unreadable response; the API may exist: inspect before recovery: %w", err)
	}
	if err := c.apiCheckCreatedID(apiKind, "", result.ID); err != nil {
		return nil, fmt.Errorf("API creation returned no usable ID, so no follow-up request uses it; the API may exist: inspect before recovery: %w", err)
	}
	return result, nil
}

// apiCheckCreatedID has the signature and rules of the foundation's
// checkCreatedID (added after this branch's base) and is replaced by it at
// integration: an ID the server chose must be a UUID, an ID the caller
// supplied must come back exactly, and no ID may contain the API key this
// client sends (Pocket ID accepts any static key of 16 or more characters,
// so a key can look like a UUID). The error never includes the value.
func (c *Client) apiCheckCreatedID(kind, requested, returned string) error {
	if c.apiToken != "" && strings.Contains(returned, c.apiToken) {
		return fmt.Errorf("%w: the %s ID in the create response contains the API key this provider sent", ErrInvalidIdentifier, kind)
	}
	if requested != "" {
		if returned != requested {
			return fmt.Errorf("%w: the %s ID in the create response is not the one requested", ErrInvalidIdentifier, kind)
		}
		return nil
	}
	if !uuidPattern.MatchString(returned) {
		return fmt.Errorf("%w: the %s ID in the create response is not a UUID", ErrInvalidIdentifier, kind)
	}
	return nil
}

// GetAPI reads an API with its permissions. A missing API is reported as
// IsNotFound(err, ResourceAPI).
func (c *Client) GetAPI(ctx context.Context, id string) (*API, error) {
	segment, err := uuidSegment(apiKind, id)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/apis/"+segment, nil)
	if err != nil {
		return nil, err
	}
	return decodeAPI(body)
}

// UpdateAPI renames an API and returns it as the server holds it afterwards.
func (c *Client) UpdateAPI(ctx context.Context, id string, req *APIUpdateRequest) (*API, error) {
	segment, err := uuidSegment(apiKind, id)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "PUT", "/api/apis/"+segment, req)
	if err != nil {
		return nil, err
	}
	return decodeAPI(body)
}

// DeleteAPI deletes an API. Pocket ID deletes its permissions and every
// client's grants on it with it.
func (c *Client) DeleteAPI(ctx context.Context, id string) error {
	segment, err := uuidSegment(apiKind, id)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", "/api/apis/"+segment, nil)
	return err
}

// ListAPIs returns every API, following all pages of GET /api/apis (see
// listAll).
func (c *Client) ListAPIs(ctx context.Context) ([]API, error) {
	return listAll(ctx, c, "APIs", "/api/apis", nil, func(api API) string { return api.ID })
}

// UpdateAPIPermissions replaces the full permission set of an API and returns
// the API as the server holds it afterwards. Pocket ID matches permissions by
// key (Service.UpdatePermissions): a kept key keeps its ID, its grants and its
// CIMD flag, with its name and description replaced; a removed key is deleted
// together with every client's grant of it; a new key gets a new ID. An empty
// or nil list is sent as [] and removes every permission.
func (c *Client) UpdateAPIPermissions(ctx context.Context, id string, permissions []APIPermissionInput) (*API, error) {
	segment, err := uuidSegment(apiKind, id)
	if err != nil {
		return nil, err
	}
	if permissions == nil {
		permissions = []APIPermissionInput{}
	}
	body, err := c.doRequest(ctx, "PUT", "/api/apis/"+segment+"/permissions", apiPermissionsUpdateRequest{Permissions: permissions})
	if err != nil {
		return nil, err
	}
	return decodeAPI(body)
}

// UpdateAPICIMDAccess sets whether clients registered through a Client ID
// Metadata Document may reach the API, and which of its permissions they may
// request, replacing both. Pocket ID ignores permission IDs that are not the
// API's own without an error (Service.SetCIMDAccess), so a caller compares the
// returned API with what it asked for. A nil list is sent as [].
func (c *Client) UpdateAPICIMDAccess(ctx context.Context, id string, enabled bool, permissionIDs []string) (*API, error) {
	segment, err := uuidSegment(apiKind, id)
	if err != nil {
		return nil, err
	}
	if permissionIDs == nil {
		permissionIDs = []string{}
	}
	body, err := c.doRequest(ctx, "PUT", "/api/apis/"+segment+"/cimd-access", apiCIMDAccessRequest{Enabled: enabled, PermissionIDs: permissionIDs})
	if err != nil {
		return nil, err
	}
	return decodeAPI(body)
}

// SetAPIClientAccess replaces one client's grant on one API, leaving its
// grants on other APIs and other clients' grants untouched, and returns the
// grant the server stored.
//
// The stored grant can differ from the request without an error
// (Service.SetAPIClientAccess): permission IDs that are not the API's own are
// dropped; a public client gets no client access and no client permissions;
// a granted permission turns on access for its subject type. A grant with
// nothing in it removes the client's access. Callers compare the result with
// what they asked for. Nil lists are sent as []. The PUT is never retried.
//
// A successful response must carry all four grant fields (see
// decodeAPIClientGrant). One that does not (null, {}, a partial object, the
// wrong types) is not a grant: it is reported as an error wrapping
// ErrResultUnread, because the write may have been applied.
func (c *Client) SetAPIClientAccess(ctx context.Context, apiID, clientID string, grant APIClientGrant) (*APIClientGrant, error) {
	path, err := apiClientPath(apiID, clientID)
	if err != nil {
		return nil, err
	}
	if grant.UserDelegatedPermissionIDs == nil {
		grant.UserDelegatedPermissionIDs = []string{}
	}
	if grant.ClientPermissionIDs == nil {
		grant.ClientPermissionIDs = []string{}
	}
	body, err := c.doRequest(ctx, "PUT", path, grant)
	if err != nil {
		return nil, err
	}
	applied, err := decodeAPIClientGrant(body)
	if err != nil {
		// The PUT was accepted: what it stored is unknown, not empty.
		return nil, fmt.Errorf("API access of client %s: %w: the response did not describe a grant", clientID, ErrResultUnread)
	}
	return &applied, nil
}

// RemoveAPIClientAccess removes every grant one client holds on one API. A
// missing API is reported as IsNotFound(err, ResourceAPI) and a missing client
// as IsNotFound(err, ResourceOIDCClient); either way no grant remains, because
// Pocket ID deletes grants together with their API or client. Removing a
// grant that does not exist succeeds.
func (c *Client) RemoveAPIClientAccess(ctx context.Context, apiID, clientID string) error {
	path, err := apiClientPath(apiID, clientID)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", path, nil)
	return err
}

// ListClientAPIGrants returns every API the client may reach, with its grants
// (GET /api/api-access/{clientId}/apis; one unpaginated list). A missing
// client is reported as IsNotFound(err, ResourceOIDCClient). An answer that is
// not a list, or whose entries lack the API or the grant fields, is an error
// wrapping ErrIncompleteGrantResponse, never an empty or missing grant.
func (c *Client) ListClientAPIGrants(ctx context.Context, clientID string) ([]ClientAPIGrant, error) {
	segment, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/api-access/"+segment+"/apis", nil)
	if err != nil {
		return nil, err
	}
	return decodeClientAPIGrants(body)
}

// FindClientAPIGrant returns the grant the client holds on the API, or nil
// when the client's list has no grant written for it (the list may still show
// the API when it reaches it only through CIMD access).
func (c *Client) FindClientAPIGrant(ctx context.Context, clientID, apiID string) (*ClientAPIGrant, error) {
	if err := ValidateUUID(apiKind, apiID); err != nil {
		return nil, err
	}
	grants, err := c.ListClientAPIGrants(ctx, clientID)
	if err != nil {
		return nil, err
	}
	for i := range grants {
		if grants[i].API.ID == apiID && !grants[i].IsEmpty() {
			return &grants[i], nil
		}
	}
	return nil, nil
}

func apiClientPath(apiID, clientID string) (string, error) {
	api, err := uuidSegment(apiKind, apiID)
	if err != nil {
		return "", err
	}
	client, err := clientIDSegment(clientID)
	if err != nil {
		return "", err
	}
	return "/api/apis/" + api + "/clients/" + client, nil
}

// ErrIncompleteGrantResponse marks a successful response that does not
// describe a grant completely: not the object or list expected, or without the
// fields that say what the client may do. It is a fixed message; the response
// is never quoted.
var ErrIncompleteGrantResponse = errors.New("the response did not describe the API grants completely")

// The fields every grant object carries (api.apiClientGrantDto has no
// omitempty), from v2.14.0 to v2.17.0.
const (
	grantFieldUserAccess = "userDelegatedAccess"
	grantFieldClientAcc  = "clientAccess"
	grantFieldUserIDs    = "userDelegatedPermissionIds"
	grantFieldClientIDs  = "clientPermissionIds"
)

// grantFromFields reads the four grant fields from a decoded JSON object. Each
// must be present: the access flags as booleans, the permission lists as
// arrays of strings. A list may be null, which Go's nil slice encodes as and
// which means no permissions (the server answers [] today). A field that is
// missing, or of another type, makes the object no grant, so that an empty or
// partial object can never read as "access revoked".
func grantFromFields(fields map[string]json.RawMessage) (APIClientGrant, bool) {
	flag := func(name string) (bool, bool) {
		raw, ok := fields[name]
		if !ok {
			return false, false
		}
		var value *bool
		if json.Unmarshal(raw, &value) != nil || value == nil {
			return false, false
		}
		return *value, true
	}
	list := func(name string) ([]string, bool) {
		raw, ok := fields[name]
		if !ok {
			return nil, false
		}
		var elements []*string
		if json.Unmarshal(raw, &elements) != nil {
			return nil, false
		}
		ids := make([]string, 0, len(elements))
		for _, element := range elements {
			if element == nil {
				return nil, false
			}
			ids = append(ids, *element)
		}
		return ids, true
	}
	var g APIClientGrant
	var ok [4]bool
	g.UserDelegatedAccess, ok[0] = flag(grantFieldUserAccess)
	g.ClientAccess, ok[1] = flag(grantFieldClientAcc)
	g.UserDelegatedPermissionIDs, ok[2] = list(grantFieldUserIDs)
	g.ClientPermissionIDs, ok[3] = list(grantFieldClientIDs)
	return g, ok[0] && ok[1] && ok[2] && ok[3]
}

// decodeAPIClientGrant decodes the answer to PUT /api/apis/{id}/clients/{id}
// (api.apiClientGrantDto). Anything that is not an object with all four grant
// fields is an error.
func decodeAPIClientGrant(body []byte) (APIClientGrant, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return APIClientGrant{}, ErrIncompleteGrantResponse
	}
	grant, ok := grantFromFields(fields)
	if !ok {
		return APIClientGrant{}, ErrIncompleteGrantResponse
	}
	return grant, nil
}

// decodeClientAPIGrants decodes the answer to GET /api/api-access/{clientId}/apis
// (a list of api.clientApiGrantDto, [] when there are none). Every entry must
// name its API and carry the four grant fields; one that does not fails the
// whole read, because it may be the entry that was looked for.
func decodeClientAPIGrants(body []byte) ([]ClientAPIGrant, error) {
	var entries []json.RawMessage
	if json.Unmarshal(body, &entries) != nil || entries == nil {
		return nil, ErrIncompleteGrantResponse
	}
	grants := make([]ClientAPIGrant, len(entries))
	for i, entry := range entries {
		var fields map[string]json.RawMessage
		if json.Unmarshal(entry, &fields) != nil {
			return nil, ErrIncompleteGrantResponse
		}
		grant, ok := grantFromFields(fields)
		if !ok {
			return nil, ErrIncompleteGrantResponse
		}
		var api API
		if raw, present := fields["api"]; !present || json.Unmarshal(raw, &api) != nil || api.ID == "" {
			return nil, ErrIncompleteGrantResponse
		}
		var cimd struct {
			Access        bool     `json:"cimdGrantedAccess"`
			PermissionIDs []string `json:"cimdGrantedPermissionIds"`
		}
		if json.Unmarshal(entry, &cimd) != nil {
			return nil, ErrIncompleteGrantResponse
		}
		grants[i] = ClientAPIGrant{API: api, APIClientGrant: grant, CIMDGrantedAccess: cimd.Access, CIMDGrantedPermissionIDs: cimd.PermissionIDs}
	}
	return grants, nil
}

func decodeAPI(body []byte) (*API, error) {
	var result API
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	return &result, nil
}

// Pocket ID's limits for APIs (api.apiCreateDto and apiPermissionInputDto,
// v2.14.0 to v2.17.0). Lengths count Unicode characters, as the server's
// validator does.
const (
	APINameMaxLength                  = 50
	APIResourceMaxLength              = 350
	APIPermissionKeyMaxLength         = 128
	APIPermissionNameMaxLength        = 50
	APIPermissionDescriptionMaxLength = 200
)

// APIResourceProblem applies Pocket ID's rules for an API's resource
// identifier and returns "" when value is acceptable, or what is wrong with
// it. The rules are those of dto.ValidateResourceURI (fosite's
// IsValidResourceIndicatorURI: an absolute URI with no fragment and no
// whitespace, and not a javascript: or data: URI) and the max=350 binding.
// A trailing slash is refused as well: Pocket ID removes trailing slashes
// before storing the identifier (Service.Create), so the stored value would
// differ from the configured one. The server also refuses its own issuer URL,
// which only it knows.
func APIResourceProblem(value string) string {
	switch {
	case value == "":
		return "must not be empty"
	case utf8.RuneCountInString(value) > APIResourceMaxLength:
		return fmt.Sprintf("must be at most %d characters long", APIResourceMaxLength)
	case strings.ContainsFunc(value, unicode.IsSpace):
		return "must not contain whitespace"
	case strings.Contains(value, "#"):
		return "must not contain a fragment (#)"
	case strings.HasSuffix(value, "/"):
		return "must not end with a slash: Pocket ID stores the identifier without trailing slashes"
	}
	u, err := url.Parse(value)
	if err != nil || !u.IsAbs() {
		return "must be an absolute URI with a scheme, such as https://api.example.com"
	}
	switch strings.ToLower(u.Scheme) {
	case "javascript", "data":
		return "must not be a javascript: or data: URI"
	}
	return ""
}

// apiReservedPermissionKeys are the scope and claim names Pocket ID keeps for
// itself (api.isPermissionKeyReserved), compared without regard to case.
var apiReservedPermissionKeys = []string{"openid", "profile", "email", "email_verified", "groups", "offline_access"}

// APIPermissionKeyProblem applies Pocket ID's rules for a permission key and
// returns "" when key is acceptable, or what is wrong with it: 1 to 128
// characters that are valid in an OAuth scope (RFC 6749 scope-token:
// printable ASCII except space, '"' and '\'), and not one of the scope or
// claim names Pocket ID reserves.
func APIPermissionKeyProblem(key string) string {
	if key == "" {
		return "must not be empty"
	}
	if len(key) > APIPermissionKeyMaxLength {
		return fmt.Sprintf("must be at most %d characters long", APIPermissionKeyMaxLength)
	}
	for i := 0; i < len(key); i++ {
		ch := key[i]
		if ch == 0x21 || (ch >= 0x23 && ch <= 0x5B) || (ch >= 0x5D && ch <= 0x7E) {
			continue
		}
		return `must contain only characters that are valid in an OAuth scope: printable ASCII other than space, '"' and '\'`
	}
	for _, reserved := range apiReservedPermissionKeys {
		if strings.EqualFold(key, reserved) {
			return fmt.Sprintf("%q is reserved by Pocket ID (reserved, in any case: %s)", key, strings.Join(apiReservedPermissionKeys, ", "))
		}
	}
	return ""
}
