package datasources_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

// Test that all data sources have descriptions. It covers every registered
// data source, so a new one is checked without editing this file.
func TestDataSources_HaveDescriptions(t *testing.T) {
	ctx := context.Background()

	for _, factory := range datasources.All() {
		ds := factory()
		t.Run(getDataSourceName(t, ds), func(t *testing.T) {
			req := datasource.SchemaRequest{}
			resp := &datasource.SchemaResponse{}

			ds.Schema(ctx, req, resp)

			require.False(t, resp.Diagnostics.HasError())
			assert.NotEmpty(t, resp.Schema.Description, "Data source should have a description")
		})
	}
}

// Helper function to get data source name
func getDataSourceName(t *testing.T, ds datasource.DataSource) string {
	ctx := context.Background()
	req := datasource.MetadataRequest{
		ProviderTypeName: "pocketid",
	}
	resp := &datasource.MetadataResponse{}
	ds.Metadata(ctx, req, resp)
	return resp.TypeName
}
