package datasources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	apiDataSourceTestFirst  = "00000000-0000-4000-8000-000000000001"
	apiDataSourceTestSecond = "00000000-0000-4000-8000-000000000002"
)

// apiDataSourceTestServer serves two APIs over two pages; the second API's
// identifier extends the first one's, so only an exact match finds the
// first.
func apiDataSourceTestServer(t *testing.T, getStatus int, getBody string) (*client.Client, *[]string) {
	t.Helper()
	var calls []string
	first := fmt.Sprintf(`{"id":%q,"name":"Inventory","resource":"https://inventory.example","createdAt":"2026-01-01T00:00:00Z","allowCimdClients":true,"permissions":[{"id":"00000000-0000-4000-8000-0000000000b1","key":"read","name":"Read","description":"Read items","allowedForCimdClients":true},{"id":"00000000-0000-4000-8000-0000000000b2","key":"write","name":"Write","allowedForCimdClients":false}]}`, apiDataSourceTestFirst)
	second := fmt.Sprintf(`{"id":%q,"name":"Inventory v2","resource":"https://inventory.example/v2","createdAt":"2026-01-02T00:00:00Z","allowCimdClients":false,"permissions":[]}`, apiDataSourceTestSecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/apis":
			page := r.URL.Query().Get("pagination[page]")
			item := first
			if page == "2" {
				item = second
			}
			_, _ = fmt.Fprintf(w, `{"data":[%s],"pagination":{"totalPages":2,"totalItems":2,"currentPage":%s,"itemsPerPage":100}}`, item, page)
		case "/api/apis/" + apiDataSourceTestFirst:
			w.WriteHeader(getStatus)
			if getBody == "" {
				getBody = first
			}
			_, _ = fmt.Fprint(w, getBody)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c, &calls
}

func apiDataSourceTestRead(t *testing.T, c *client.Client, id, resourceID types.String) (datasource.ReadResponse, apiDataSourceModel) {
	t.Helper()
	ctx := context.Background()
	var sr datasource.SchemaResponse
	(&apiDataSource{}).Schema(ctx, datasource.SchemaRequest{}, &sr)
	cfgModel := apiDataSourceModel{
		ID: id, Resource: resourceID, Name: types.StringNull(), CreatedAt: types.StringNull(), AllowCIMDClients: types.BoolNull(),
		Permissions: types.MapNull(types.ObjectType{AttrTypes: apiDataSourcePermissionAttrTypes}),
	}
	state := tfsdk.State{Schema: sr.Schema}
	require.False(t, state.Set(ctx, &cfgModel).HasError())
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&apiDataSource{client: c}).Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: state.Raw}}, &resp)
	var got apiDataSourceModel
	if !resp.Diagnostics.HasError() {
		require.False(t, resp.State.Get(ctx, &got).HasError())
	}
	return resp, got
}

func TestAPIDataSource_ByID(t *testing.T) {
	c, calls := apiDataSourceTestServer(t, 200, "")
	resp, got := apiDataSourceTestRead(t, c, types.StringValue(apiDataSourceTestFirst), types.StringNull())
	require.False(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{"GET /api/apis/" + apiDataSourceTestFirst}, *calls)
	assert.Equal(t, "https://inventory.example", got.Resource.ValueString())
	assert.True(t, got.AllowCIMDClients.ValueBool())
	var perms map[string]struct {
		ID                    types.String `tfsdk:"id"`
		Name                  types.String `tfsdk:"name"`
		Description           types.String `tfsdk:"description"`
		AllowedForCIMDClients types.Bool   `tfsdk:"allowed_for_cimd_clients"`
	}
	require.False(t, got.Permissions.ElementsAs(context.Background(), &perms, false).HasError())
	require.Len(t, perms, 2)
	assert.Equal(t, "Read items", perms["read"].Description.ValueString())
	assert.True(t, perms["write"].Description.IsNull())
	assert.True(t, perms["read"].AllowedForCIMDClients.ValueBool())
}

