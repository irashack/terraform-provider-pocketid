package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// User represents a user in Pocket-ID
type User struct {
	ID            string        `json:"id,omitempty"`
	Username      string        `json:"username"`
	Email         string        `json:"email"`
	FirstName     string        `json:"firstName,omitempty"`
	LastName      string        `json:"lastName,omitempty"`
	DisplayName   string        `json:"displayName,omitempty"`
	EmailVerified bool          `json:"emailVerified"`
	IsAdmin       bool          `json:"isAdmin"`
	Locale        *string       `json:"locale,omitempty"`
	Disabled      bool          `json:"disabled"`
	UserGroups    []UserGroup   `json:"userGroups,omitempty"`
	CustomClaims  []CustomClaim `json:"customClaims,omitempty"`
	LdapID        *string       `json:"ldapId,omitempty"`
}

// UserCreateRequest represents a request to create or update a user
type UserCreateRequest struct {
	Username      string  `json:"username"`
	Email         string  `json:"email"`
	FirstName     string  `json:"firstName,omitempty"`
	LastName      string  `json:"lastName,omitempty"`
	DisplayName   string  `json:"displayName,omitempty"`
	EmailVerified bool    `json:"emailVerified"`
	IsAdmin       bool    `json:"isAdmin"`
	Locale        *string `json:"locale,omitempty"`
	Disabled      bool    `json:"disabled"`
}

// UpdateUserGroupsRequest represents a request to update a user's groups
type UpdateUserGroupsRequest struct {
	UserGroupIDs []string `json:"userGroupIds"`
}

// CreateUser creates a new user
func (c *Client) CreateUser(ctx context.Context, user *UserCreateRequest) (*User, error) {
	body, err := c.doRequest(ctx, "POST", "/api/users", user)
	if err != nil {
		return nil, err
	}

	var result User
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	if err := ValidateUUID("user", result.ID); err != nil {
		return nil, fmt.Errorf("user creation returned no usable ID, so no follow-up request uses it; the user may exist: inspect before recovery: %w", err)
	}

	return &result, nil
}

// GetUser retrieves a user by ID
func (c *Client) GetUser(ctx context.Context, userID string) (*User, error) {
	id, err := uuidSegment("user", userID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/users/"+id, nil)
	if err != nil {
		return nil, err
	}

	var result User
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// UpdateUser updates an existing user
func (c *Client) UpdateUser(ctx context.Context, userID string, user *UserCreateRequest) (*User, error) {
	id, err := uuidSegment("user", userID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "PUT", "/api/users/"+id, user)
	if err != nil {
		return nil, err
	}

	var result User
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return &result, nil
}

// DeleteUser deletes a user
func (c *Client) DeleteUser(ctx context.Context, userID string) error {
	id, err := uuidSegment("user", userID)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", "/api/users/"+id, nil)
	return err
}

// ListUsers retrieves one page of users (the server's default: page 1, 20
// items). Pocket-ID paginates GET /api/users, so this alone silently misses
// any user past the first page. Prefer ListAllUsers to see every user.
func (c *Client) ListUsers(ctx context.Context) (*PaginatedResponse[User], error) {
	return c.ListUsersPage(ctx, 0, 0, "")
}

// ListUsersPage retrieves one page of users. page and limit are 1-based;
// zero selects the server's own default for that parameter (page 1, limit
// 20). search, when non-empty, is passed through to the server's free-text
// search filter (matching is the server's own logic, e.g. substring across
// username/email/name — not guaranteed to be exact or to fit on one page),
// so callers doing an exact lookup must still filter the returned users
// themselves and must still follow pagination.
func (c *Client) ListUsersPage(ctx context.Context, page, limit int, search string) (*PaginatedResponse[User], error) {
	query := url.Values{}
	if search != "" {
		query.Set("search", search)
	}
	return getPage[User](ctx, c, "/api/users", query, page, limit)
}

// ListAllUsers returns every user, optionally narrowed by the server's
// free-text search filter (empty string for no filter), following all pages
// of GET /api/users (see listAll). The search matches substrings of several
// fields, so a caller doing an exact lookup must still compare the results.
func (c *Client) ListAllUsers(ctx context.Context, search string) ([]User, error) {
	query := url.Values{}
	if search != "" {
		query.Set("search", search)
	}
	return listAll(ctx, c, "users", "/api/users", query, func(user User) string { return user.ID })
}

// UpdateUserGroups updates the groups a user belongs to
func (c *Client) UpdateUserGroups(ctx context.Context, userID string, groupIDs []string) error {
	// Ensure groupIDs is never nil to serialize as empty array instead of null
	if groupIDs == nil {
		groupIDs = []string{}
	}
	req := UpdateUserGroupsRequest{UserGroupIDs: groupIDs}
	id, err := uuidSegment("user", userID)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "PUT", "/api/users/"+id+"/user-groups", req)
	return err
}

// AddUserToGroup adds a user to a group without changing the user's other
// group memberships. Pocket-ID exposes no endpoint to add a single member to
// a group; the only mutating endpoint is PUT /api/users/{id}/user-groups,
// which replaces a user's entire group list. This performs a read-modify-write:
// it reads the user's current groups, adds groupID if it is not already
// present, and writes the full list back. A concurrent writer of the same
// user's groups (another apply of this provider, or an external process such
// as an onboarding broker) that runs between the read and the write can have
// its change silently overwritten; there is no compare-and-swap primitive
// that would close this window.
func (c *Client) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	user, err := c.GetUser(ctx, userID)
	if err != nil {
		return err
	}

	groupIDs := make([]string, 0, len(user.UserGroups)+1)
	for _, group := range user.UserGroups {
		groupIDs = append(groupIDs, group.ID)
		if group.ID == groupID {
			// Already a member; nothing to do.
			return nil
		}
	}
	groupIDs = append(groupIDs, groupID)

	return c.UpdateUserGroups(ctx, userID, groupIDs)
}

