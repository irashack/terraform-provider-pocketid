//go:build acc
// +build acc

package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// testAccAPIAccessAPIConfig is an API with the permissions read, write and
// admin.
func testAccAPIAccessAPIConfig(rName, uri, adminDescription string) string {
	return testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = %[1]q
  resource = %[2]q
  permissions = {
    read  = { name = "Read" }
    write = { name = "Write" }
    admin = {
      name        = "Admin"
      description = %[3]q
    }
  }
}
`, rName, uri, adminDescription)
}

// testAccAPIAccessServerGrant checks the grant the server holds for the
// client named by the state attribute clientRef, in permission keys; nil
// keys with no access means no grant at all.
func testAccAPIAccessServerGrant(clientRef string, userAccess bool, userKeys []string, clientAccess bool, clientKeys []string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		name, attribute, _ := strings.Cut(clientRef, "#")
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s not in state", name)
		}
		clientID := rs.Primary.Attributes[attribute]
		apiID := s.RootModule().Resources["pocketid_api.test"].Primary.ID
		c, err := testClient()
		if err != nil {
			return err
		}
		return testAccAPIAccessCompare(c, apiID, clientID, userAccess, userKeys, clientAccess, clientKeys)
	}
}

func testAccAPIAccessCompare(c *client.Client, apiID, clientID string, userAccess bool, userKeys []string, clientAccess bool, clientKeys []string) error {
	got, err := c.FindClientAPIGrant(context.Background(), clientID, apiID)
	if err != nil {
		return err
	}
	if got == nil {
		if userAccess || clientAccess {
			return fmt.Errorf("the client holds no grant on the API")
		}
		return nil
	}
	if !userAccess && !clientAccess {
		return fmt.Errorf("the client still holds a grant on the API")
	}
	keyOf := map[string]string{}
	for _, p := range got.API.Permissions {
		keyOf[p.ID] = p.Key
	}
	keys := func(ids []string) string {
		var out []string
		for _, id := range ids {
			out = append(out, keyOf[id])
		}
		slices.Sort(out)
		return strings.Join(out, ",")
	}
	slices.Sort(userKeys)
	slices.Sort(clientKeys)
	if got.UserDelegatedAccess != userAccess || keys(got.UserDelegatedPermissionIDs) != strings.Join(userKeys, ",") {
		return fmt.Errorf("server user-delegated grant: access %t, permissions %s", got.UserDelegatedAccess, keys(got.UserDelegatedPermissionIDs))
	}
	if got.ClientAccess != clientAccess || keys(got.ClientPermissionIDs) != strings.Join(clientKeys, ",") {
		return fmt.Errorf("server client grant: access %t, permissions %s", got.ClientAccess, keys(got.ClientPermissionIDs))
	}
	return nil
}

func TestAccResourceAPIClientAccess_basic(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc-apiaccess")
	uri := testAccResourceAPIResource(acctest.RandString(8))
	name := "pocketid_api_client_access.app"
	clients := fmt.Sprintf(`
resource "pocketid_client" "app" {
  name          = "%[1]s-app"
  callback_urls = ["https://example.com/callback"]
}

resource "pocketid_client" "other" {
  name          = "%[1]s-other"
  callback_urls = ["https://example.com/callback"]
}

resource "pocketid_api_client_access" "other" {
  api_id                     = pocketid_api.test.id
  client_id                  = pocketid_client.other.id
  user_delegated_permissions = ["read"]
}
`, rName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Access left unset follows the permissions.
			{
				Config: testAccAPIAccessAPIConfig(rName, uri, "first") + clients + `
