package client

import (
	"context"
	"net/url"
	"strings"
)

// SearchUserGroups returns every user group whose name contains name,
// following all pages of GET /api/user-groups?search=name (see listAll).
//
// The match is the server's own: a SQL LIKE on the group name
// ("name LIKE %term%"), so it is case-insensitive on SQLite, treats "_" and "%"
// in name as wildcards, and may return groups whose name merely resembles
// name. It is always a superset of the exact match, so a caller looking for one
// group by name must compare the names it gets back. An empty name returns
// every group.
//
// PostgreSQL treats a backslash in a LIKE pattern as an escape character,
// which can make the pattern miss the very group it names. A name containing a
// backslash is therefore not sent as a search: every group is listed instead,
// and the caller's comparison still picks the match.
func (c *Client) SearchUserGroups(ctx context.Context, name string) ([]UserGroup, error) {
	if strings.Contains(name, `\`) {
		return c.ListUserGroups(ctx)
	}
	query := url.Values{}
	if name != "" {
		query.Set("search", name)
	}
	return listAll(ctx, c, "user groups", "/api/user-groups", query, func(group UserGroup) string { return group.ID })
}
