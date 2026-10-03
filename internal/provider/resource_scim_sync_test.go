//go:build acc
// +build acc

package provider_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

// fakeScimEndpoint is a SCIM server the fixture's Pocket ID can reach from its
// container, so a sync has somewhere to push users to. It keeps what it was
// given and counts the list requests that start each sync.
type fakeScimEndpoint struct {
	mu        sync.Mutex
	token     string
	users     []map[string]any
	groups    []map[string]any
	userLists int
	badBearer int
	failLists bool
	nextID    int
	url       string
}

// newFakeScimEndpoint listens on the loopback interface only, for the length
// of the test. The fixture's container reaches the host as
// host.docker.internal, and under the OrbStack runtime this machine uses that
// name connects to services bound to the host's loopback address, so nothing
// here is reachable from the network. (A runtime that does not forward to
// loopback needs a wider bind; the test then fails to sync rather than
// exposing anything.)
func newFakeScimEndpoint(t *testing.T, token string) *fakeScimEndpoint {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	fake := &fakeScimEndpoint{token: token}
	server := &http.Server{Handler: http.HandlerFunc(fake.serve)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	fake.url = fmt.Sprintf("http://host.docker.internal:%d/scim", listener.Addr().(*net.TCPAddr).Port)
	return fake
}

func (f *fakeScimEndpoint) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.badBearer++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/scim+json")
	collection := &f.users
	resourceName := "Users"
	if strings.HasPrefix(r.URL.Path, "/scim/Groups") {
		collection = &f.groups
		resourceName = "Groups"
	}
	switch r.Method {
	case http.MethodGet:
		if resourceName == "Users" {
			f.userLists++
			if f.failLists {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Resources": *collection, "totalResults": len(*collection), "startIndex": 1, "itemsPerPage": len(*collection),
		})
	case http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.nextID++
		body["id"] = fmt.Sprintf("remote-%d", f.nextID)
		body["meta"] = map[string]any{"resourceType": strings.TrimSuffix(resourceName, "s"), "created": "2026-01-01T00:00:00Z", "lastModified": "2026-01-01T00:00:00Z"}
		*collection = append(*collection, body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(body)
	case http.MethodPut:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(body)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeScimEndpoint) snapshot() (userLists, users, badBearer int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.userLists, len(f.users), f.badBearer
}

func (f *fakeScimEndpoint) setFailLists(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failLists = fail
}

func testAccResourceScimSyncConfig(clientName, endpoint, token, triggers string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %[1]q
  callback_urls = ["https://example.com/callback"]
}

resource "pocketid_scim_service_provider" "test" {
  client_id = pocketid_client.test.id
  endpoint  = %[2]q
  token     = %[3]q
}

resource "pocketid_scim_sync" "test" {
  service_provider_id = pocketid_scim_service_provider.test.id
  triggers            = %[4]s
}
`, clientName, endpoint, token, triggers)
}

// The sync runs inside the apply: when it returns, Pocket ID has pushed its
// users to the SCIM endpoint (with the configured bearer token) and recorded
// the time, and a failure of the endpoint fails the apply once.
func TestAccResourceScimSync_runsTheSyncDuringTheApply(t *testing.T) {
	syncName := "pocketid_scim_sync.test"
	clientName := acctest.RandomWithPrefix("tf-acc-scim-sync")
	fake := newFakeScimEndpoint(t, "sync-bearer-token")
	var firstLists int

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceScimSyncConfig(clientName, fake.url, "sync-bearer-token", `{ run = "1" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(syncName, "synced_at"),
					resource.TestCheckResourceAttrPair(syncName, "id", "pocketid_scim_service_provider.test", "id"),
					func(s *terraform.State) error {
						lists, users, bad := fake.snapshot()
						if lists != 1 {
							return fmt.Errorf("expected one sync to list the endpoint's users, saw %d", lists)
						}
						if users < 1 {
							return fmt.Errorf("the sync pushed no user to the endpoint")
						}
						if bad != 0 {
							return fmt.Errorf("the endpoint saw %d requests without the configured token", bad)
						}
						firstLists = lists
						// The sync is synchronous: Pocket ID has already recorded it.
						var view struct {
							LastSyncedAt *string `json:"lastSyncedAt"`
						}
						clientID := s.RootModule().Resources["pocketid_scim_service_provider.test"].Primary.Attributes["client_id"]
						status, err := testAccAPI("GET", "/api/oidc/clients/"+clientID+"/scim-service-provider", nil, &view)
						if err != nil || status != http.StatusOK {
							return fmt.Errorf("reading the SCIM service provider failed (HTTP %d)", status)
						}
						if view.LastSyncedAt == nil {
							return fmt.Errorf("the apply returned before Pocket ID recorded the sync")
						}
						return nil
					},
				),
			},
			{
				// Unchanged triggers: no new sync.
				Config:   testAccResourceScimSyncConfig(clientName, fake.url, "sync-bearer-token", `{ run = "1" }`),
				PlanOnly: true,
			},
			{
				// New triggers: the resource is replaced and syncs again.
				Config: testAccResourceScimSyncConfig(clientName, fake.url, "sync-bearer-token", `{ run = "2" }`),
				Check: func(s *terraform.State) error {
					if lists, _, _ := fake.snapshot(); lists != firstLists+1 {
						return fmt.Errorf("expected exactly one more sync, saw %d list requests in total", lists)
					}
					return nil
				},
			},
			{
				// The endpoint fails: the apply fails after one sync attempt.
				PreConfig:   func() { fake.setFailLists(true) },
				Config:      testAccResourceScimSyncConfig(clientName, fake.url, "sync-bearer-token", `{ run = "3" }`),
				ExpectError: regexp.MustCompile(`Error syncing SCIM service provider`),
			},
			{
				// Recover so the destroy at the end of the test is clean.
				PreConfig: func() {
					fake.setFailLists(false)
					if lists, _, _ := fake.snapshot(); lists != firstLists+2 {
						t.Errorf("the failed apply must have made exactly one sync attempt, saw %d list requests in total", lists)
					}
				},
				Config: testAccResourceScimSyncConfig(clientName, fake.url, "sync-bearer-token", `{ run = "3" }`),
			},
		},
	})
}
