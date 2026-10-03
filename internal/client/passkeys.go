package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// Passkey is a WebAuthn credential registered by a user, as far as the
// provider reads it. Pocket ID's answer also carries the credential's ID and
// attestation type; neither is decoded here, and the public key is never sent.
type Passkey struct {
	// ID is Pocket ID's identifier of the passkey (not the WebAuthn credential
	// ID).
	ID   string
	Name string
	// CreatedAt is when the passkey was registered. Pocket ID records no time
	// of last use.
	CreatedAt string
	// BackupEligible is whether the authenticator allows the passkey to be
	// backed up or synced; BackupState is whether it currently is.
	BackupEligible bool
	BackupState    bool
	// Transports are the authenticator transports the passkey reported
	// (usb, internal, hybrid, ...).
	Transports []string
	// AAGUID identifies the authenticator model; "" before Pocket ID 2.15,
	// which does not report it.
	AAGUID string
}

// ListUserPasskeys reads the passkeys of a user from
// GET /api/users/{id}/webauthn-credentials, which exists from Pocket ID 2.14.0.
// A user without passkeys gives an empty list. A user that does not exist is an
// error satisfying IsUserNotFound. The order is the server's, which sorts
// nothing.
func (c *Client) ListUserPasskeys(ctx context.Context, userID string) ([]Passkey, error) {
	id, err := uuidSegment("user", userID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/users/"+id+"/webauthn-credentials", nil)
	if err != nil {
		return nil, err
	}
	var wire []struct {
		ID             string   `json:"id"`
		Name           string   `json:"name"`
		CreatedAt      string   `json:"createdAt"`
		BackupEligible bool     `json:"backupEligible"`
		BackupState    bool     `json:"backupState"`
		Transport      []string `json:"transport"`
		AAGUID         string   `json:"aaguid"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	passkeys := make([]Passkey, 0, len(wire))
	for _, item := range wire {
		passkeys = append(passkeys, Passkey{
			ID: item.ID, Name: item.Name, CreatedAt: item.CreatedAt,
			BackupEligible: item.BackupEligible, BackupState: item.BackupState,
			Transports: item.Transport, AAGUID: item.AAGUID,
		})
	}
	return passkeys, nil
}
