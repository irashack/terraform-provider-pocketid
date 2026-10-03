package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// LDAP error codes Pocket ID (2.14.0 to 2.17.0) answers with HTTP 403 when a
// request would change a user or group that LDAP manages while LDAP is
// enabled (apperror.LdapUserUpdate, apperror.LdapUserGroupUpdate).
const (
	CodeLDAPUserUpdate      = "ldap_user_update"
	CodeLDAPUserGroupUpdate = "ldap_user_group_update"
)

// HasErrorCode reports whether err is Pocket ID's structured error with the
// given code.
func HasErrorCode(err error, code string) bool {
	var status *HTTPError
	return code != "" && errors.As(err, &status) && status.Code == code
}

// LDAPEnabled reports whether the instance has LDAP enabled, from the public
// application configuration (GET /api/application-configuration, where
// ldapEnabled is marked public in 2.14.0 to 2.17.0). While it is, Pocket ID
// lets the API change only the locale of a user that has an LDAP ID (other
// fields are silently kept) and refuses to update or delete a group that
// has one.
func (c *Client) LDAPEnabled(ctx context.Context) (bool, error) {
	body, err := c.doRequest(ctx, "GET", "/api/application-configuration", nil)
	if err != nil {
		return false, err
	}
	var vars []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &vars); err != nil {
		return false, fmt.Errorf("the application configuration could not be read")
	}
	for _, v := range vars {
		if v.Key == "ldapEnabled" {
			return v.Value == "true", nil
		}
	}
	return false, fmt.Errorf("the application configuration does not say whether LDAP is enabled")
}
