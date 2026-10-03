package client

import (
	"context"
)

// GetCurrentUser reads the user the API key belongs to, from
// GET /api/users/me. Pocket ID accepts an API key on this route (it is an
// any-signed-in-user route that resolves the key's owner) in every version
// from 2.14.0 to 2.17.0, and answers with the same record GET /api/users/{id}
// gives, including the user's groups and custom claims.
func (c *Client) GetCurrentUser(ctx context.Context) (*User, error) {
	body, err := c.doRequest(ctx, "GET", "/api/users/me", nil)
	if err != nil {
		return nil, err
	}
	var user User
	if err := decodeResponse(body, &user); err != nil {
		return nil, err
	}
	if err := c.checkUser("", &user); err != nil {
		return nil, err
	}
	return &user, nil
}
