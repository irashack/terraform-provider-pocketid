package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/mod/semver"
)

// ClientSecretsMinVersion is the first Pocket ID with several secrets per
// client (POST/GET /oidc/clients/{id}/secrets and DELETE
// .../secrets/{secretId}); before it, POST .../secret replaced the only
// secret and returned just its value.
const ClientSecretsMinVersion = "2.14.0"

// clientSecretsMinVersion is the unexported name the version checks below use.
const clientSecretsMinVersion = ClientSecretsMinVersion

// MaxClientSecrets is the most secrets one client can hold, expired ones
// included (model.MaxOidcClientSecrets, 20 in Pocket ID 2.14.0 to 2.17.0).
// The server refuses another secret with HTTP 400 "validation_failed",
// a code it shares with other refusals, so a caller that needs to tell this
// one apart counts the client's secrets.
const MaxClientSecrets = 20

// ErrCreatedSecretValueMissing marks a create response that named the new
// secret (a usable ID) but carried no value. The secret exists and can be
// revoked by its ID; its value is lost.
var ErrCreatedSecretValueMissing = errors.New("the server created a client secret but did not return its value")

// ClientSecretMetadata describes one secret of an OIDC client, without its
// value, as GET /api/oidc/clients/{id}/secrets lists it (OidcClientSecretDto,
// Pocket ID 2.14.0+, unchanged to 2.17.0).
type ClientSecretMetadata struct {
	ID string `json:"id"`
	// Prefix is the secret's first characters in clear text
	// (OidcClientSecretPrefixLength, 4), empty for a secret migrated from
	// the single-secret column of earlier versions.
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt"`
	// IsActive is false once the secret has expired.
	IsActive bool `json:"isActive"`
}

// ClientSecretResponse represents the response when generating a client secret
type ClientSecretResponse struct {
	Secret string `json:"secret"`
}

// ClientSecretOptions are the optional inputs for a new secret
// (OidcClientSecretCreateDto, Pocket ID 2.14.0+).
type ClientSecretOptions struct {
	// Value is a caller-chosen secret: at least 16 printable ASCII
	// characters. Empty lets the server generate one (32 alphanumerics).
	Value string
	// ExpiresAt makes the secret unusable after this time; nil means it
	// never expires. The server refuses a time that is not in the future.
	ExpiresAt *time.Time
}

// ClientSecret is a secret just created: its metadata and its value, which
// Pocket ID returns only in this response. Value is excluded from JSON and
// from fmt output so that printing the struct cannot reveal it.
type ClientSecret struct {
	ClientSecretMetadata
	Value string `json:"-"`
}

// String describes the secret without its value.
func (s ClientSecret) String() string {
	return fmt.Sprintf("client secret %q (value redacted)", s.ID)
}

// GoString describes the secret without its value.
func (s ClientSecret) GoString() string { return s.String() }

// validateSecretValue applies the server's rule for a caller-chosen secret
// (binding "omitempty,min=16,printascii") before it is sent.
func validateSecretValue(value string) error {
	if len(value) < 16 {
		return errors.New("a client secret value must be at least 16 characters")
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return errors.New("a client secret value must be printable ASCII")
		}
	}
	return nil
}

// GenerateClientSecret creates a secret for an OIDC client and returns it with
// its value. opts may be nil.
//
// On Pocket ID 2.14.0 and later the client keeps its other secrets (at most
// 20, expired ones included) and the result carries the new secret's ID and
// metadata; opts can supply the value and an expiry. Before 2.14.0 the call
// replaces the client's only secret, the result has only a Value, and opts
// must be nil or empty.
//
// The POST is never retried. Any failure after it was sent (a response
// without a value or a usable ID, or one that cannot be decoded) leaves the
// outcome uncertain: the secret may exist, so inspect the client's secrets
// before trying again.
func (c *Client) GenerateClientSecret(ctx context.Context, clientID string, opts *ClientSecretOptions) (*ClientSecret, error) {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	body, err := secretCreateBody(opts)
	if err != nil {
		return nil, err
	}
	version, err := c.GetCurrentVersion(ctx)
	if err != nil {
		return nil, err
	}
	multiple := semver.Compare("v"+version, "v"+clientSecretsMinVersion) >= 0 // the semver package requires a `v` prefix.
	if body != nil && !multiple {
		return nil, fmt.Errorf("a chosen secret value or an expiry requires Pocket ID %s or later; no secret was created", clientSecretsMinVersion)
	}

	endpoint := "/api/oidc/clients/" + id + "/secret"
	if multiple {
		endpoint += "s"
	}

	response, err := c.doRequest(ctx, "POST", endpoint, body)
	if err != nil {
		return nil, err
	}

	if multiple {
		secret, err := decodeCreatedSecret(response)
		if err != nil {
			return nil, err
		}
		return secret, nil
	}

	var result struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling secret response; result uncertain, inspect before recovery")
	}
	if result.Secret == "" {
		return nil, fmt.Errorf("secret creation returned no secret; result uncertain, inspect the client before recovery")
	}
	return &ClientSecret{Value: result.Secret}, nil
}

