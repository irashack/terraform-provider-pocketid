//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccLosingProxy forwards every request to the fixture. After the fixture
// has answered a request for which lose returns true, the caller gets a 502
// instead, as when a proxy gives up on a request the server has committed.
// It returns the proxy's base URL.
func testAccLosingProxy(t *testing.T, lose func(*http.Request) bool) string {
	t.Helper()
	target, err := url.Parse(os.Getenv("POCKETID_BASE_URL"))
	if err != nil {
		t.Fatal("POCKETID_BASE_URL is not a URL")
	}
	forward := httputil.NewSingleHostReverseProxy(target)
	director := forward.Director
	forward.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lose(r) {
			forward.ServeHTTP(httptest.NewRecorder(), r)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	return proxy.URL
}

// testAccHoldingProxy forwards every request to the fixture, except that the
// first request for which hold returns true is received but not forwarded: the
// caller gets a 502, as when a proxy gives up on a request the server goes on to
// commit. release then sends the held request to the fixture, as its late
// commit, and lets later requests through. It returns the proxy's base URL.
func testAccHoldingProxy(t *testing.T, hold func(*http.Request) bool) (string, func()) {
	t.Helper()
	target, err := url.Parse(os.Getenv("POCKETID_BASE_URL"))
	if err != nil {
		t.Fatal("POCKETID_BASE_URL is not a URL")
	}
	forward := httputil.NewSingleHostReverseProxy(target)
	director := forward.Director
	forward.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
	}
	var (
		mu      sync.Mutex
		held    bool
		pending *http.Request
		body    []byte
	)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if !held && hold(r) {
			held = true
			body, _ = io.ReadAll(r.Body)
			pending = r.Clone(r.Context())
			mu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		mu.Unlock()
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	release := func() {
		mu.Lock()
		defer mu.Unlock()
		if pending == nil {
			t.Fatal("no request was held")
		}
		req, err := http.NewRequest(pending.Method, target.String()+pending.URL.Path, bytes.NewReader(body))
		if err != nil {
			t.Fatal("could not rebuild the held request")
		}
		req.Header = pending.Header.Clone()
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("could not send the held request")
		}
		_ = response.Body.Close()
		if response.StatusCode >= 300 {
			t.Fatalf("the held request was refused: HTTP %d", response.StatusCode)
		}
		pending = nil
	}
	return proxy.URL, release
}

// testAccUnresolvedUserConfig is a provider block pointing at proxyURL and a
// user with a chosen ID, or a removed block that forgets the user without
// deleting it.
func testAccUnresolvedUserConfig(proxyURL, id string, withResource bool) string {
	body := fmt.Sprintf(`
resource "pocketid_user" "test" {
  id       = %q
  username = "unresolved-user"
  email    = "unresolved-user@example.com"
}
`, id)
	if !withResource {
		body = `
removed {
  from = pocketid_user.test
  lifecycle {
    destroy = false
  }
}
`
	}
	return fmt.Sprintf("provider \"pocketid\" {\n  base_url = %q\n}\n", proxyURL) + body
}

// testAccUserOnServer fails unless the user exists on the fixture.
func testAccUserOnServer(id string) error {
	status, err := testAccAPI("GET", "/api/users/"+id, nil, nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("the user is not on the server: HTTP %d", status)
	}
	return nil
}

// A create with a chosen ID whose response is lost after the server committed
// leaves the ID in state as an unresolved creation and the resource tainted.
// Terraform then plans the replacement of the tainted resource from a null
// prior state, and the destroy half must not delete a user this apply may not
// own: the replacement apply is refused and the user stays on the server.
func TestAccResourceUser_unresolvedCreationIsNotReplaced(t *testing.T) {
	const id = "7b2e4c91-5a3d-4f6e-8a1b-9c0d1e2f3a4b"
	proxyURL := testAccLosingProxy(t, func(r *http.Request) bool {
		return r.Method == http.MethodPost && r.URL.Path == "/api/users"
	})
	t.Cleanup(func() { _, _ = testAccAPI("DELETE", "/api/users/"+id, nil, nil) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccUnresolvedUserConfig(proxyURL, id, true),
				ExpectError: regexp.MustCompile(`User creation result uncertain`),
			},
			{
				// The refresh keeps the condition in state; the tainted
				// resource still plans a replacement.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user.test", "id", id),
					resource.TestCheckResourceAttr("pocketid_user.test", "unresolved_creation", "true"),
				),
			},
			{
				// Terraform plans a replacement of the tainted resource; its
				// destroy half is refused, and so the user is never deleted.
				Config: testAccUnresolvedUserConfig(proxyURL, id, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pocketid_user.test", plancheck.ResourceActionReplace)},
				},
				ExpectError: regexp.MustCompile(`User creation unresolved`),
			},
			{
				PreConfig: func() {
					if err := testAccUserOnServer(id); err != nil {
						t.Fatal(err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user.test", "unresolved_creation", "true"),
					func(*terraform.State) error { return testAccUserOnServer(id) },
				),
			},
			{
				// Reconciled by the operator: the resource leaves state
				// without a delete, and the user stays.
				Config: testAccUnresolvedUserConfig(proxyURL, id, false),
				Check:  func(*terraform.State) error { return testAccUserOnServer(id) },
			},
		},
	})
}

// A create that has not committed when the next refresh runs is not forgotten
// by that refresh: Pocket ID's "user not found" would otherwise remove the
// resource, and the create then lands untracked. The resource stays in state
// until it is reconciled, and when the create does land it is still protected.
func TestAccResourceUser_refreshBeforeLateCommitKeepsResource(t *testing.T) {
	const id = "8c3f5da2-6b4e-4a7f-9b2c-0d1e2f3a4b5c"
	proxyURL, release := testAccHoldingProxy(t, func(r *http.Request) bool {
		return r.Method == http.MethodPost && r.URL.Path == "/api/users"
	})
	t.Cleanup(func() { _, _ = testAccAPI("DELETE", "/api/users/"+id, nil, nil) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The POST is received but not applied, and answered 502.
				Config:      testAccUnresolvedUserConfig(proxyURL, id, true),
				ExpectError: regexp.MustCompile(`User creation result uncertain`),
			},
			{
				// The server has no such user yet: the resource stays.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user.test", "id", id),
					resource.TestCheckResourceAttr("pocketid_user.test", "unresolved_creation", "true"),
				),
			},
			{
				// The create lands after the refresh; the user it made is
				// tracked, and the replacement of the tainted resource cannot
				// delete it.
				PreConfig:          release,
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user.test", "unresolved_creation", "true"),
					func(*terraform.State) error { return testAccUserOnServer(id) },
				),
			},
			{
				Config:      testAccUnresolvedUserConfig(proxyURL, id, true),
				ExpectError: regexp.MustCompile(`User creation unresolved`),
			},
			{
				Config: testAccUnresolvedUserConfig(proxyURL, id, false),
				Check:  func(*terraform.State) error { return testAccUserOnServer(id) },
			},
		},
	})
}
