package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// UserGroup represents a user group in Pocket-ID
type UserGroup struct {
	ID           string        `json:"id,omitempty"`
	Name         string        `json:"name"`
	FriendlyName string        `json:"friendlyName"`
	Users        []User        `json:"users,omitempty"`
	UserCount    int           `json:"userCount,omitempty"`
	CustomClaims []CustomClaim `json:"customClaims,omitempty"`
	LdapID       *string       `json:"ldapId,omitempty"`
	CreatedAt    string        `json:"createdAt,omitempty"`
}

// UserGroupCreateRequest represents a request to create or update a user group
type UserGroupCreateRequest struct {
	Name         string `json:"name"`
	FriendlyName string `json:"friendlyName"`
}

// CreateUserGroup creates a new user group
func (c *Client) CreateUserGroup(ctx context.Context, group *UserGroupCreateRequest) (*UserGroup, error) {
	body, err := c.doRequest(ctx, "POST", "/api/user-groups", group)
	if err != nil {
		return nil, err
	}

	var result UserGroup
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	if err := ValidateUUID("user group", result.ID); err != nil {
		return nil, fmt.Errorf("user group creation returned no usable ID, so no follow-up request uses it; the user group may exist: inspect before recovery: %w", err)
	}

	return &result, nil
}

// GetUserGroup retrieves a user group by ID
func (c *Client) GetUserGroup(ctx context.Context, groupID string) (*UserGroup, error) {
	id, err := uuidSegment("user group", groupID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/user-groups/"+id, nil)
	if err != nil {
		return nil, err
	}

	var result UserGroup
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// UpdateUserGroup updates an existing user group
func (c *Client) UpdateUserGroup(ctx context.Context, groupID string, group *UserGroupCreateRequest) (*UserGroup, error) {
	id, err := uuidSegment("user group", groupID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "PUT", "/api/user-groups/"+id, group)
	if err != nil {
		return nil, err
	}

	var result UserGroup
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// DeleteUserGroup deletes a user group
func (c *Client) DeleteUserGroup(ctx context.Context, groupID string) error {
	id, err := uuidSegment("user group", groupID)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", "/api/user-groups/"+id, nil)
	return err
}

// ListUserGroups retrieves all user groups
func (c *Client) ListUserGroups(ctx context.Context) (*PaginatedResponse[UserGroup], error) {
	body, err := c.doRequest(ctx, "GET", "/api/user-groups", nil)
	if err != nil {
		return nil, err
	}

	var result PaginatedResponse[UserGroup]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}
