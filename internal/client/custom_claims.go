package client

import (
	"encoding/json"
	"fmt"
)

// CustomClaim represents a custom claim for users or groups
type CustomClaim struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// UpdateUserCustomClaims replaces all custom claims for a user. The API
// performs a full replace: claims not present in the list are removed.
func (c *Client) UpdateUserCustomClaims(userID string, claims []CustomClaim) ([]CustomClaim, error) {
	if claims == nil {
		claims = []CustomClaim{}
	}
	body, err := c.doRequest("PUT", fmt.Sprintf("/api/custom-claims/user/%s", userID), claims)
	if err != nil {
		return nil, err
	}

	var result []CustomClaim
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return result, nil
}

// UpdateGroupCustomClaims replaces all custom claims for a user group. The API
// performs a full replace: claims not present in the list are removed.
func (c *Client) UpdateGroupCustomClaims(groupID string, claims []CustomClaim) ([]CustomClaim, error) {
	if claims == nil {
		claims = []CustomClaim{}
	}
	body, err := c.doRequest("PUT", fmt.Sprintf("/api/custom-claims/user-group/%s", groupID), claims)
	if err != nil {
		return nil, err
	}

	var result []CustomClaim
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return result, nil
}
