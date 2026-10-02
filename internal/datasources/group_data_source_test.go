package datasources_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

// Test Group Data Source
func TestGroupDataSource_Metadata(t *testing.T) {
	ctx := context.Background()
	ds := datasources.NewGroupDataSource()

	req := datasource.MetadataRequest{
		ProviderTypeName: "pocketid",
	}
	resp := &datasource.MetadataResponse{}

	ds.Metadata(ctx, req, resp)

	assert.Equal(t, "pocketid_group", resp.TypeName)
}

func TestGroupDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	ds := datasources.NewGroupDataSource()

	req := datasource.SchemaRequest{}
	resp := &datasource.SchemaResponse{}

	ds.Schema(ctx, req, resp)

	assert.False(t, resp.Diagnostics.HasError())
	assert.NotNil(t, resp.Schema)
	assert.NotEmpty(t, resp.Schema.Description)

	// Verify attributes exist
	expectedAttributes := []string{
		"id", "name", "friendly_name",
	}

	for _, attr := range expectedAttributes {
		_, ok := resp.Schema.Attributes[attr]
		assert.True(t, ok, "Schema should have %s attribute", attr)
	}

	// Check specific attribute types and properties
	idAttr, ok := resp.Schema.Attributes["id"].(schema.StringAttribute)
	assert.True(t, ok)
	assert.True(t, idAttr.Optional)
	assert.True(t, idAttr.Computed)

	nameAttr, ok := resp.Schema.Attributes["name"].(schema.StringAttribute)
	assert.True(t, ok)
	assert.True(t, nameAttr.Optional)
	assert.True(t, nameAttr.Computed)

	friendlyNameAttr, ok := resp.Schema.Attributes["friendly_name"].(schema.StringAttribute)
	assert.True(t, ok)
	assert.True(t, friendlyNameAttr.Computed)
}

func TestGroupDataSource_Configure(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name          string
		providerData  interface{}
		expectError   bool
		errorContains string
	}{
		{
			name:         "valid_client",
			providerData: &client.Client{},
			expectError:  false,
		},
		{
			name:         "nil_provider_data",
			providerData: nil,
			expectError:  false,
		},
		{
			name:          "invalid_provider_data_type",
			providerData:  struct{ Name string }{Name: "invalid"},
			expectError:   true,
			errorContains: "Expected *client.Client",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ds := datasources.NewGroupDataSource()

			configurable, ok := ds.(datasource.DataSourceWithConfigure)
			require.True(t, ok)

			req := datasource.ConfigureRequest{
				ProviderData: tc.providerData,
			}
			resp := &datasource.ConfigureResponse{}

			configurable.Configure(ctx, req, resp)

			if tc.expectError {
				assert.True(t, resp.Diagnostics.HasError())
				assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), tc.errorContains)
			} else {
				assert.False(t, resp.Diagnostics.HasError())
			}
		})
	}
}
