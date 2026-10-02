package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
	RequiresPushedAuthorizationRequests *bool                 `json:"requiresPushedAuthorizationRequests,omitempty"`
	LaunchURL                           string                `json:"launchURL,omitempty"`
	PkceEnabled                         bool                  `json:"pkceEnabled"`
	IsGroupRestricted                   bool                  `json:"isGroupRestricted"`
	Credentials                         OIDCClientCredentials `json:"credentials"`
	AllowedUserGroups                   []UserGroup           `json:"allowedUserGroups,omitempty"`
	AllowedUserGroupsCount              int64                 `json:"allowedUserGroupsCount,omitempty"`

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

// CreatedClientSecret identifies a secret the server generated while creating
// a client. Only the ID is decoded: the provider revokes that secret. The value
// is still present in the raw response bytes the client reads, but it is never
// decoded into a field, stored or logged.
type CreatedClientSecret struct {
	ID string `json:"id"`
}

// OIDCClientCredentials represents federated identity credentials for an OIDC client
type OIDCClientCredentials struct {
	FederatedIdentities []OIDCClientFederatedIdentity `json:"federatedIdentities,omitempty"`
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
	body, err := c.doRequest(ctx, "POST", "/api/oidc/clients", createReq)
	if err != nil {
		return nil, err
	}

	var result OIDCClient
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	if result.ID == "" {
		return nil, fmt.Errorf("client creation returned no ID; inspect clients before recovery")
	}
	return &result, nil
}

// GetClient retrieves an OIDC client by ID
func (c *Client) GetClient(ctx context.Context, clientID string) (*OIDCClient, error) {
	body, err := c.doRequest(ctx, "GET", fmt.Sprintf("/api/oidc/clients/%s", url.PathEscape(clientID)), nil)
	if err != nil {
		return nil, err
	}

	var result OIDCClient
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// UpdateClient updates an existing OIDC client
func (c *Client) UpdateClient(ctx context.Context, clientID string, updateReq *OIDCClientCreateRequest) (*OIDCClient, error) {
	body, err := c.doRequest(ctx, "PUT", fmt.Sprintf("/api/oidc/clients/%s", url.PathEscape(clientID)), updateReq)
	if err != nil {
		return nil, err
	}

	var result OIDCClient
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// DeleteClient deletes an OIDC client
func (c *Client) DeleteClient(ctx context.Context, clientID string) error {
	_, err := c.doRequest(ctx, "DELETE", fmt.Sprintf("/api/oidc/clients/%s", url.PathEscape(clientID)), nil)
	return err
}

// ListClients retrieves all OIDC clients
func (c *Client) ListClients(ctx context.Context) (*PaginatedResponse[OIDCClient], error) {
	body, err := c.doRequest(ctx, "GET", "/api/oidc/clients", nil)
	if err != nil {
		return nil, err
	}

	var result PaginatedResponse[OIDCClient]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// UpdateClientAllowedUserGroups updates the allowed user groups for an OIDC client
func (c *Client) UpdateClientAllowedUserGroups(ctx context.Context, clientID string, groupIDs []string) error {
	req := UpdateAllowedUserGroupsRequest{UserGroupIDs: groupIDs}
	_, err := c.doRequest(ctx, "PUT", fmt.Sprintf("/api/oidc/clients/%s/allowed-user-groups", url.PathEscape(clientID)), req)
	return err
}
