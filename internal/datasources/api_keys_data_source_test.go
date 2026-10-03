package datasources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

func TestAPIKeysDataSource_Metadata(t *testing.T) {
	resp := &datasource.MetadataResponse{}
	datasources.NewAPIKeysDataSource().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "pocketid"}, resp)
	assert.Equal(t, "pocketid_api_keys", resp.TypeName)
}

func TestAPIKeysDataSource_SchemaHasNoKeyValue(t *testing.T) {
	resp := &datasource.SchemaResponse{}
	datasources.NewAPIKeysDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())
	assert.NotEmpty(t, resp.Schema.Description)

	list, ok := resp.Schema.Attributes["keys"].(schema.ListNestedAttribute)
	require.True(t, ok)
	assert.True(t, list.Computed)
	assert.ElementsMatch(t, []string{"id", "name", "description", "expires_at", "last_used_at", "created_at"}, apiKeysAttributeNames(list.NestedObject.Attributes),
		"the data source must offer no attribute that could carry a key value")
	for name, attribute := range list.NestedObject.Attributes {
		assert.True(t, attribute.IsComputed(), name)
	}
}

func apiKeysAttributeNames(attributes map[string]schema.Attribute) []string {
	var names []string
	for name := range attributes {
		names = append(names, name)
	}
	return names
}

func TestAPIKeysDataSource_Configure(t *testing.T) {
	ds, ok := datasources.NewAPIKeysDataSource().(datasource.DataSourceWithConfigure)
	require.True(t, ok)

	resp := &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: nil}, resp)
	assert.False(t, resp.Diagnostics.HasError())

	resp = &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: 42}, resp)
	assert.True(t, resp.Diagnostics.HasError())
}

func apiKeysRead(t *testing.T, c *client.Client) *datasource.ReadResponse {
	t.Helper()
	ds := datasources.NewAPIKeysDataSource()
	configure := &datasource.ConfigureResponse{}
	ds.(datasource.DataSourceWithConfigure).Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, configure)
	require.False(t, configure.Diagnostics.HasError())

	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(context.Background(), datasource.SchemaRequest{}, schemaResp)
	objectType := schemaResp.Schema.Type().TerraformType(context.Background())
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objectType, nil)}}
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objectType, map[string]tftypes.Value{
		"keys": tftypes.NewValue(objectType.(tftypes.Object).AttributeTypes["keys"], nil),
	})}
	ds.Read(context.Background(), datasource.ReadRequest{Config: config}, resp)
	return resp
}

func TestAPIKeysDataSource_ReadsTheKeysWithoutAnyKeyValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "00000000-0000-4000-8000-000000000001", "name": "terraform", "description": "management key",
					"expiresAt": "2026-12-01T00:00:00Z", "lastUsedAt": "2026-10-02T09:00:00Z", "createdAt": "2026-10-01T00:00:00Z",
					"key": "must-not-reach-state"},
				{"id": "00000000-0000-4000-8000-000000000002", "name": "unused", "description": nil,
					"expiresAt": "2027-01-01T00:00:00Z", "lastUsedAt": nil, "createdAt": "2026-10-01T00:00:00Z"},
			},
			"pagination": map[string]any{"totalPages": 1, "totalItems": 2, "currentPage": 1, "itemsPerPage": 100},
		})
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	resp := apiKeysRead(t, c)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	var list types.List
	require.False(t, resp.State.GetAttribute(context.Background(), path.Root("keys"), &list).HasError())
	items := list.Elements()
	require.Len(t, items, 2)
	first := items[0].(types.Object).Attributes()
	assert.Equal(t, types.StringValue("terraform"), first["name"])
	assert.Equal(t, types.StringValue("management key"), first["description"])
	assert.Equal(t, types.StringValue("2026-12-01T00:00:00Z"), first["expires_at"])
	assert.Equal(t, types.StringValue("2026-10-02T09:00:00Z"), first["last_used_at"])
	second := items[1].(types.Object).Attributes()
	assert.Equal(t, types.StringNull(), second["description"])
	assert.Equal(t, types.StringNull(), second["last_used_at"])

	assert.NotContains(t, list.String(), "must-not-reach-state")
}

func TestAPIKeysDataSource_AnEmptyListIsEmptyNotNull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       []any{},
			"pagination": map[string]any{"totalPages": 1, "totalItems": 0, "currentPage": 1, "itemsPerPage": 100},
		})
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	resp := apiKeysRead(t, c)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var list types.List
	require.False(t, resp.State.GetAttribute(context.Background(), path.Root("keys"), &list).HasError())
	assert.False(t, list.IsNull())
	assert.Empty(t, list.Elements())
}

func TestAPIKeysDataSource_AFailureIsAnErrorWithoutTheResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"leaked-body","code":"not_signed_in"}`))
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	resp := apiKeysRead(t, c)
	require.True(t, resp.Diagnostics.HasError())
	for _, d := range resp.Diagnostics {
		assert.NotContains(t, d.Summary()+d.Detail(), "leaked-body")
	}
}
