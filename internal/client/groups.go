package client

import (
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
func (c *Client) CreateUserGroup(group *UserGroupCreateRequest) (*UserGroup, error) {
	body, err := c.doRequest("POST", "/api/user-groups", group)
	if err != nil {
		return nil, err
	}

	var result UserGroup
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// GetUserGroup retrieves a user group by ID
func (c *Client) GetUserGroup(groupID string) (*UserGroup, error) {
	body, err := c.doRequest("GET", fmt.Sprintf("/api/user-groups/%s", groupID), nil)
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
func (c *Client) UpdateUserGroup(groupID string, group *UserGroupCreateRequest) (*UserGroup, error) {
	body, err := c.doRequest("PUT", fmt.Sprintf("/api/user-groups/%s", groupID), group)
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
func (c *Client) DeleteUserGroup(groupID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/api/user-groups/%s", groupID), nil)
	return err
}

// ListUserGroups retrieves all user groups
func (c *Client) ListUserGroups() (*PaginatedResponse[UserGroup], error) {
	body, err := c.doRequest("GET", "/api/user-groups", nil)
	if err != nil {
		return nil, err
	}

	var result PaginatedResponse[UserGroup]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}