// Only Pocket ID's own not-found error is reported as a missing API.
func TestAPIDataSource_ByIDNotFound(t *testing.T) {
	for name, tc := range map[string]struct {
		body, want string
	}{
		"api not found": {`{"error":"API not found","code":"not_found","details":{"resource":"API"}}`, "API not found"},
		"bare 404":      {`{}`, "Unable to read API"},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := apiDataSourceTestServer(t, 404, tc.body)
			resp, _ := apiDataSourceTestRead(t, c, types.StringValue(apiDataSourceTestFirst), types.StringNull())
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, tc.want, resp.Diagnostics[0].Summary())
		})
	}
}

// A lookup by resource reads every page and matches the identifier exactly.
func TestAPIDataSource_ByResource(t *testing.T) {
	c, calls := apiDataSourceTestServer(t, 200, "")
	resp, got := apiDataSourceTestRead(t, c, types.StringNull(), types.StringValue("https://inventory.example/v2"))
	require.False(t, resp.Diagnostics.HasError())
	assert.Equal(t, apiDataSourceTestSecond, got.ID.ValueString())
	assert.Len(t, *calls, 2)

	resp, got = apiDataSourceTestRead(t, c, types.StringNull(), types.StringValue("https://inventory.example"))
	require.False(t, resp.Diagnostics.HasError())
	assert.Equal(t, apiDataSourceTestFirst, got.ID.ValueString())

	resp, _ = apiDataSourceTestRead(t, c, types.StringNull(), types.StringValue("https://inventory.exampl"))
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, "API not found", resp.Diagnostics[0].Summary())
}

func TestAPIsDataSource_ListsEveryPageInOrder(t *testing.T) {
	ctx := context.Background()
	c, _ := apiDataSourceTestServer(t, 200, "")
	var sr datasource.SchemaResponse
	(&apisDataSource{}).Schema(ctx, datasource.SchemaRequest{}, &sr)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&apisDataSource{client: c}).Read(ctx, datasource.ReadRequest{}, &resp)
	require.False(t, resp.Diagnostics.HasError())
	var got apisDataSourceModel
	require.False(t, resp.State.Get(ctx, &got).HasError())
	require.Len(t, got.APIs, 2)
	assert.Equal(t, apiDataSourceTestFirst, got.APIs[0].ID.ValueString())
	assert.Equal(t, apiDataSourceTestSecond, got.APIs[1].ID.ValueString())
	assert.Empty(t, got.APIs[1].Permissions.Elements())
}

