package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// OneTimeAccessToken represents a one-time access token. The create endpoint
// only returns the token value; pocket-id v2 exposes no GET endpoint.
type OneTimeAccessToken struct {
	Token string `json:"token"`
}

// OneTimeAccessTokenRequest represents a request to create a one-time access token.
// The API expects a ttl (lifetime) as a duration string, e.g. "15m" or "1h".
type OneTimeAccessTokenRequest struct {
	TTL string `json:"ttl"`
}

// CreateOneTimeAccessToken creates a new one-time access token for a user
func (c *Client) CreateOneTimeAccessToken(ctx context.Context, userID string, req *OneTimeAccessTokenRequest) (*OneTimeAccessToken, error) {
	tflog.Debug(ctx, "CreateOneTimeAccessToken request", map[string]interface{}{
		"user_id": userID,
		"ttl":     req.TTL,
	})

	body, err := c.doRequest(ctx, "POST", fmt.Sprintf("/api/users/%s/one-time-access-token", userID), req)
	if err != nil {
		return nil, err
	}

	var token OneTimeAccessToken
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &token, nil
}
