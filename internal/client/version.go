package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

type CurrentVersion struct {
	Current string `json:"currentVersion"`
}

// errVersionReflectsKey is fixed text: it never includes the version string.
var errVersionReflectsKey = errors.New("the Pocket ID version response contains the API key this provider sent; refusing to use it")

func (c *Client) GetCurrentVersion(ctx context.Context) (string, error) {
	body, err := c.doRequest(ctx, "GET", "/api/version/current", nil)
	if err != nil {
		// The /version/current endpoint was added in v2.3.0. If it doesn't exist, return an empty string.
		var status *HTTPError
		if errors.As(err, &status) && status.MissingEndpoint {
			return "", nil
		}

		return "", err
	}

	var version CurrentVersion
	if err := json.Unmarshal(body, &version); err != nil {
		return "", fmt.Errorf("invalid Pocket ID version response")
	}
	// currentVersion is the only field of the answer (environment handler,
	// v2.14.0 to v2.17.0), and callers keep it: the version data source
	// stores it in non-sensitive state. A valid semantic version can carry
	// arbitrary text in its pre-release or build metadata, so a server could
	// return the API key it received inside one (a static key can be all
	// digits); such an answer is refused before anything else looks at it.
	if c.reflectsKey(version.Current) {
		return "", errVersionReflectsKey
	}

	normalized := "v" + strings.TrimPrefix(version.Current, "v")
	if !semver.IsValid(normalized) {
		return "", fmt.Errorf("invalid or missing Pocket ID currentVersion; refusing to select a secret endpoint")
	}
	return strings.TrimPrefix(normalized, "v"), nil
}

// VersionAtLeast reports whether the server runs at least the given version.
// A server without the version endpoint (before v2.3.0) is older than any
// version this is asked about.
func (c *Client) VersionAtLeast(ctx context.Context, minimum string) (bool, error) {
	version, err := c.GetCurrentVersion(ctx)
	if err != nil {
		return false, err
	}
	if version == "" {
		return false, nil
	}
	return semver.Compare("v"+version, "v"+strings.TrimPrefix(minimum, "v")) >= 0, nil
}

// CheckSecretAPI validates compatibility before a confidential client is created.
func (c *Client) CheckSecretAPI(ctx context.Context) error {
	_, err := c.GetCurrentVersion(ctx)
	return err
}