resource "pocketid_api_client_access" "app" {
  api_id                     = pocketid_api.test.id
  client_id                  = pocketid_client.app.id
  user_delegated_permissions = ["read"]
  client_permissions         = ["write"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "user_delegated_access", "true"),
					resource.TestCheckResourceAttr(name, "client_access", "true"),
					resource.TestCheckTypeSetElemAttr(name, "user_delegated_permissions.*", "read"),
					resource.TestCheckTypeSetElemAttr(name, "client_permissions.*", "write"),
					resource.TestCheckResourceAttrPair(name, "api_id", "pocketid_api.test", "id"),
					testAccAPIAccessServerGrant("pocketid_client.app#id", true, []string{"read"}, true, []string{"write"}),
					testAccAPIAccessServerGrant("pocketid_client.other#id", true, []string{"read"}, false, nil),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Changing one pair's grant leaves the other client's grant alone;
			// client access without permissions is kept as scopeless access.
			{
				Config: testAccAPIAccessAPIConfig(rName, uri, "first") + clients + `
resource "pocketid_api_client_access" "app" {
  api_id                     = pocketid_api.test.id
  client_id                  = pocketid_client.app.id
  user_delegated_permissions = ["read", "admin"]
  client_access              = true
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(name, plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pocketid_api_client_access.other", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "client_access", "true"),
					resource.TestCheckResourceAttr(name, "client_permissions.#", "0"),
					testAccAPIAccessServerGrant("pocketid_client.app#id", true, []string{"admin", "read"}, true, nil),
					testAccAPIAccessServerGrant("pocketid_client.other#id", true, []string{"read"}, false, nil),
				),
			},
			// Editing a permission on the API changes nothing about the grants.
			{
				Config: testAccAPIAccessAPIConfig(rName, uri, "second") + clients + `
resource "pocketid_api_client_access" "app" {
  api_id                     = pocketid_api.test.id
  client_id                  = pocketid_client.app.id
  user_delegated_permissions = ["read", "admin"]
  client_access              = true
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pocketid_api.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction(name, plancheck.ResourceActionNoop),
					},
				},
				Check: testAccAPIAccessServerGrant("pocketid_client.app#id", true, []string{"admin", "read"}, true, nil),
			},
			// Removing the resource revokes only that pair.
			{
				Config: testAccAPIAccessAPIConfig(rName, uri, "second") + clients,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccAPIAccessServerGrant("pocketid_client.app#id", false, nil, false, nil),
					testAccAPIAccessServerGrant("pocketid_client.other#id", true, []string{"read"}, false, nil),
				),
			},
		},
	})
}

// The server silently narrows a grant: it drops permission IDs that are not
// the API's own, and client access for a public client. These are the
// behaviors the resource checks for before it writes.
func TestAccResourceAPIClientAccess_serverNarrowsSilently(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)
	rName := acctest.RandomWithPrefix("tf-acc-apiaccess")
	api, err := c.CreateAPI(ctx, &client.APICreateRequest{Name: rName, Resource: testAccResourceAPIResource(acctest.RandString(8))})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteAPI(context.Background(), api.ID) })
	api, err = c.UpdateAPIPermissions(ctx, api.ID, []client.APIPermissionInput{{Key: "read", Name: "Read"}})
	require.NoError(t, err)
	readID := api.Permissions[0].ID
	public, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: rName + "-public", CallbackURLs: []string{"https://example.com/callback"}, IsPublic: true, PkceEnabled: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), public.ID) })
	confidential := testAccResourceAPIClient(t, rName+"-confidential")

	const unknownPermission = "0b6f4f2e-7c1a-4d3e-9f10-2a3b4c5d6e7f"
	applied, err := c.SetAPIClientAccess(ctx, api.ID, confidential, client.APIClientGrant{UserDelegatedPermissionIDs: []string{readID, unknownPermission}})
	require.NoError(t, err)
	assert.Equal(t, []string{readID}, applied.UserDelegatedPermissionIDs, "the unknown permission ID is dropped without an error")
	assert.True(t, applied.UserDelegatedAccess, "a granted permission turns access on")

	applied, err = c.SetAPIClientAccess(ctx, api.ID, public.ID, client.APIClientGrant{ClientAccess: true, ClientPermissionIDs: []string{readID}, UserDelegatedAccess: true})
	require.NoError(t, err)
	assert.False(t, applied.ClientAccess, "a public client gets no client access")
	assert.Empty(t, applied.ClientPermissionIDs)
	assert.True(t, applied.UserDelegatedAccess, "user-delegated access for a public client is kept")

	applied, err = c.SetAPIClientAccess(ctx, api.ID, confidential, client.APIClientGrant{})
	require.NoError(t, err)
	assert.True(t, applied.IsEmpty())
	grant, err := c.FindClientAPIGrant(ctx, confidential, api.ID)
	require.NoError(t, err)
	assert.Nil(t, grant, "an empty grant stores nothing")
}

// A grant the server would narrow is refused before anything is written.
func TestAccResourceAPIClientAccess_refusedBeforeWrite(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc-apiaccess")
	uri := testAccResourceAPIResource(acctest.RandString(8))
	base := testAccAPIAccessAPIConfig(rName, uri, "first") + fmt.Sprintf(`
resource "pocketid_client" "public" {
  name          = "%[1]s-public"
  callback_urls = ["https://example.com/callback"]
  is_public     = true
}

resource "pocketid_client" "app" {
  name          = "%[1]s-app"
  callback_urls = ["https://example.com/callback"]
}
`, rName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: base + `
resource "pocketid_api_client_access" "test" {
  api_id                     = pocketid_api.test.id
  client_id                  = pocketid_client.app.id
  user_delegated_permissions = ["read", "delete"]
}
`,
				ExpectError: regexp.MustCompile(`has\s+no\s+permission\s+"delete"`),
			},
			{
				Config: base + `
resource "pocketid_api_client_access" "test" {
  api_id        = pocketid_api.test.id
  client_id     = pocketid_client.public.id
  client_access = true
}
`,
				ExpectError: regexp.MustCompile(`is\s+public`),
			},
			{
				Config: base,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccAPIAccessServerGrant("pocketid_client.app#id", false, nil, false, nil),
					testAccAPIAccessServerGrant("pocketid_client.public#id", false, nil, false, nil),
				),
			},
		},
	})
}

