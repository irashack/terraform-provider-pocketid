package datasources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
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
