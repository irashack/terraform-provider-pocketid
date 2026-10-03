package datasources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestSignupTokensDataSource_Metadata(t *testing.T) {
	resp := &datasource.MetadataResponse{}
	datasources.NewSignupTokensDataSource().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "pocketid"}, resp)
	assert.Equal(t, "pocketid_signup_tokens", resp.TypeName)
}

func TestSignupTokensDataSource_SchemaMarksTheTokenSensitive(t *testing.T) {
	resp := &datasource.SchemaResponse{}
	datasources.NewSignupTokensDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Schema.MarkdownDescription, "live secret")

	list, ok := resp.Schema.Attributes["tokens"].(schema.ListNestedAttribute)
	require.True(t, ok)
	assert.True(t, list.Computed)
	token, ok := list.NestedObject.Attributes["token"].(schema.StringAttribute)
	require.True(t, ok)
	assert.True(t, token.Sensitive, "Pocket ID's list returns each token's value, so it must be sensitive")
	for _, name := range []string{"id", "expires_at", "created_at", "usage_limit", "usage_count", "user_group_ids"} {
		attribute, ok := list.NestedObject.Attributes[name]
		require.True(t, ok, name)
		assert.False(t, attribute.IsSensitive(), name)
		assert.True(t, attribute.IsComputed(), name)
	}
}

func TestSignupTokensDataSource_Configure(t *testing.T) {
	ds, ok := datasources.NewSignupTokensDataSource().(datasource.DataSourceWithConfigure)
	require.True(t, ok)

	resp := &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: nil}, resp)
	assert.False(t, resp.Diagnostics.HasError())

	resp = &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "invalid"}, resp)
	assert.True(t, resp.Diagnostics.HasError())
}

func signupTokensRead(t *testing.T, c *client.Client) *datasource.ReadResponse {
	t.Helper()
	ds := datasources.NewSignupTokensDataSource()
	configure := &datasource.ConfigureResponse{}
	ds.(datasource.DataSourceWithConfigure).Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, configure)
	require.False(t, configure.Diagnostics.HasError())

	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(context.Background(), datasource.SchemaRequest{}, schemaResp)
	objectType := schemaResp.Schema.Type().TerraformType(context.Background())
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objectType, nil)}}
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objectType, map[string]tftypes.Value{
		"tokens": tftypes.NewValue(objectType.(tftypes.Object).AttributeTypes["tokens"], nil),
	})}
	ds.Read(context.Background(), datasource.ReadRequest{Config: config}, resp)
	return resp
}

func TestSignupTokensDataSource_ListsEveryTokenAcrossPages(t *testing.T) {
	const total = 130
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("pagination[page]"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("pagination[limit]"))
		var data []map[string]any
		for i := (page - 1) * limit; i < total && i < page*limit; i++ {
			data = append(data, map[string]any{
				"id": fmt.Sprintf("00000000-0000-4000-8000-%012d", i), "token": fmt.Sprintf("token-%d", i),
				"expiresAt": "2026-10-03T10:00:00Z", "createdAt": "2026-10-02T10:00:00Z", "usageLimit": 2, "usageCount": i % 2,
				"userGroups": []map[string]any{{"id": "66666666-6666-4666-8666-666666666666", "name": "staff", "friendlyName": "Staff"}},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       data,
			"pagination": map[string]any{"totalPages": 2, "totalItems": total, "currentPage": page, "itemsPerPage": limit},
		})
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	resp := signupTokensRead(t, c)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	var list types.List
	diags := resp.State.GetAttribute(context.Background(), path.Root("tokens"), &list)
	require.False(t, diags.HasError(), "%v", diags)
	items := list.Elements()
	require.Len(t, items, total)
	last := items[total-1].(types.Object).Attributes()
	assert.Equal(t, types.StringValue("token-129"), last["token"])
	second := items[1].(types.Object).Attributes()
	assert.Equal(t, types.Int64Value(1), second["usage_count"])
	groups, ok := second["user_group_ids"].(types.Set)
	require.True(t, ok)
	assert.Len(t, groups.Elements(), 1)
}

func TestSignupTokensDataSource_AnEmptyListIsEmptyNotNull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       []any{},
			"pagination": map[string]any{"totalPages": 1, "totalItems": 0, "currentPage": 1, "itemsPerPage": 100},
		})
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	resp := signupTokensRead(t, c)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var list types.List
	require.False(t, resp.State.GetAttribute(context.Background(), path.Root("tokens"), &list).HasError())
	assert.False(t, list.IsNull())
	assert.Empty(t, list.Elements())
}

func TestSignupTokensDataSource_AFailureIsAnErrorWithoutTheResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"leaked-token-value","code":"missing_permission"}`))
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	resp := signupTokensRead(t, c)
	require.True(t, resp.Diagnostics.HasError())
	for _, d := range resp.Diagnostics {
		assert.NotContains(t, d.Summary()+d.Detail(), "leaked-token-value")
	}
}