// Plan-time validation refuses grants the server would store differently.
func TestAccResourceAPIClientAccess_planTimeValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		body, want string
	}{
		"access off with permissions": {`user_delegated_access = false
  user_delegated_permissions = ["read"]`, `Access cannot be off with permissions`},
		"empty grant":     {`client_access = false`, `Grant gives nothing`},
		"reserved key":    {`client_permissions = ["openid"]`, `reserved by Pocket ID`},
		"not an API ID":   {`user_delegated_access = true`, `must be an API ID`},
		"CIMD client URL": {`user_delegated_access = true`, `must be an OIDC client ID`},
	} {
		t.Run(name, func(t *testing.T) {
			apiID, clientID := "00000000-0000-4000-8000-000000000001", "app"
			if name == "not an API ID" {
				apiID = "inventory"
			}
			if name == "CIMD client URL" {
				clientID = "https://mcp.example.com/client.json"
			}
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: testAccProviderConfig() + fmt.Sprintf(`
resource "pocketid_api_client_access" "test" {
  api_id    = %q
  client_id = %q
  %s
}
`, apiID, clientID, tc.body),
						PlanOnly:    true,
						ExpectError: regexp.MustCompile(strings.ReplaceAll(tc.want, " ", `\s+`)),
					},
				},
			})
		})
	}
}

