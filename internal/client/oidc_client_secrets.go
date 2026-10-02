package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"golang.org/x/mod/semver"
)

// ClientSecretMetadata describes one secret of an OIDC client, without its
// value, as listed by GET /api/oidc/clients/{id}/secrets (Pocket ID 2.14.0+).
type ClientSecretMetadata struct {
	ID       string `json:"id"`
	IsActive bool   `json:"isActive"`
}

// ClientSecretResponse represents the response when generating a client secret
type ClientSecretResponse struct {
	Secret string `json:"secret"`
}

// GenerateClientSecret generates a new client secret for an OIDC client
func (c *Client) GenerateClientSecret(ctx context.Context, clientID string) (string, error) {
	version, err := c.GetCurrentVersion(ctx)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("/api/oidc/clients/%s/secret", url.PathEscape(clientID))
	if semver.Compare("v"+version, "v2.14.0") >= 0 { // the semver package requires a `v` prefix.
		url += "s"
	}

	body, err := c.doRequest(ctx, "POST", url, nil)
	if err != nil {
		return "", err
	}

	var result struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("error unmarshaling secret response; result uncertain, inspect before recovery")
	}

	if result.Secret == "" {
		return "", fmt.Errorf("secret creation returned no secret; result uncertain, inspect the client before recovery")
	}
	return result.Secret, nil
}

// ListClientSecrets lists an OIDC client's secrets without their values.
// Pocket ID 2.14.0 and later only.
func (c *Client) ListClientSecrets(ctx context.Context, clientID string) ([]ClientSecretMetadata, error) {
	body, err := c.doRequest(ctx, "GET", fmt.Sprintf("/api/oidc/clients/%s/secrets", url.PathEscape(clientID)), nil)
	if err != nil {
		return nil, err
	}

	var result []ClientSecretMetadata
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling client secret list")
	}
	return result, nil
}

// DeleteClientSecret revokes one secret of an OIDC client. Pocket ID 2.14.0
// and later only. Like every mutation it is not retried, and its error is
// returned unchanged: a 404 alone does not prove the secret is gone (it may be
// a wrong base URL or a proxy's page), so a caller that needs that answer
// confirms it with ListClientSecrets.
func (c *Client) DeleteClientSecret(ctx context.Context, clientID, secretID string) error {
	_, err := c.doRequest(ctx, "DELETE", fmt.Sprintf("/api/oidc/clients/%s/secrets/%s", url.PathEscape(clientID), url.PathEscape(secretID)), nil)
	return err
}
