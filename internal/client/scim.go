package client

import (
	"context"
	"encoding/json"
	"errors"
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
	if err := decodeResult(body, &result); err != nil {
		return nil, err
	}
	if err := c.checkCreatedID("SCIM service provider", "", result.ID); err != nil {
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
	if err := decodeResponse(body, &result); err != nil {
		return nil, err
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
	if err := decodeResult(body, &result); err != nil {
		return nil, err
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

// SyncScimServiceProvider runs a synchronization of users and groups to the
// SCIM service provider and returns when it has finished.
//
// Pocket ID runs it inside the request (scimsync handler: "The sync runs
// inline rather than through the actor so the response reports whether it
// succeeded"), so a nil error means every user and group was pushed and the
// provider's lastSyncedAt moved, and a failure of the remote SCIM endpoint
// comes back as an HTTP 500 whose body is not read. Part of the work may
// already be applied when it fails. The POST is never retried, and a sync that
// takes longer than the client's timeout leaves its outcome unknown: the
// server carries on after the client gives up.
func (c *Client) SyncScimServiceProvider(ctx context.Context, id string) error {
	segment, err := uuidSegment("SCIM service provider", id)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "POST", "/api/scim/service-provider/"+segment+"/sync", nil)
	return err
}

// ErrUnexpectedAnswer marks a successful response that is not the object the
// caller needs, for example an empty object, JSON null, or another object than
// the one asked for. It carries no part of the response.
var ErrUnexpectedAnswer = errors.New("the answer was not the expected object")

// GetScimServiceProviderToken returns the bearer token Pocket ID holds for the
// SCIM service provider providerID of the OIDC client clientID, for a caller
// that will send it back unchanged in a PUT.
//
// Pocket ID always includes the token field (decrypted; "" when none is
// configured), and a PUT that carries "" clears the token. An answer that
// merely decodes to "" is therefore not enough: the object must name
// providerID exactly (not merely case-insensitively: SQLite stores the ID as
// case-sensitive text and the PUT addresses the row by the caller's spelling,
// so another spelling can name another row or none) and hold a present,
// non-null string token ("" is a valid token meaning none). Anything else is ErrUnexpectedAnswer, so a malformed answer
// (an empty object, null, a missing or null token, another provider) can never
// turn into a request that erases the credential.
func (c *Client) GetScimServiceProviderToken(ctx context.Context, clientID, providerID string) (string, error) {
	if err := ValidateUUID("SCIM service provider", providerID); err != nil {
		return "", err
	}
	segment, err := clientIDSegment(clientID)
	if err != nil {
		return "", err
	}
	body, err := c.doRequest(ctx, "GET", "/api/oidc/clients/"+segment+"/scim-service-provider", nil)
	if err != nil {
		return "", err
	}

	var answer struct {
		ID    *string `json:"id"`
		Token *string `json:"token"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.ID == nil || answer.Token == nil || *answer.ID != providerID {
		return "", ErrUnexpectedAnswer
	}
	return *answer.Token, nil
}
