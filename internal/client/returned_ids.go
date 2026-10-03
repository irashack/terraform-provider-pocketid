package client

import "fmt"

// This file holds the checkReturnedID walks shared by several area files:
// the IDs a user, a user group or an OIDC client response carries, nested
// objects included. Each returns the first failure, which wraps
// ErrInvalidIdentifier and never includes a value.

// checkUser checks a user's own ID (against addressed, the user the request
// named, when it is not "") and the ID of every group it lists.
func (c *Client) checkUser(addressed string, user *User) error {
	if err := c.checkReturnedID("user", addressed, user.ID); err != nil {
		return err
	}
	return c.checkGroupIDs(user.UserGroups)
}

// checkUsers checks every user of a list: none was addressed.
func (c *Client) checkUsers(users []User) error {
	for i := range users {
		if err := c.checkUser("", &users[i]); err != nil {
			return err
		}
	}
	return nil
}

// checkGroupIDs checks the IDs of groups nested in another object.
func (c *Client) checkGroupIDs(groups []UserGroup) error {
	for _, group := range groups {
		if err := c.checkReturnedID("user group", "", group.ID); err != nil {
			return err
		}
	}
	return nil
}

// checkUserGroup checks a group's own ID (against addressed when it is not
// "") and every member it lists, with each member's own groups.
func (c *Client) checkUserGroup(addressed string, group *UserGroup) error {
	if err := c.checkReturnedID("user group", addressed, group.ID); err != nil {
		return err
	}
	return c.checkUsers(group.Users)
}

// checkUserGroups checks every group of a list: none was addressed.
func (c *Client) checkUserGroups(groups []UserGroup) error {
	for i := range groups {
		if err := c.checkUserGroup("", &groups[i]); err != nil {
			return err
		}
	}
	return nil
}

// checkOIDCClient checks a client's own ID (byte for byte against addressed
// when it is not "") and the IDs nested in it: its allowed user groups and
// its listed secrets.
func (c *Client) checkOIDCClient(addressed string, oidcClient *OIDCClient) error {
	if err := c.checkReturnedID(kindOIDCClient, addressed, oidcClient.ID); err != nil {
		return err
	}
	if err := c.checkGroupIDs(oidcClient.AllowedUserGroups); err != nil {
		return err
	}
	for _, secret := range oidcClient.Credentials.Secrets {
		if err := c.checkReturnedID("client secret", "", secret.ID); err != nil {
			return err
		}
	}
	return nil
}

// unreadResult marks err, found in the answer to a mutation the server
// accepted (a 2xx), as an unread result: the change was made and only its
// result is unknown.
func unreadResult(err error) error {
	return fmt.Errorf("%w: %w", ErrResultUnread, err)
}

// SameUUID reports whether a and b are the same UUID: both valid UUIDs that
// differ at most in the case of their hexadecimal digits. PostgreSQL answers a
// request for an upper-case UUID with the lower-case one, so a caller that
// compares an ID it holds with one a response returned uses this, never ==,
// for every kind except an OIDC client (whose ID is compared byte for byte).
func SameUUID(a, b string) bool {
	return sameUUID(a, b)
}
