package datasources_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

type logoPresetsTestModel struct {
	Search  types.String `tfsdk:"search"`
	Presets []struct {
		Name        types.String `tfsdk:"name"`
		Reference   types.String `tfsdk:"reference"`
		LogoURL     types.String `tfsdk:"logo_url"`
		DarkLogoURL types.String `tfsdk:"dark_logo_url"`
	} `tfsdk:"presets"`
}

func logoPresetsRead(t *testing.T, status int, body string, search *string) (*datasource.ReadResponse, []string) {
	t.Helper()
	var searches []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/oidc/logo-presets", r.URL.Path)
		searches = append(searches, r.URL.RawQuery)
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)

	ctx := context.Background()
	ds := datasources.NewLogoPresetsDataSource()
	configure := &datasource.ConfigureResponse{}
	ds.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{ProviderData: c}, configure)
	require.False(t, configure.Diagnostics.HasError())
	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	objectType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	var searchValue tftypes.Value
	if search == nil {
		searchValue = tftypes.NewValue(tftypes.String, nil)
	} else {
		searchValue = tftypes.NewValue(tftypes.String, *search)
	}
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objectType, map[string]tftypes.Value{
		"search":  searchValue,
		"presets": tftypes.NewValue(objectType.AttributeTypes["presets"], nil),
	})}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objectType, nil)}}
	ds.Read(ctx, datasource.ReadRequest{Config: config}, resp)
	return resp, searches
}

func TestLogoPresetsDataSource_Read(t *testing.T) {
	search := "home assistant"
	resp, searches := logoPresetsRead(t, http.StatusOK, `[{"name":"Home Assistant","reference":"home-assistant",`+
		`"logoUrl":"https://cdn.example/svg/home-assistant.svg","darkLogoUrl":null},`+
		`{"name":"Jellyfin","reference":"jellyfin","logoUrl":"https://cdn.example/svg/jellyfin.svg","darkLogoUrl":"https://cdn.example/svg/jellyfin-light.svg"}]`, &search)
	require.False(t, resp.Diagnostics.HasError(), resp.Diagnostics)
	assert.Equal(t, []string{"search=home+assistant"}, searches)
	var got logoPresetsTestModel
	require.False(t, resp.State.Get(context.Background(), &got).HasError())
	require.Len(t, got.Presets, 2)
	assert.Equal(t, "home-assistant", got.Presets[0].Reference.ValueString())
	assert.True(t, got.Presets[0].DarkLogoURL.IsNull())
	assert.Equal(t, "https://cdn.example/svg/jellyfin-light.svg", got.Presets[1].DarkLogoURL.ValueString())

	_, searches = logoPresetsRead(t, http.StatusOK, `[]`, nil)
	assert.Equal(t, []string{""}, searches, "no search parameter without a search")
}

func TestLogoPresetsDataSource_Failures(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"disabled":     {http.StatusForbidden, `{"error":"Logo presets are disabled","code":"logo_presets_disabled"}`, "Icon library turned off"},
		"unavailable":  {http.StatusBadGateway, `{"error":"Logo presets could not be loaded","code":"logo_presets_unavailable"}`, "Icon library unavailable"},
		"older server": {http.StatusNotFound, `{"error":"API endpoint not found"}`, "No icon library"},
		"key echoed":   {http.StatusOK, `[{"name":"synthetic-token","reference":"x","logoUrl":"https://cdn.example/x.svg"}]`, "Unable to Search Logo Presets"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, _ := logoPresetsRead(t, tc.status, tc.body, nil)
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, tc.want, resp.Diagnostics[0].Summary())
		})
	}
}