// A grant revoked outside Terraform is dropped on refresh and written again;
// a deleted client takes its grant with it, and refresh drops the resource
// without an error.
func TestAccResourceAPIClientAccess_outOfBandChanges(t *testing.T) {
	testAccPreCheck(t)
	rName := acctest.RandomWithPrefix("tf-acc-apiaccess")
	uri := testAccResourceAPIResource(acctest.RandString(8))
	clientID := testAccResourceAPIClient(t, rName)
	var apiID string
	grantConfig := testAccAPIAccessAPIConfig(rName, uri, "first") + fmt.Sprintf(`
resource "pocketid_api_client_access" "test" {
  api_id                = pocketid_api.test.id
  client_id             = %q
  user_delegated_access = true
}
`, clientID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: grantConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						apiID = s.RootModule().Resources["pocketid_api.test"].Primary.ID
						return nil
					},
					resource.TestCheckResourceAttr("pocketid_api_client_access.test", "user_delegated_permissions.#", "0"),
				),
			},
			{
				PreConfig: func() {
					c, err := testClient()
					require.NoError(t, err)
					require.NoError(t, c.RemoveAPIClientAccess(context.Background(), apiID, clientID))
				},
				Config: grantConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pocketid_api_client_access.test", plancheck.ResourceActionCreate)},
				},
				Check: func(*terraform.State) error {
					c, err := testClient()
					if err != nil {
						return err
					}
					return testAccAPIAccessCompare(c, apiID, clientID, true, nil, false, nil)
				},
			},
			{
				PreConfig: func() {
					c, err := testClient()
					require.NoError(t, err)
					require.NoError(t, c.DeleteClient(context.Background(), clientID))
				},
				Config: testAccAPIAccessAPIConfig(rName, uri, "first"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// testAccGrantShape describes the fields of a decoded grant object: each of
// the four grant fields as "bool", "array", "null" or "missing".
func testAccGrantShape(t *testing.T, raw map[string]json.RawMessage) map[string]string {
	t.Helper()
	shape := map[string]string{}
	for _, field := range []string{"userDelegatedAccess", "clientAccess", "userDelegatedPermissionIds", "clientPermissionIds"} {
		value, ok := raw[field]
		switch {
		case !ok:
			shape[field] = "missing"
		case string(value) == "null":
			shape[field] = "null"
		case string(value) == "true" || string(value) == "false":
			shape[field] = "bool"
		case strings.HasPrefix(string(value), "["):
			shape[field] = "array"
		default:
			shape[field] = "other"
		}
	}
	return shape
}

// The grant responses carry all four grant fields in every case the provider
// relies on, and the client accepts them: access flags are always booleans
// and the permission lists are arrays (the server's DTOs have no omitempty,
// so a nil list could be null; an empty grant is answered with empty arrays).
// The client refuses a response without these fields, since it would read as
// no grant at all, so this is the server behavior that validation depends on.
func TestAccAPIClientGrant_responseShape(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c, err := testClient()
	require.NoError(t, err)
	rName := acctest.RandomWithPrefix("tf-acc-apigrant")
	api, err := c.CreateAPI(ctx, &client.APICreateRequest{Name: rName, Resource: testAccResourceAPIResource(acctest.RandString(8))})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteAPI(context.Background(), api.ID) })
	api, err = c.UpdateAPIPermissions(ctx, api.ID, []client.APIPermissionInput{{Key: "read", Name: "Read"}})
	require.NoError(t, err)
	readID := api.Permissions[0].ID
	confidential := testAccResourceAPIClient(t, rName+"-confidential")
	public, err := c.CreateClient(ctx, &client.OIDCClientCreateRequest{Name: rName + "-public", CallbackURLs: []string{"https://example.com/callback"}, IsPublic: true, PkceEnabled: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteClient(context.Background(), public.ID) })

	put := func(clientID string, grant client.APIClientGrant) map[string]string {
		var raw map[string]json.RawMessage
		status, err := testAccAPI("PUT", "/api/apis/"+api.ID+"/clients/"+clientID, grant, &raw)
		require.NoError(t, err)
		require.Equal(t, 200, status)
		return testAccGrantShape(t, raw)
	}
	// The cases run in this order: the first ones overwrite the confidential
	// client's grant, so the list is only asserted after a grant is set
	// explicitly below.
	for _, tc := range []struct {
		name     string
		clientID string
		grant    client.APIClientGrant
	}{
		{"empty grant", confidential, client.APIClientGrant{UserDelegatedPermissionIDs: []string{}, ClientPermissionIDs: []string{}}},
		{"access without permissions", confidential, client.APIClientGrant{UserDelegatedAccess: true, ClientAccess: true, UserDelegatedPermissionIDs: []string{}, ClientPermissionIDs: []string{}}},
		{"permissions", confidential, client.APIClientGrant{UserDelegatedPermissionIDs: []string{readID}, ClientPermissionIDs: []string{readID}}},
		{"public client", public.ID, client.APIClientGrant{ClientAccess: true, UserDelegatedPermissionIDs: []string{}, ClientPermissionIDs: []string{readID}}},
	} {
		name := tc.name
		shape := put(tc.clientID, tc.grant)
		t.Logf("PUT %s: %v", name, shape)
		assert.Equal(t, "bool", shape["userDelegatedAccess"], name)
		assert.Equal(t, "bool", shape["clientAccess"], name)
		for _, field := range []string{"userDelegatedPermissionIds", "clientPermissionIds"} {
			assert.Contains(t, []string{"array", "null"}, shape[field], "%s: %s", name, field)
		}
		// The client decodes the same response without treating it as unread.
		applied, err := c.SetAPIClientAccess(ctx, api.ID, tc.clientID, tc.grant)
		require.NoError(t, err, name)
		// A public client's client access is dropped, leaving nothing here.
		assert.Equal(t, name == "empty grant" || name == "public client", applied.IsEmpty(), name)
	}

	// Whatever the cases above left behind, set a grant with one user
	// permission and an empty client list, then read the client's list.
	_, err = c.SetAPIClientAccess(ctx, api.ID, confidential, client.APIClientGrant{UserDelegatedPermissionIDs: []string{readID}, ClientPermissionIDs: []string{}})
	require.NoError(t, err)
	var list []map[string]json.RawMessage
	status, err := testAccAPI("GET", "/api/api-access/"+confidential+"/apis", nil, &list)
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Len(t, list, 1)
	shape := testAccGrantShape(t, list[0])
	t.Logf("GET list: %v", shape)
	assert.Equal(t, "bool", shape["userDelegatedAccess"])
	assert.Equal(t, "bool", shape["clientAccess"])
	assert.Equal(t, "array", shape["userDelegatedPermissionIds"])
	assert.Equal(t, "array", shape["clientPermissionIds"])
	assert.JSONEq(t, fmt.Sprintf("[%q]", readID), string(list[0]["userDelegatedPermissionIds"]))
	assert.JSONEq(t, "[]", string(list[0]["clientPermissionIds"]))
	var apiObject map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(list[0]["api"], &apiObject))
	assert.Contains(t, apiObject, "id")
	grant, err := c.FindClientAPIGrant(ctx, confidential, api.ID)
	require.NoError(t, err)
	require.NotNil(t, grant)
}
