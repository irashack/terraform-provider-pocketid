package client

import (
	"encoding/json"
	"fmt"
)

// ScimServiceProvider represents a SCIM service provider configuration attached
// to an OIDC client in Pocket-ID. The token is stored encrypted server-side but
// is returned (decrypted) on read.
type ScimServiceProvider struct {
	ID           string              `json:"id,omitempty"`
	Endpoint     string              `json:"endpoint"`
	Token        string              `json:"token,omitempty"`
	LastSyncedAt *string             `json:"lastSyncedAt,omitempty"`
	OidcClient   *OIDCClientMetadata `json:"oidcClient,omitempty"`
	CreatedAt    string              `json:"createdAt,omitempty"`
}

// OIDCClientMetadata represents the OIDC client metadata embedded in a SCIM
// service provider response.
type OIDCClientMetadata struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// ScimServiceProviderCreateRequest represents a request to create or update a
// SCIM service provider configuration for an OIDC client.
type ScimServiceProviderCreateRequest struct {
	Endpoint     string `json:"endpoint"`
	Token        string `json:"token,omitempty"`
	OidcClientID string `json:"oidcClientId"`
}

// CreateScimServiceProvider creates a new SCIM service provider configuration.
func (c *Client) CreateScimServiceProvider(req *ScimServiceProviderCreateRequest) (*ScimServiceProvider, error) {
	body, err := c.doRequest("POST", "/api/scim/service-provider", req)
	if err != nil {
		return nil, err
	}

	var result ScimServiceProvider
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// GetClientScimServiceProvider retrieves the SCIM service provider configuration
// for an OIDC client. The token is returned decrypted.
func (c *Client) GetClientScimServiceProvider(clientID string) (*ScimServiceProvider, error) {
	body, err := c.doRequest("GET", fmt.Sprintf("/api/oidc/clients/%s/scim-service-provider", clientID), nil)
	if err != nil {
		return nil, err
	}

	var result ScimServiceProvider
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// UpdateScimServiceProvider updates an existing SCIM service provider configuration.
func (c *Client) UpdateScimServiceProvider(id string, req *ScimServiceProviderCreateRequest) (*ScimServiceProvider, error) {
	body, err := c.doRequest("PUT", fmt.Sprintf("/api/scim/service-provider/%s", id), req)
	if err != nil {
		return nil, err
	}

	var result ScimServiceProvider
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// DeleteScimServiceProvider deletes a SCIM service provider configuration by ID.
func (c *Client) DeleteScimServiceProvider(id string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/api/scim/service-provider/%s", id), nil)
	return err
}
