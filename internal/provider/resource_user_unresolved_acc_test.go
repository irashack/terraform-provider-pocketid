//go:build acc
// +build acc

package provider_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
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
	config := func(withResource bool) string {
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
	onServer := func() error {
		status, err := testAccAPI("GET", "/api/users/"+id, nil, nil)
		if err != nil {
			return err
		}
		if status != 200 {
			return fmt.Errorf("the user is not on the server: HTTP %d", status)
		}
		return nil
	}
	t.Cleanup(func() { _, _ = testAccAPI("DELETE", "/api/users/"+id, nil, nil) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config(true),
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
				Config: config(true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pocketid_user.test", plancheck.ResourceActionReplace)},
				},
				ExpectError: regexp.MustCompile(`User creation unresolved`),
			},
			{
				PreConfig: func() {
					if err := onServer(); err != nil {
						t.Fatal(err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user.test", "unresolved_creation", "true"),
					func(*terraform.State) error { return onServer() },
				),
			},
			{
				// Reconciled by the operator: the resource leaves state
				// without a delete, and the user stays.
				Config: config(false),
				Check:  func(*terraform.State) error { return onServer() },
			},
		},
	})
}
