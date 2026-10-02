package resources_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Helper to create a mock server
func createMockServer(t *testing.T, handler http.HandlerFunc) *client.Client {
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	testClient, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	return testClient
}
