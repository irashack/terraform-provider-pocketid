package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// SignupToken is a signup token as Pocket ID lists it. Token is the secret a
// new user presents to register: both the create and the list endpoints return
// it, so it must never be logged or put in an error.
//
// Pocket ID keeps a token in its actor state store with a time-to-live equal
// to the token's lifetime (usersignup/actor.go), so an expired token is purged
// and disappears from the list; a token whose uses are exhausted stays listed
// until it expires.
type SignupToken struct {
	ID         string      `json:"id"`
	Token      string      `json:"token"`
	ExpiresAt  string      `json:"expiresAt"`
	UsageLimit int         `json:"usageLimit"`
	UsageCount int         `json:"usageCount"`
	UserGroups []UserGroup `json:"userGroups"`
	CreatedAt  string      `json:"createdAt"`
}

// UserGroupIDs returns the IDs of the groups a signup through the token joins.
func (t *SignupToken) UserGroupIDs() []string {
	ids := make([]string, 0, len(t.UserGroups))
	for _, group := range t.UserGroups {
		ids = append(ids, group.ID)
	}
	return ids
}

// SignupTokenCreateRequest is the body of POST /api/signup-tokens
// (usersignup.signupTokenCreateDto). TTL is a Go duration string; Pocket ID
// accepts more than one second and at most 31 days, and uses one hour when it
// is empty. UsageLimit must be 1 to 100.
type SignupTokenCreateRequest struct {
	TTL          string   `json:"ttl,omitempty"`
	UsageLimit   int      `json:"usageLimit"`
	UserGroupIDs []string `json:"userGroupIds"`
}

// CreateSignupToken creates a signup token. Pocket ID silently drops group IDs
// that name no group, so compare UserGroupIDs of the result with the request.
// Available in every supported Pocket ID version (v2.14.0 to v2.17.0).
//
// The POST is never retried. When the server accepted it but the answer holds
// no usable ID, the error wraps ErrResultUnread and the result is nil: a token
// may exist that this client cannot name. When the ID is usable but the token
// value is missing, the error wraps ErrResultUnread and the result is
// returned, so the caller can record the ID of the token that exists.
func (c *Client) CreateSignupToken(ctx context.Context, req *SignupTokenCreateRequest) (*SignupToken, error) {
	body := *req
	if body.UserGroupIDs == nil {
		body.UserGroupIDs = []string{}
	}
	for _, id := range body.UserGroupIDs {
		if err := ValidateUUID("user group", id); err != nil {
			return nil, err
		}
	}

	raw, err := c.doRequest(ctx, "POST", "/api/signup-tokens", &body)
	if err != nil {
		return nil, err
	}

	var result SignupToken
	if err := json.Unmarshal(raw, &result); err != nil || ValidateUUID("signup token", result.ID) != nil {
		return nil, fmt.Errorf("signup token creation: %w: the response held no usable ID, so a token may have been created that this provider cannot name; it expires on its own", ErrResultUnread)
	}
	if result.Token == "" {
		return &result, fmt.Errorf("signup token %s: %w: the response held no token value", result.ID, ErrResultUnread)
	}
	return &result, nil
}

// ListSignupTokens returns every signup token Pocket ID currently holds, with
// its token value, in creation order. Expired tokens are not listed.
func (c *Client) ListSignupTokens(ctx context.Context) ([]SignupToken, error) {
	return listAll(ctx, c, "signup tokens", "/api/signup-tokens", url.Values{}, func(t SignupToken) string { return t.ID })
}

// DeleteSignupToken deletes a signup token by ID. Pocket ID looks the token up
// in its list first and answers 204 whether or not it was there (an expired or
// already deleted token "already reaches the desired end state"), so success
// does not say that the token existed, and there is no not-found answer.
func (c *Client) DeleteSignupToken(ctx context.Context, id string) error {
	segment, err := uuidSegment("signup token", id)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", "/api/signup-tokens/"+segment, nil)
	return err
}
