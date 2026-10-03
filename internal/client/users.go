package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
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
	// ID, on create only, is a caller-chosen user ID (a UUID; Pocket ID
	// 2.12.0 and later). Empty lets Pocket ID generate one. Update ignores it.
	ID       string `json:"id,omitempty"`
	Username string `json:"username"`
	// Email is omitted when empty: Pocket ID stores no address (null), and
	// rejects "" as an invalid one.
	Email         string  `json:"email,omitempty"`
	FirstName     string  `json:"firstName,omitempty"`
	LastName      string  `json:"lastName,omitempty"`
	DisplayName   string  `json:"displayName,omitempty"`
	EmailVerified bool    `json:"emailVerified"`
	IsAdmin       bool    `json:"isAdmin"`
	Locale        *string `json:"locale,omitempty"`
	Disabled      bool    `json:"disabled"`
	// UserGroupIDs, on create only, are the groups the new user is put in.
	// When it is non-empty Pocket ID (2.14.0 to 2.17.0) does not add the
	// instance's signup default groups; an ID that names no group is
	// dropped without an error. Update ignores it.
	UserGroupIDs []string `json:"userGroupIds,omitempty"`
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
	if err := decodeResult(body, &result); err != nil {
		return nil, err
	}
	// The request carries no ID yet, so the server chose it.
	if err := c.checkCreatedID("user", "", result.ID); err != nil {
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
	if err := decodeResponse(body, &result); err != nil {
		return nil, err
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
	if err := decodeResult(body, &result); err != nil {
		return nil, err
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

// UpdateUserGroups replaces the groups a user belongs to and returns the IDs
// of the groups the user is in afterwards, as the server's response reports
// them.
//
// Pocket ID keeps only the requested IDs that name an existing group and
// drops the rest without an error (UserService.UpdateUserGroups looks the IDs
// up with "id IN ?"), so a caller compares the result with what it asked
// for. An empty or nil groupIDs is sent as [] (the server rejects null). A
// response that cannot be decoded gives an error wrapping ErrResultUnread:
// the change was made, its result is unknown. The PUT is never retried.
func (c *Client) UpdateUserGroups(ctx context.Context, userID string, groupIDs []string) ([]string, error) {
	if groupIDs == nil {
		groupIDs = []string{}
	}
	req := UpdateUserGroupsRequest{UserGroupIDs: groupIDs}
	id, err := uuidSegment("user", userID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "PUT", "/api/users/"+id+"/user-groups", req)
	if err != nil {
		return nil, err
	}
	// UserDto.userGroups has no omitempty: a user in no group is null.
	var fields map[string]json.RawMessage
	var groups []UserGroup
	raw, present := json.RawMessage(nil), false
	if json.Unmarshal(body, &fields) == nil {
		raw, present = fields["userGroups"]
	}
	if !present || json.Unmarshal(raw, &groups) != nil {
		return nil, fmt.Errorf("groups of user %s: %w: the response did not list them", userID, ErrResultUnread)
	}
	return userGroupIDs(groups), nil
}

// UserGroupsMismatchError reports that after a write of a user's groups the
// server holds a different set from the one requested. Pocket ID drops a
// requested ID that names no group (one that never existed or was deleted
// meanwhile) without an error, so Missing usually names such IDs. The write
// itself was made: the user's groups are now the held set.
type UserGroupsMismatchError struct {
	UserID string
	// Missing were requested but are not held; Unexpected are held but
	// were not requested. Both are sorted.
	Missing, Unexpected []string
}

func (e *UserGroupsMismatchError) Error() string {
	var parts []string
	if len(e.Missing) > 0 {
		parts = append(parts, "the user is not in group(s) "+strings.Join(e.Missing, ", ")+
			" (Pocket ID ignores a group ID that names no group, for example one deleted during the apply)")
	}
	if len(e.Unexpected) > 0 {
		parts = append(parts, "the user is also in group(s) "+strings.Join(e.Unexpected, ", ")+" that were not requested")
	}
	return "Pocket ID did not apply the requested groups of user " + e.UserID + ": " + strings.Join(parts, "; ")
}

// diffGroupIDs returns the IDs in want but not in held, and in held but not
// in want, each sorted.
func diffGroupIDs(want, held []string) (missing, unexpected []string) {
	inHeld := make(map[string]bool, len(held))
	for _, id := range held {
		inHeld[id] = true
	}
	inWant := make(map[string]bool, len(want))
	for _, id := range want {
		inWant[id] = true
		if !inHeld[id] {
			missing = append(missing, id)
		}
	}
	for id := range inHeld {
		if !inWant[id] {
			unexpected = append(unexpected, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	return missing, unexpected
}

// writeUserGroups replaces the user's groups with groupIDs and returns the
// groups the user is in afterwards. When the PUT's response does not list
// them, they are read back with a GET; if that fails too, the error wraps
// ErrResultUnread (the write was made, its result is unknown).
func (c *Client) writeUserGroups(ctx context.Context, userID string, groupIDs []string) ([]string, error) {
	held, err := c.UpdateUserGroups(ctx, userID, groupIDs)
	if err == nil {
		return held, nil
	}
	if !errors.Is(err, ErrResultUnread) {
		return nil, err
	}
	user, readErr := c.GetUser(ctx, userID)
	if readErr != nil {
		return nil, fmt.Errorf("groups of user %s: %w; reading them back failed: %w", userID, ErrResultUnread, readErr)
	}
	return user.GroupIDs(), nil
}

// CheckUserGroups compares the groups a user is in (held) with the ones
// requested (want) and returns a *UserGroupsMismatchError when they differ.
func CheckUserGroups(userID string, want, held []string) error {
	if missing, unexpected := diffGroupIDs(want, held); len(missing) > 0 || len(unexpected) > 0 {
		return &UserGroupsMismatchError{UserID: userID, Missing: missing, Unexpected: unexpected}
	}
	return nil
}

// GroupIDs returns the IDs of the groups the user is in, as the response
// that produced u listed them.
func (u *User) GroupIDs() []string {
	ids := make([]string, 0, len(u.UserGroups))
	for _, group := range u.UserGroups {
		ids = append(ids, group.ID)
	}
	return ids
}

// SetUserGroups replaces the groups a user belongs to with exactly groupIDs
// and checks that the server holds that set afterwards. It returns the held
// set; when it differs from groupIDs the error is a *UserGroupsMismatchError
// (the write was made). An empty or nil groupIDs removes every group. The
// PUT is never retried.
func (c *Client) SetUserGroups(ctx context.Context, userID string, groupIDs []string) ([]string, error) {
	held, err := c.writeUserGroups(ctx, userID, groupIDs)
	if err != nil {
		return nil, err
	}
	return held, CheckUserGroups(userID, groupIDs, held)
}

// AddUserToGroup adds a user to a group without changing the user's other
// group memberships, and checks that the user is in the group afterwards: a
// group that does not exist, or was deleted meanwhile, gives a
// *UserGroupsMismatchError naming it instead of a silent success.
//
// Pocket-ID exposes no endpoint to add a single member to a group; the only
// mutating endpoint is PUT /api/users/{id}/user-groups, which replaces a
// user's entire group list. This performs a read-modify-write: it reads the
// user's current groups, adds groupID if it is not already present, and
// writes the full list back. A concurrent writer of the same user's groups
// (another apply of this provider, or an external process such as an
// onboarding broker) that runs between the read and the write can have its
// change silently overwritten; there is no compare-and-swap primitive that
// would close this window.
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

	held, err := c.writeUserGroups(ctx, userID, groupIDs)
	if err != nil {
		return err
	}
	if !slices.Contains(held, groupID) {
		return &UserGroupsMismatchError{UserID: userID, Missing: []string{groupID}}
	}
	return nil
}

// RemoveUserFromGroup removes a user from a group without changing the
// user's other group memberships, and checks that the user is no longer in
// the group afterwards. Removing membership of a user who no longer exists is
// treated as already done, but only once that is positively confirmed (see
// IsUserNotFound): a generic or malformed 404 - a wrong base URL, a proxy's
// own not-found page, or an endpoint missing on an older server - does not by
// itself prove the user is gone, and is returned as an error instead. See
// AddUserToGroup for the read-modify-write mechanism this relies on and the
// race window it leaves.
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

	held, err := c.writeUserGroups(ctx, userID, groupIDs)
	if err != nil {
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
	if slices.Contains(held, groupID) {
		return &UserGroupsMismatchError{UserID: userID, Unexpected: []string{groupID}}
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
