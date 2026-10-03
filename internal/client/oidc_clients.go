package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// OIDCClient represents an OIDC client in Pocket-ID
type OIDCClient struct {
	ID                 string   `json:"id,omitempty"`
	Name               string   `json:"name"`
	HasLogo            bool     `json:"hasLogo,omitempty"`
	CallbackURLs       []string `json:"callbackURLs"`
	LogoutCallbackURLs []string `json:"logoutCallbackURLs,omitempty"`
	// BackchannelLogoutURL is reported from Pocket ID 2.17.0; empty when unset
	// or when the server predates it.
	BackchannelLogoutURL     string `json:"backchannelLogoutURL,omitempty"`
	IsPublic                 bool   `json:"isPublic"`
	RequiresReauthentication bool   `json:"requiresReauthentication,omitempty"`
	// Pointer so an absent field (Pocket-ID <= v2.8.0, which has no PAR support)
	// is distinguishable from an explicit false.
	RequiresPushedAuthorizationRequests *bool  `json:"requiresPushedAuthorizationRequests,omitempty"`
	LaunchURL                           string `json:"launchURL,omitempty"`
	PkceEnabled                         bool   `json:"pkceEnabled"`
	// PkceSupported is set by Pocket ID when the client sent PKCE although
	// it is not required; an update with pkceEnabled false resets it.
	PkceSupported          bool                  `json:"pkceSupported,omitempty"`
	IsGroupRestricted      bool                  `json:"isGroupRestricted"`
	Credentials            OIDCClientCredentials `json:"credentials"`
	AllowedUserGroups      []UserGroup           `json:"allowedUserGroups,omitempty"`
	AllowedUserGroupsCount int64                 `json:"allowedUserGroupsCount,omitempty"`
	// ClientType is "standard", or "cimd" for a client created from a Client
	// ID Metadata Document (Pocket ID 2.14.0+); empty on older servers.
	ClientType string `json:"clientType,omitempty"`

	// Settings the provider does not expose as attributes. They are read so
	// that an update can send them back unchanged; the update endpoint
	// replaces the client in full and resets anything omitted.
	Description                 string  `json:"description"`
	SkipConsent                 bool    `json:"skipConsent"`
	AccessTokenDurationMinutes  int64   `json:"accessTokenDurationMinutes,omitempty"`
	RefreshTokenDurationMinutes int64   `json:"refreshTokenDurationMinutes,omitempty"`
	HasDarkLogo                 bool    `json:"hasDarkLogo,omitempty"`
	LogoURL                     *string `json:"logoUrl,omitempty"`
	DarkLogoURL                 *string `json:"darkLogoUrl,omitempty"`

	// CreatedSecret is present only in a create response from Pocket ID
	// 2.17.0+, when the server generated a secret for a new confidential
	// client (autoCreateOidcClientSecret, enabled by default).
	CreatedSecret *CreatedClientSecret `json:"createdSecret,omitempty"`
}

// ClientTypeCIMD is the client_type of a client registered from a Client ID
// Metadata Document. Its registration settings are owned by that document.
const ClientTypeCIMD = "cimd"

// CreatedClientSecret identifies a secret the server generated while creating
// a client. Only the ID is decoded: the provider revokes that secret. The value
// is still present in the raw response bytes the client reads, but it is never
// decoded into a field, stored or logged. CreateClient empties an ID that is
// not a UUID or that contains the API key, so callers see an unusable ID as a
// missing one.
type CreatedClientSecret struct {
	ID string `json:"id"`
}

// OIDCClientCredentials represents federated identity credentials for an OIDC client
type OIDCClientCredentials struct {
	FederatedIdentities []OIDCClientFederatedIdentity `json:"federatedIdentities,omitempty"`
	// Secrets lists the client's secrets without their values (Pocket ID
	// 2.14.0+). It is read-only: the server ignores it in an update, and
	// requests built by this provider leave it empty.
	Secrets []ClientSecretMetadata `json:"secrets,omitempty"`
}

// OIDCClientFederatedIdentity represents a federated identity configuration
type OIDCClientFederatedIdentity struct {
	Issuer   string `json:"issuer"`
	Subject  string `json:"subject,omitempty"`
	Audience string `json:"audience,omitempty"`
	// JWKS is the URL of a JWKS; mutually exclusive with PublicKeys.
	JWKS string `json:"jwks,omitempty"`
	// PublicKeys are explicit public JWKs (Pocket ID 2.15.0+); mutually exclusive with JWKS.
	PublicKeys []json.RawMessage `json:"publicKeys,omitempty"`
	// ReplayProtection is always sent. The server replaces the whole identity
	// list on every client update, so an omitted field silently disables it.
	ReplayProtection bool `json:"replayProtection"`
}

