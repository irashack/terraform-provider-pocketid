//go:build acc
// +build acc

package datasources_test

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"golang.org/x/mod/semver"

	"github.com/irashack/terraform-provider-pocketid/internal/provider"
)

// testAccProtoV6ProviderFactories are used to instantiate a provider during
// acceptance testing. The factory function will be invoked for every Terraform
// CLI command executed to create a provider server to which the CLI can
// reattach.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"pocketid": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func testAccPreCheck(t *testing.T) {
	// Check for required environment variables
	if v := os.Getenv("POCKETID_BASE_URL"); v == "" {
		t.Fatal("POCKETID_BASE_URL must be set for acceptance tests")
	}

	if v := os.Getenv("POCKETID_API_TOKEN"); v == "" {
		t.Fatal("POCKETID_API_TOKEN must be set for acceptance tests")
	}
}

// testAccServerAtLeast reports whether the fixture runs at least version. A
// missing or malformed POCKETID_TEST_VERSION fails the test rather than
// silently selecting the older server's checks.
func testAccServerAtLeast(t *testing.T, version string) bool {
	t.Helper()
	running := "v" + os.Getenv("POCKETID_TEST_VERSION")
	if !semver.IsValid(running) {
		t.Fatalf("POCKETID_TEST_VERSION must be the fixture's Pocket ID version, got %q", os.Getenv("POCKETID_TEST_VERSION"))
	}
	return semver.Compare(running, "v"+version) >= 0
}