// CreateClientSecret adds one secret to an OIDC client through
// POST /api/oidc/clients/{id}/secrets (Pocket ID 2.14.0+) and returns it
// with its value. opts may be nil: the server then generates the value (32
// alphanumerics) and the secret never expires.
//
// Unlike GenerateClientSecret it never falls back to the single-secret
// endpoint of older servers, which replaces the client's only secret: on a
// server without the route the POST is answered with the router's 404
// (HTTPError.MissingEndpoint) and nothing is created. Callers that need a
// clear message check the version first (VersionAtLeast with
// ClientSecretsMinVersion).
//
// The POST is never retried. When the response names the new secret but
// carries no value, the result holds the secret's metadata (so it can be
// revoked by ID) and the error wraps ErrCreatedSecretValueMissing. Any other
// failure after the request was sent returns no result: the outcome is
// uncertain, so inspect the client's secrets (ListClientSecrets) before
// trying again.
func (c *Client) CreateClientSecret(ctx context.Context, clientID string, opts *ClientSecretOptions) (*ClientSecret, error) {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	body, err := secretCreateBody(opts)
	if err != nil {
		return nil, err
	}
	response, err := c.doRequest(ctx, "POST", "/api/oidc/clients/"+id+"/secrets", body)
	if err != nil {
		return nil, err
	}
	return decodeCreatedSecret(response)
}

// secretCreateBody builds OidcClientSecretCreateDto from opts, after checking
// a caller-chosen value. It returns nil (no body at all) when opts asks for
// nothing, which the server reads as "generate a value, no expiry".
func secretCreateBody(opts *ClientSecretOptions) (any, error) {
	if opts == nil || (opts.Value == "" && opts.ExpiresAt == nil) {
		return nil, nil
	}
	if opts.Value != "" {
		if err := validateSecretValue(opts.Value); err != nil {
			return nil, err
		}
	}
	return struct {
		Secret    string     `json:"secret,omitempty"`
		ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	}{opts.Value, opts.ExpiresAt}, nil
}

// decodeCreatedSecret reads OidcClientSecretCreatedDto. A response without a
// usable (UUID) ID returns no result; one with an ID but no value returns the
// metadata together with ErrCreatedSecretValueMissing. Neither error carries
// any of the response.
func decodeCreatedSecret(response []byte) (*ClientSecret, error) {
	var result struct {
		ClientSecretMetadata
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return nil, undecodableResultError{message: "error unmarshaling secret response; result uncertain, inspect the client's secrets before recovery"}
	}
	if err := ValidateUUID("client secret", result.ID); err != nil {
		if result.Secret == "" {
			return nil, fmt.Errorf("secret creation returned no secret; result uncertain, inspect the client before recovery")
		}
		return nil, fmt.Errorf("secret creation returned no usable secret ID; result uncertain, inspect the client's secrets before recovery: %w", err)
	}
	if result.Secret == "" {
		return &ClientSecret{ClientSecretMetadata: result.ClientSecretMetadata},
			fmt.Errorf("secret creation returned no secret for secret %s; result uncertain, inspect the client before recovery: %w", result.ID, ErrCreatedSecretValueMissing)
	}
	return &ClientSecret{ClientSecretMetadata: result.ClientSecretMetadata, Value: result.Secret}, nil
}

// ListClientSecrets lists an OIDC client's secrets without their values.
// Pocket ID 2.14.0 and later only.
func (c *Client) ListClientSecrets(ctx context.Context, clientID string) ([]ClientSecretMetadata, error) {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, "GET", "/api/oidc/clients/"+id+"/secrets", nil)
	if err != nil {
		return nil, err
	}

	var result []ClientSecretMetadata
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling client secret list")
	}
	return result, nil
}

// DeleteClientSecret revokes one secret of an OIDC client. Pocket ID 2.14.0
// and later only. Like every mutation it is not retried, and its error is
// returned unchanged: a 404 alone does not prove the secret is gone (it may be
// a wrong base URL or a proxy's page), so a caller that needs that answer
// confirms it with ListClientSecrets.
func (c *Client) DeleteClientSecret(ctx context.Context, clientID, secretID string) error {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return err
	}
	secret, err := uuidSegment("client secret", secretID)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", "/api/oidc/clients/"+id+"/secrets/"+secret, nil)
	return err
}

// RevokeClientSecret revokes one secret of an OIDC client and returns nil
// only once the secret is confirmed absent: by Pocket ID's own not-found
// error for the secret or for its client (a client's secrets live in the
// client, so they go with it), or by a list of the client's secrets read
// after the DELETE that no longer contains it. A successful DELETE is
// confirmed the same way. Any other outcome is an error that names the
// secret's ID: the secret may still be valid. The DELETE is never retried;
// the list is a read and follows the read retry rules.
func (c *Client) RevokeClientSecret(ctx context.Context, clientID, secretID string) error {
	deleteErr := c.DeleteClientSecret(ctx, clientID, secretID)
	if errors.Is(deleteErr, ErrInvalidIdentifier) {
		return deleteErr
	}
	if IsNotFound(deleteErr, ResourceClientSecret) || IsNotFound(deleteErr, ResourceOIDCClient) {
		return nil
	}

	remaining, listErr := c.ListClientSecrets(ctx, clientID)
	if listErr != nil {
		if IsNotFound(listErr, ResourceOIDCClient) {
			return nil
		}
		if deleteErr != nil {
			return fmt.Errorf("could not confirm that client secret %s was revoked: the request failed (%w) and the client's secrets could not be listed (%w)", secretID, deleteErr, listErr)
		}
		return fmt.Errorf("could not confirm that client secret %s was revoked: the client's secrets could not be listed afterwards: %w", secretID, listErr)
	}
	for _, secret := range remaining {
		if secret.ID == secretID {
			if deleteErr != nil {
				return fmt.Errorf("client secret %s was not revoked; it is still listed and may still be valid: %w", secretID, deleteErr)
			}
			return fmt.Errorf("client secret %s is still listed after Pocket ID accepted its revocation; it may still be valid", secretID)
		}
	}
	return nil
}
