package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// GroupDetail is a user group with everything the single-object endpoint
// (GET /api/user-groups/{id}) reports: its claims, its members and the OIDC
// clients that list it as allowed. The list endpoint carries the claims and a
// member count but neither the members nor the clients.
type GroupDetail struct {
	ID           string
	Name         string
	FriendlyName string
	LdapID       *string
	CreatedAt    string
	CustomClaims []CustomClaim
	// MemberIDs are the IDs of the users in the group.
	MemberIDs []string
	// AllowedClientIDs are the IDs of the clients whose allowed-group list
	// includes this group (the oidc_clients_allowed_user_groups table, which the
	// group and the client both write). A client that is not group-restricted
	// admits everyone whatever the table says.
	AllowedClientIDs []string
}

type groupRelationRef struct {
	ID string `json:"id"`
}

func groupRelationIDs(refs []groupRelationRef) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	return ids
}

// GetUserGroupDetail reads one group from GET /api/user-groups/{id}. A group
// that does not exist is an error satisfying IsNotFound(err, ResourceUserGroup).
func (c *Client) GetUserGroupDetail(ctx context.Context, groupID string) (*GroupDetail, error) {
	id, err := uuidSegment("user group", groupID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/user-groups/"+id, nil)
	if err != nil {
		return nil, err
	}
	var wire struct {
		ID                 string             `json:"id"`
		Name               string             `json:"name"`
		FriendlyName       string             `json:"friendlyName"`
		LdapID             *string            `json:"ldapId"`
		CreatedAt          string             `json:"createdAt"`
		CustomClaims       []CustomClaim      `json:"customClaims"`
		Users              []groupRelationRef `json:"users"`
		AllowedOidcClients []groupRelationRef `json:"allowedOidcClients"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	return &GroupDetail{
		ID:               wire.ID,
		Name:             wire.Name,
		FriendlyName:     wire.FriendlyName,
		LdapID:           wire.LdapID,
		CreatedAt:        wire.CreatedAt,
		CustomClaims:     wire.CustomClaims,
		MemberIDs:        groupRelationIDs(wire.Users),
		AllowedClientIDs: groupRelationIDs(wire.AllowedOidcClients),
	}, nil
}

// GroupMemberIDs returns, for every user group that has members, the IDs of its
// members, built from one pass over the user list: each user in GET /api/users
// carries the groups it belongs to. That costs a request per 100 users however
// many groups there are, where reading each group would cost one request per
// group. A group without members is absent from the map.
func (c *Client) GroupMemberIDs(ctx context.Context) (map[string][]string, error) {
	type userLinks struct {
		ID         string             `json:"id"`
		UserGroups []groupRelationRef `json:"userGroups"`
	}
	users, err := listAll(ctx, c, "users", "/api/users", nil, func(user userLinks) string { return user.ID })
	if err != nil {
		return nil, err
	}
	members := map[string][]string{}
	for _, user := range users {
		for _, group := range user.UserGroups {
			members[group.ID] = append(members[group.ID], user.ID)
		}
	}
	return members, nil
}

// AllowedClientIDsByGroup returns, for every user group that is allowed on
// some OIDC client, the IDs of those clients, built from one pass over the
// client list: each client in GET /api/oidc/clients carries its allowed groups
// from Pocket ID 2.15. A group allowed on no client is absent from the map.
//
// Pocket ID 2.14's client list carries only a count of allowed groups. There
// the result is ok == false and a caller must read the groups one by one
// (GetUserGroupDetail). An empty client list proves nothing about the version
// and gives an empty map with ok == true, which is right: no client allows any
// group.
func (c *Client) AllowedClientIDsByGroup(ctx context.Context) (clients map[string][]string, ok bool, err error) {
	type clientLinks struct {
		ID string `json:"id"`
		// AllowedUserGroups is absent (nil) from a 2.14 list, null when
		// present and empty.
		AllowedUserGroups json.RawMessage `json:"allowedUserGroups"`
	}
	list, err := listAll(ctx, c, "OIDC clients", "/api/oidc/clients", nil, func(item clientLinks) string { return item.ID })
	if err != nil {
		return nil, false, err
	}
	byGroup := map[string][]string{}
	for _, item := range list {
		if item.AllowedUserGroups == nil {
			return nil, false, nil
		}
		var groups []groupRelationRef
		if err := json.Unmarshal(item.AllowedUserGroups, &groups); err != nil {
			return nil, false, fmt.Errorf("error unmarshaling response: %w", err)
		}
		for _, group := range groups {
			byGroup[group.ID] = append(byGroup[group.ID], item.ID)
		}
	}
	return byGroup, true, nil
}