// OIDCClientCreateRequest represents a request to create or update an OIDC client
type OIDCClientCreateRequest struct {
	Name               string   `json:"name"`
	ClientID           *string  `json:"id,omitempty"`
	CallbackURLs       []string `json:"callbackURLs"`
	LogoutCallbackURLs []string `json:"logoutCallbackURLs,omitempty"`
	// BackchannelLogoutURL (Pocket ID 2.17.0+) is omitted when nil, so a
	// server that predates it never receives the field. On 2.17 an omitted
	// value clears it, because the update replaces the client in full.
	BackchannelLogoutURL                *string               `json:"backchannelLogoutURL,omitempty"`
	IsPublic                            bool                  `json:"isPublic"`
	RequiresReauthentication            bool                  `json:"requiresReauthentication,omitempty"`
	RequiresPushedAuthorizationRequests bool                  `json:"requiresPushedAuthorizationRequests"`
	LaunchURL                           *string               `json:"launchURL,omitempty"`
	PkceEnabled                         bool                  `json:"pkceEnabled"`
	IsGroupRestricted                   bool                  `json:"isGroupRestricted"`
	Credentials                         OIDCClientCredentials `json:"credentials"`

	// Carried through from the current server state on update. See the
	// matching fields on OIDCClient.
	Description                 string  `json:"description"`
	SkipConsent                 bool    `json:"skipConsent"`
	AccessTokenDurationMinutes  int64   `json:"accessTokenDurationMinutes,omitempty"`
	RefreshTokenDurationMinutes int64   `json:"refreshTokenDurationMinutes,omitempty"`
	HasLogo                     bool    `json:"hasLogo,omitempty"`
	HasDarkLogo                 bool    `json:"hasDarkLogo,omitempty"`
	LogoURL                     *string `json:"logoUrl,omitempty"`
	DarkLogoURL                 *string `json:"darkLogoUrl,omitempty"`
}

// UpdateAllowedUserGroupsRequest represents a request to update allowed user groups for a client
type UpdateAllowedUserGroupsRequest struct {
	UserGroupIDs []string `json:"userGroupIds"`
}

// CreateClient creates a new OIDC client
func (c *Client) CreateClient(ctx context.Context, createReq *OIDCClientCreateRequest) (*OIDCClient, error) {
	if createReq.ClientID != nil {
		if err := c.checkRequestIDs(kindOIDCClient, *createReq.ClientID); err != nil {
			return nil, err
		}
	}
	body, err := c.doRequest(ctx, "POST", "/api/oidc/clients", createReq)
	if err != nil {
		return nil, err
	}

	var result OIDCClient
	if err := decodeResult(body, &result); err != nil {
		return nil, err
	}

	if result.ID == "" {
		return nil, fmt.Errorf("client creation returned no ID; inspect clients before recovery: %w", ErrResultUnread)
	}
	requested := ""
	if createReq.ClientID != nil {
		requested = *createReq.ClientID
	}
	if err := c.checkCreatedID("OIDC client", requested, result.ID); err != nil {
		return nil, fmt.Errorf("client creation returned an unusable client ID, so no follow-up request uses it; the client may exist: inspect clients before recovery: %w", unreadResult(err))
	}
	// A secret ID that is not a UUID cannot be addressed for revocation, and
	// one that carries the API key must not be logged or put in a URL, so
	// either is treated like one the response did not name.
	if result.CreatedSecret != nil && c.checkCreatedID("client secret", "", result.CreatedSecret.ID) != nil {
		result.CreatedSecret.ID = ""
	}
	// A new client has no allowed groups yet, and callers set them with a
	// separate request. Groups the response lists that fail the ID check are
	// dropped rather than failing a create whose own ID is usable.
	if c.checkGroupIDs(result.AllowedUserGroups) != nil {
		result.AllowedUserGroups = nil
	}
	// An answer whose text carries the API key is not used; the client
	// exists under its usable ID, which alone is returned so the caller can
	// keep it for recovery.
	if err := c.checkClientContent(&result); err != nil {
		return &OIDCClient{ID: result.ID}, fmt.Errorf("client %s was created, but its answer cannot be used; only its ID is kept: %w", result.ID, unreadResult(err))
	}
	return &result, nil
}

// GetClient retrieves an OIDC client by ID
func (c *Client) GetClient(ctx context.Context, clientID string) (*OIDCClient, error) {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/oidc/clients/"+id, nil)
	if err != nil {
		return nil, err
	}

	result, err := decodeClientResponse(body, decodeResponse)
	if err != nil {
		return nil, err
	}
	if err := c.checkOIDCClient(clientID, result); err != nil {
		return nil, err
	}

	return result, nil
}

// ErrIncompleteClient marks a client response without its allowedUserGroups
// field. Pocket ID 2.14.0 to 2.17.0 always send it with a single client
// (OidcClientWithAllowedUserGroupsDto has no omitempty; null or [] means
// none), so an answer without it says nothing about the client's groups and
// is never read as an empty set.
var ErrIncompleteClient = errors.New("the OIDC client response does not list the client's allowed user groups")

