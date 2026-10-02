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

// CreateOneTimeAccessToken creates a one-time access token for a user. The
// POST is never retried. A success response without a token is an error
// wrapping ErrResultUnread: the creation's outcome is uncertain.
func (c *Client) CreateOneTimeAccessToken(ctx context.Context, userID string, req *OneTimeAccessTokenRequest) (*OneTimeAccessToken, error) {
	tflog.Debug(ctx, "CreateOneTimeAccessToken request", map[string]interface{}{
		"user_id": userID,
		"ttl":     req.TTL,
	})

	id, err := uuidSegment("user", userID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "POST", "/api/users/"+id+"/one-time-access-token", req)
	if err != nil {
		return nil, err
	}

	// The POST succeeded, so a token may exist from here on; it is never sent
	// again. Pocket ID answers {"token": "..."} (onetimeaccess handler).
	var token OneTimeAccessToken
	if err := json.Unmarshal(body, &token); err != nil || token.Token == "" {
		return nil, fmt.Errorf("one-time access token for user %s: %w: the response held no token, so a token may have been created that stays valid until it expires; no second request was sent", userID, ErrResultUnread)
	}

	return &token, nil
}