// RemoveUserFromGroup removes a user from a group without changing the
// user's other group memberships. Removing membership of a user who no
// longer exists is treated as already done, but only once that is positively
// confirmed (see IsUserNotFound): a generic or malformed 404 - a wrong base
// URL, a proxy's own not-found page, or an endpoint missing on an older
// server - does not by itself prove the user is gone, and is returned as an
// error instead. See AddUserToGroup for the read-modify-write mechanism this
// relies on and the race window it leaves.
func (c *Client) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
	user, err := c.GetUser(ctx, userID)
	if err != nil {
		if IsUserNotFound(err) {
			return nil
		}
		return err
	}

	found := false
	groupIDs := make([]string, 0, len(user.UserGroups))
	for _, group := range user.UserGroups {
		if group.ID == groupID {
			found = true
			continue
		}
		groupIDs = append(groupIDs, group.ID)
	}
	if !found {
		// Already not a member; nothing to do.
		return nil
	}

	if err := c.UpdateUserGroups(ctx, userID, groupIDs); err != nil {
		// A 404 from this PUT does not by itself prove the user is gone: it
		// could be a wrong path or a proxy's generic not-found response.
		// Re-check with a GET, which does positively identify a missing
		// user, before treating the removal as already satisfied.
		var status *HTTPError
		if errors.As(err, &status) && status.StatusCode == 404 {
			if _, getErr := c.GetUser(ctx, userID); IsUserNotFound(getErr) {
				return nil
			}
		}
		return err
	}
	return nil
}

// UserHasGroupMembership reports whether userID currently belongs to groupID.
// It returns the error from GetUser unchanged, so callers can use
// IsUserNotFound to distinguish a confirmed-missing user from any other
// error (including a merely-generic 404) and from the user simply not
// belonging to the group.
func (c *Client) UserHasGroupMembership(ctx context.Context, userID, groupID string) (bool, error) {
	user, err := c.GetUser(ctx, userID)
	if err != nil {
		return false, err
	}

	for _, group := range user.UserGroups {
		if group.ID == groupID {
			return true, nil
		}
	}
	return false, nil
}
