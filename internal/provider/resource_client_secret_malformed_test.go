//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

// A create that Pocket ID carried out but whose answer cannot be used (here:
// the whole generated value in the non-secret `prefix` field) keeps only the
// new secret's ID. Terraform taints the resource, and the next apply revokes
// exactly that secret and creates a replacement, leaving the client's other
// secrets alone and the value out of state throughout.
//
// A proxy in front of the fixture damages the answer to one secret creation;
// the server itself really created the secret, so the second apply works on a
// real orphan.
func TestAccResourceClientSecret_malformedCreateIsReplaced(t *testing.T) {
	name := "tf-acc-secret-malformed-" + acctest.RandString(6)
	upstream, err := url.Parse(os.Getenv("POCKETID_BASE_URL"))
	require.NoError(t, err)

	var damage atomic.Bool
	var damaged atomic.Int32
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Header.Del("Accept-Encoding") // the proxy's own transport decodes what the server compresses
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.Request.Method != http.MethodPost || !strings.HasSuffix(resp.Request.URL.Path, "/secrets") ||
				resp.StatusCode >= 300 || !damage.CompareAndSwap(true, false) {
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			var created map[string]any
			if err := json.Unmarshal(body, &created); err != nil {
				return err
			}
			created["prefix"] = created["secret"]
			if body, err = json.Marshal(created); err != nil {
				return err
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			damaged.Add(1)
			return nil
		},
	}
	front := httptest.NewServer(proxy)
	defer front.Close()

	config := func(withSecret bool) string {
		secret := ""
		if withSecret {
			secret = `
resource "pocketid_client_secret" "app" {
  client_id = pocketid_client.app.id
}
`
		}
		return fmt.Sprintf(`
provider "pocketid" {
  base_url = %q
}

resource "pocketid_client" "app" {
  name          = %q
  callback_urls = ["https://example.invalid/callback"]
}
%s`, front.URL, name, secret)
	}
	serverIDs := func(clientID string) ([]string, error) {
		listed, err := testAccClientSecretResList(clientID)
		ids := make([]string, 0, len(listed))
		for id := range listed {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return ids, err
	}
	var clientID, orphan string
	var before []string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The client alone: whatever secrets it holds are not this resource's.
				Config: config(false),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccClientSecretResCapture("pocketid_client.app", "id", &clientID),
					func(*terraform.State) error {
						var err error
						before, err = serverIDs(clientID)
						return err
					},
				),
			},
			{
				PreConfig:   func() { damage.Store(true) },
				Config:      config(true),
				ExpectError: regexp.MustCompile(`Client\s+secret\s+response\s+unusable`),
			},
			{
				// The server holds the secret the damaged answer named, and
				// state holds only its ID, tainted: the plan replaces it.
				PreConfig: func() {
					require.Equal(t, int32(1), damaged.Load(), "the proxy damaged exactly one answer")
					now, err := serverIDs(clientID)
					require.NoError(t, err)
					var extra []string
					for _, id := range now {
						if !slices.Contains(before, id) {
							extra = append(extra, id)
						}
					}
					require.Len(t, extra, 1, "the server created exactly one secret that the damaged answer named")
					orphan = extra[0]
				},
				Config: config(true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccClientSecretAddr, plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction("pocketid_client.app", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccClientSecretResChanged(&orphan),
					testAccClientSecretResAbsent(&clientID, &orphan),
					testAccCheckSensitiveMatches(testAccClientSecretAddr, "secret", testAccSecretFormat),
					testAccCheckSensitiveMatches(testAccClientSecretAddr, "prefix", testAccPrefixFormat),
					testAccClientSecretResOnServer(),
					func(s *terraform.State) error {
						attrs, err := testAccClientSecretResAttrs(s, testAccClientSecretAddr)
						if err != nil {
							return err
						}
						now, err := serverIDs(clientID)
						if err != nil {
							return err
						}
						want := append(append([]string{}, before...), attrs["id"])
						sort.Strings(want)
						if strings.Join(now, " ") != strings.Join(want, " ") {
							return fmt.Errorf("the client holds secrets %v, want its own %v plus the replacement %s", now, before, attrs["id"])
						}
						return nil
					},
				),
			},
		},
	})
}
