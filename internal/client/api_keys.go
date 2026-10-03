package client

import (
	"context"
	"net/url"
)

// APIKey is the metadata Pocket ID lists for an API key (apikey.apiKeyDto).
// It deliberately has no field for the key itself: Pocket ID stores only a
// hash, shows the value once at creation, and its list never carries it, so
// nothing here could hold a key even if a server sent one.
type APIKey struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	ExpiresAt   string  `json:"expiresAt"`
	LastUsedAt  *string `json:"lastUsedAt"`
	CreatedAt   string  `json:"createdAt"`
}

// ListAPIKeys returns the API keys of the user the request is authenticated
// as, which for this client is the owner of the API key it was configured
// with: GET /api/api-keys lists "API keys belonging to the current user"
// (apikey handler, user ID from the authenticated request). It never lists
// other users' keys, and lists none when the client runs on Pocket ID's static
// API key (STATIC_API_KEY), which is not a stored key. Unchanged from
// v2.14.0 to v2.17.0.
//
// Creating, renewing and revoking keys is not offered: creation and renewal
// refuse API-key authentication by design, and revoking could delete the key
// this client authenticates with.
func (c *Client) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	keys, err := listAll(ctx, c, "API keys", "/api/api-keys", url.Values{}, func(k APIKey) string { return k.ID })
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if err := c.checkReturnedID("API key", "", key.ID); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
