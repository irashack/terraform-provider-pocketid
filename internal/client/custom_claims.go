package client

import (
	"context"
)

// CustomClaim represents a custom claim for users or groups
type CustomClaim struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// UpdateUserCustomClaims replaces all custom claims for a user. The API
// performs a full replace: claims not present in the list are removed.
func (c *Client) UpdateUserCustomClaims(ctx context.Context, userID string, claims []CustomClaim) ([]CustomClaim, error) {
	if claims == nil {
		claims = []CustomClaim{}
	}
	id, err := uuidSegment("user", userID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "PUT", "/api/custom-claims/user/"+id, claims)
	if err != nil {
		return nil, err
	}

	var result []CustomClaim
	if err := decodeResponse(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// UpdateGroupCustomClaims replaces all custom claims for a user group. The API
// performs a full replace: claims not present in the list are removed.
func (c *Client) UpdateGroupCustomClaims(ctx context.Context, groupID string, claims []CustomClaim) ([]CustomClaim, error) {
	if claims == nil {
		claims = []CustomClaim{}
	}
	id, err := uuidSegment("user group", groupID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "PUT", "/api/custom-claims/user-group/"+id, claims)
	if err != nil {
		return nil, err
	}

	var result []CustomClaim
	if err := decodeResponse(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}
