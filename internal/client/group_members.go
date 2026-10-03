package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// SetGroupMembers replaces the whole membership of a group with userIDs, through
// PUT /api/user-groups/{id}/users, and returns the IDs of the users the group
// holds afterwards as the server's response reports them.
//
// Pocket ID keeps only the requested IDs that name an existing user and drops
// the rest without an error (UserGroupService.UpdateUsersInternal looks them up
// with "id IN ?"), so a caller compares the result with what it asked for. Each
// ID must be a UUID: the client refuses anything else before sending, because
// the server would drop it silently. An empty or nil userIDs is sent as [] (the
// server rejects null) and empties the group. A response that cannot be
// decoded gives an error wrapping ErrResultUnread: the change was made, its
// result is unknown. The PUT is never retried.
//
// On Pocket ID 2.17 a removed member may be signed out of group-restricted
// clients that have a back-channel logout URL.
func (c *Client) SetGroupMembers(ctx context.Context, groupID string, userIDs []string) ([]string, error) {
	id, err := uuidSegment("user group", groupID)
	if err != nil {
		return nil, err
	}
	if userIDs == nil {
		userIDs = []string{}
	}
	for _, userID := range userIDs {
		if err := ValidateUUID("user", userID); err != nil {
			return nil, err
		}
	}
	body, err := c.doRequest(ctx, "PUT", "/api/user-groups/"+id+"/users", struct {
		UserIDs []string `json:"userIds"`
	}{UserIDs: userIDs})
	if err != nil {
		return nil, err
	}
	// UserGroupDto.users has no omitempty: a group without members is [] or
	// null; a response without the key is not one this client understands.
	var fields map[string]json.RawMessage
	if err := decodeResult(body, &fields); err != nil {
		return nil, fmt.Errorf("members of group %s: %w", groupID, err)
	}
	raw, present := fields["users"]
	var users []groupRelationRef
	if !present || json.Unmarshal(raw, &users) != nil {
		return nil, fmt.Errorf("members of group %s: %w: the response did not list them", groupID, ErrResultUnread)
	}
	// The response must describe this group, and every member it lists must
	// pass the ID check, before the members are taken as the result.
	var gotID string
	if rawID, ok := fields["id"]; ok && json.Unmarshal(rawID, &gotID) != nil {
		gotID = ""
	}
	if err := c.checkReturnedID("user group", groupID, gotID); err != nil {
		return nil, fmt.Errorf("members of group %s: %w", groupID, unreadResult(err))
	}
	for _, user := range users {
		if err := c.checkReturnedID("user", "", user.ID); err != nil {
			return nil, fmt.Errorf("members of group %s: %w", groupID, unreadResult(err))
		}
	}
	return groupRelationIDs(users), nil
}