// decodeClientResponse decodes a single client (the answer to a GET or a PUT
// of /api/oidc/clients/{id}) with decode, and requires the allowedUserGroups
// field (see ErrIncompleteClient).
func decodeClientResponse(body []byte, decode func([]byte, any) error) (*OIDCClient, error) {
	var result OIDCClient
	if err := decode(body, &result); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := decode(body, &fields); err != nil {
		return nil, err
	}
	if _, present := fields["allowedUserGroups"]; !present {
		return nil, ErrIncompleteClient
	}
	return &result, nil
}

// UpdateClient updates an existing OIDC client
func (c *Client) UpdateClient(ctx context.Context, clientID string, updateReq *OIDCClientCreateRequest) (*OIDCClient, error) {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "PUT", "/api/oidc/clients/"+id, updateReq)
	if err != nil {
		return nil, err
	}

	result, err := decodeClientResponse(body, decodeResult)
	if errors.Is(err, ErrIncompleteClient) {
		return nil, unreadResult(err)
	}
	if err != nil {
		return nil, err
	}
	if err := c.checkOIDCClient(clientID, result); err != nil {
		return nil, unreadResult(err)
	}

	return result, nil
}

// DeleteClient deletes an OIDC client
func (c *Client) DeleteClient(ctx context.Context, clientID string) error {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", "/api/oidc/clients/"+id, nil)
	return err
}

// ListClients returns every OIDC client, following all pages of
// GET /api/oidc/clients (see listAll).
func (c *Client) ListClients(ctx context.Context) ([]OIDCClient, error) {
	clients, err := listAll(ctx, c, "OIDC clients", "/api/oidc/clients", nil, func(client OIDCClient) string { return client.ID })
	if err != nil {
		return nil, err
	}
	for i := range clients {
		if err := c.checkOIDCClient("", &clients[i]); err != nil {
			return nil, err
		}
	}
	return clients, nil
}

// UpdateClientAllowedUserGroups replaces the user groups allowed to use an
// OIDC client and returns the IDs of the groups the client has afterwards.
//
// Pocket ID keeps only the requested IDs that name an existing group and
// drops the rest without an error (OidcService.UpdateAllowedUserGroups looks
// the IDs up with "id IN ?"), so a caller compares the result with what it
// asked for. An empty or nil groupIDs is sent as [] (the server rejects
// null). The PUT's response does not include the groups, so they are read
// back with a GET of the client; if that read fails, or its response does not
// list the groups, the error wraps ErrResultUnread: the change was made, its
// result is unknown. The PUT is never retried.
func (c *Client) UpdateClientAllowedUserGroups(ctx context.Context, clientID string, groupIDs []string) ([]string, error) {
	if groupIDs == nil {
		groupIDs = []string{}
	}
	if err := c.checkRequestIDs("user group", groupIDs...); err != nil {
		return nil, err
	}
	req := UpdateAllowedUserGroupsRequest{UserGroupIDs: groupIDs}
	id, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	if _, err := c.doRequest(ctx, "PUT", "/api/oidc/clients/"+id+"/allowed-user-groups", req); err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/oidc/clients/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("allowed user groups of client %s: %w: %w", clientID, ErrResultUnread, err)
	}
	// OidcClientWithAllowedUserGroupsDto.allowedUserGroups has no
	// omitempty: null or [] means none. A response without the field says
	// nothing about the groups, so it never confirms an empty set.
	// An answer that is not the JSON expected wraps ErrUndecodableResponse
	// as well; one that is valid but leaves the field out does not.
	var fields map[string]json.RawMessage
	if err := decodeResult(body, &fields); err != nil {
		return nil, fmt.Errorf("allowed user groups of client %s: %w", clientID, err)
	}
	raw, present := fields["allowedUserGroups"]
	if !present {
		return nil, fmt.Errorf("allowed user groups of client %s: %w: the response did not list them", clientID, ErrResultUnread)
	}
	var groups []UserGroup
	if err := decodeResult(raw, &groups); err != nil {
		return nil, fmt.Errorf("allowed user groups of client %s: %w", clientID, err)
	}
	// The read-back must describe this client, and every group it lists must
	// pass the ID check, before its groups are taken as the result.
	var gotID string
	if rawID, ok := fields["id"]; ok && json.Unmarshal(rawID, &gotID) != nil {
		gotID = ""
	}
	if err := c.checkReturnedID(kindOIDCClient, clientID, gotID); err != nil {
		return nil, fmt.Errorf("allowed user groups of client %s: %w", clientID, unreadResult(err))
	}
	if err := c.checkGroupIDs(groups); err != nil {
		return nil, fmt.Errorf("allowed user groups of client %s: %w", clientID, unreadResult(err))
	}
	return userGroupIDs(groups), nil
}

// userGroupIDs returns the IDs of groups, never nil.
func userGroupIDs(groups []UserGroup) []string {
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}
