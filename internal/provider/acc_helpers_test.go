//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"golang.org/x/mod/semver"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func testClient() (*client.Client, error) {
	return client.NewClient(
		os.Getenv("POCKETID_BASE_URL"),
		os.Getenv("POCKETID_API_TOKEN"),
		false,
		30,
	)
}

// testAccAPI calls the fixture's API directly. Response bodies are decoded
// into out and never printed.
func testAccAPI(method, path string, body, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, os.Getenv("POCKETID_BASE_URL")+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-API-KEY", os.Getenv("POCKETID_API_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s failed", method, path)
	}
	defer func() { _ = response.Body.Close() }()
	if out != nil && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			return response.StatusCode, fmt.Errorf("%s %s returned an unreadable body", method, path)
		}
	}
	return response.StatusCode, nil
}

// testAccServerAtLeast selects version-specific assertions. A missing or
// malformed POCKETID_TEST_VERSION fails the test: comparing it would read as
// "older" and silently skip the newer server's checks.
func testAccServerAtLeast(t *testing.T, version string) bool {
	t.Helper()
	running := "v" + os.Getenv("POCKETID_TEST_VERSION")
	if !semver.IsValid(running) {
		t.Fatalf("POCKETID_TEST_VERSION must be the fixture's Pocket ID version, got %q", os.Getenv("POCKETID_TEST_VERSION"))
	}
	return semver.Compare(running, "v"+version) >= 0
}