// A server that reflects the API key as an identifier cannot put it into
// data source state or a diagnostic: the read fails with fixed text.
func TestAPIDataSources_ReflectedKeyNeverReachesState(t *testing.T) {
	const key = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	api := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"name":"Inventory","resource":"https://inventory.example","createdAt":"2026-01-01T00:00:00Z","allowCimdClients":false,"permissions":[{"id":%q,"key":"read","name":"Read","allowedForCimdClients":false}]}`, id, key)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/apis" {
			_, _ = fmt.Fprintf(w, `{"data":[%s],"pagination":{"totalPages":1,"totalItems":1,"currentPage":1,"itemsPerPage":100}}`, api(apiDataSourceTestFirst))
			return
		}
		_, _ = fmt.Fprint(w, api(apiDataSourceTestFirst))
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, key, false, 5)
	require.NoError(t, err)

	text := func(diags diag.Diagnostics) string {
		var parts []string
		for _, d := range diags.Errors() {
			parts = append(parts, d.Summary()+": "+d.Detail())
		}
		return strings.Join(parts, "\n")
	}
	ctx := context.Background()

	resp, got := apiDataSourceTestRead(t, c, types.StringValue(apiDataSourceTestFirst), types.StringNull())
	require.True(t, resp.Diagnostics.HasError())
	assert.NotContains(t, text(resp.Diagnostics), key)
	assert.True(t, got.Permissions.IsNull() || len(got.Permissions.Elements()) == 0)

	resp, _ = apiDataSourceTestRead(t, c, types.StringNull(), types.StringValue("https://inventory.example"))
	require.True(t, resp.Diagnostics.HasError())
	assert.NotContains(t, text(resp.Diagnostics), key)

	var sr datasource.SchemaResponse
	(&apisDataSource{}).Schema(ctx, datasource.SchemaRequest{}, &sr)
	list := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&apisDataSource{client: c}).Read(ctx, datasource.ReadRequest{}, &list)
	require.True(t, list.Diagnostics.HasError())
	assert.NotContains(t, text(list.Diagnostics), key)
	assert.True(t, list.State.Raw.IsNull(), "nothing reaches state")
}

// Every string field of an API that reaches state, the creation time included,
// is refused when it carries the API key, by both data sources.
func TestAPIDataSources_ReflectedTextNeverReachesState(t *testing.T) {
	const key = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	const permissionID = "00000000-0000-4000-8000-0000000000b1"
	for name, fields := range map[string][5]string{
		// name, resource, createdAt, permission key, permission name
		"creation time":  {"Inventory", "https://inventory.example", "created " + key, "read", "Read"},
		"name":           {"Inv " + key, "https://inventory.example", "2026-01-01T00:00:00Z", "read", "Read"},
		"resource":       {"Inventory", "https://inventory.example/" + key, "2026-01-01T00:00:00Z", "read", "Read"},
		"permission key": {"Inventory", "https://inventory.example", "2026-01-01T00:00:00Z", key, "Read"},
		"permission":     {"Inventory", "https://inventory.example", "2026-01-01T00:00:00Z", "read", "Read " + key},
	} {
		t.Run(name, func(t *testing.T) {
			api := fmt.Sprintf(`{"id":%q,"name":%q,"resource":%q,"createdAt":%q,"allowCimdClients":false,"permissions":[{"id":%q,"key":%q,"name":%q,"allowedForCimdClients":false}]}`,
				apiDataSourceTestFirst, fields[0], fields[1], fields[2], permissionID, fields[3], fields[4])
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/apis" {
					_, _ = fmt.Fprintf(w, `{"data":[%s],"pagination":{"totalPages":1,"totalItems":1,"currentPage":1,"itemsPerPage":100}}`, api)
					return
				}
				_, _ = fmt.Fprint(w, api)
			}))
			t.Cleanup(server.Close)
			c, err := client.NewClient(server.URL, key, false, 5)
			require.NoError(t, err)
			ctx := context.Background()
			text := func(diags diag.Diagnostics) string {
				var parts []string
				for _, d := range diags.Errors() {
					parts = append(parts, d.Summary()+": "+d.Detail())
				}
				return strings.Join(parts, "\n")
			}

			resp, _ := apiDataSourceTestRead(t, c, types.StringValue(apiDataSourceTestFirst), types.StringNull())
			require.True(t, resp.Diagnostics.HasError(), "by ID")
			assert.NotContains(t, text(resp.Diagnostics), key)
			assert.True(t, resp.State.Raw.IsNull())

			resp, _ = apiDataSourceTestRead(t, c, types.StringNull(), types.StringValue("https://inventory.example"))
			require.True(t, resp.Diagnostics.HasError(), "by resource")
			assert.NotContains(t, text(resp.Diagnostics), key)
			assert.True(t, resp.State.Raw.IsNull())

			var sr datasource.SchemaResponse
			(&apisDataSource{}).Schema(ctx, datasource.SchemaRequest{}, &sr)
			list := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
			(&apisDataSource{client: c}).Read(ctx, datasource.ReadRequest{}, &list)
			require.True(t, list.Diagnostics.HasError(), "list")
			assert.NotContains(t, text(list.Diagnostics), key)
			assert.True(t, list.State.Raw.IsNull())
		})
	}
}

func TestAPIDataSource_InputValidators(t *testing.T) {
	ctx := context.Background()
	check := func(v validator.String, value string) string {
		resp := validator.StringResponse{}
		v.ValidateString(ctx, validator.StringRequest{Path: path.Root("x"), ConfigValue: types.StringValue(value)}, &resp)
		var parts []string
		for _, d := range resp.Diagnostics {
			parts = append(parts, d.Detail())
		}
		return strings.Join(parts, " ")
	}
	assert.Empty(t, check(apiDataSourceResourceValidator{}, "https://inventory.example"))
	assert.Contains(t, check(apiDataSourceResourceValidator{}, "https://inventory.example/"), "must not end with a slash")
	assert.Empty(t, check(apiDataSourceIDValidator{}, apiDataSourceTestFirst))
	assert.Contains(t, check(apiDataSourceIDValidator{}, "inventory"), "UUID")
}
