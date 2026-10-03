package client

import "fmt"

// This file holds the checkReturnedID walks shared by several area files:
// the IDs a user, a user group or an OIDC client response carries, nested
// objects included, followed by the check of the text the provider takes
// from it (checkReturnedText). Each returns the first failure, which wraps
// ErrInvalidIdentifier and never includes a value.

// checkUser checks a user's own ID (against addressed, the user the request
// named, when it is not "") and the ID of every group it lists, then its
// text.
func (c *Client) checkUser(addressed string, user *User) error {
	if err := c.checkReturnedID("user", addressed, user.ID); err != nil {
		return err
	}
	if err := c.checkGroupIDs(user.UserGroups); err != nil {
		return err
	}
	return c.checkReturnedText(user.shownTexts()...)
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
// "") and every member it lists, with each member's own groups, then its
// text.
func (c *Client) checkUserGroup(addressed string, group *UserGroup) error {
	if err := c.checkReturnedID("user group", addressed, group.ID); err != nil {
		return err
	}
	if err := c.checkUsers(group.Users); err != nil {
		return err
	}
	return c.checkReturnedText(group.shownTexts()...)
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
// when it is not "") and the IDs nested in it (its allowed user groups and
// its listed secrets), then its text.
func (c *Client) checkOIDCClient(addressed string, oidcClient *OIDCClient) error {
	if err := c.checkReturnedID(kindOIDCClient, addressed, oidcClient.ID); err != nil {
		return err
	}
	if err := c.checkGroupIDs(oidcClient.AllowedUserGroups); err != nil {
		return err
	}
	return c.checkClientContent(oidcClient)
}

// checkClientContent checks what a client answer carries besides its own
// and its groups' IDs: the metadata of every secret it lists
// (checkSecretMetadata) and its text.
func (c *Client) checkClientContent(oidcClient *OIDCClient) error {
	for i := range oidcClient.Credentials.Secrets {
		if err := c.checkSecretMetadata(&oidcClient.Credentials.Secrets[i]); err != nil {
			return err
		}
	}
	return c.checkReturnedText(oidcClient.shownTexts()...)
}

// checkRequestIDs refuses identifiers a request would carry in its body (the
// path and query are checked by checkEndpoint) when one contains the API key
// this client sends. Nothing is sent, and the error names no identifier. A
// method that may later name these IDs in an error (a group the server did
// not apply) relies on this check. Their form is left to the server, which
// ignores an ID that names nothing.
func (c *Client) checkRequestIDs(kind string, ids ...string) error {
	for _, id := range ids {
		if c.reflectsKey(id) {
			return fmt.Errorf("%w: a %s ID in this request contains the API key this provider sends; the request was not sent", ErrInvalidIdentifier, kind)
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
