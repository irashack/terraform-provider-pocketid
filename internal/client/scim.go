package client

import (
	"context"
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
func (c *Client) CreateScimServiceProvider(ctx context.Context, req *ScimServiceProviderCreateRequest) (*ScimServiceProvider, error) {
	body, err := c.doRequest(ctx, "POST", "/api/scim/service-provider", req)
	if err != nil {
		return nil, err
	}

	var result ScimServiceProvider
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	if err := checkCreatedID("SCIM service provider", "", result.ID); err != nil {
		return nil, fmt.Errorf("SCIM service provider creation returned no usable ID, so no follow-up request uses it; the SCIM service provider may exist: inspect before recovery: %w", err)
	}

	return &result, nil
}

// GetClientScimServiceProvider retrieves the SCIM service provider configuration
// for an OIDC client. The token is returned decrypted.
func (c *Client) GetClientScimServiceProvider(ctx context.Context, clientID string) (*ScimServiceProvider, error) {
	client, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/oidc/clients/"+client+"/scim-service-provider", nil)
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
func (c *Client) UpdateScimServiceProvider(ctx context.Context, id string, req *ScimServiceProviderCreateRequest) (*ScimServiceProvider, error) {
	segment, err := uuidSegment("SCIM service provider", id)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "PUT", "/api/scim/service-provider/"+segment, req)
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
func (c *Client) DeleteScimServiceProvider(ctx context.Context, id string) error {
	segment, err := uuidSegment("SCIM service provider", id)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", "/api/scim/service-provider/"+segment, nil)
	return err
}
