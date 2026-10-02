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

// Test Client Data Source
func TestClientDataSource_Metadata(t *testing.T) {
	ctx := context.Background()
	ds := datasources.NewClientDataSource()

	req := datasource.MetadataRequest{
		ProviderTypeName: "pocketid",
	}
	resp := &datasource.MetadataResponse{}

	ds.Metadata(ctx, req, resp)

	assert.Equal(t, "pocketid_client", resp.TypeName)
}

func TestClientDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	ds := datasources.NewClientDataSource()

	req := datasource.SchemaRequest{}
	resp := &datasource.SchemaResponse{}

	ds.Schema(ctx, req, resp)

	assert.False(t, resp.Diagnostics.HasError())
	assert.NotNil(t, resp.Schema)
	assert.NotEmpty(t, resp.Schema.Description)

	// Verify attributes exist
	expectedAttributes := []string{
		"id", "name", "has_logo", "callback_urls", "logout_callback_urls",
		"is_public", "pkce_enabled", "allowed_user_groups", "requires_reauthentication",
		"launch_url",
	}

	for _, attr := range expectedAttributes {
		_, ok := resp.Schema.Attributes[attr]
		assert.True(t, ok, "Schema should have %s attribute", attr)
	}

	// Check attribute types
	idAttr, ok := resp.Schema.Attributes["id"].(schema.StringAttribute)
	assert.True(t, ok)
	assert.True(t, idAttr.Required)

	callbackUrlsAttr, ok := resp.Schema.Attributes["callback_urls"].(schema.ListAttribute)
	assert.True(t, ok)
	assert.True(t, callbackUrlsAttr.Computed)
}

func TestClientDataSource_Configure(t *testing.T) {
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
			providerData:  "invalid",
			expectError:   true,
			errorContains: "Expected *client.Client",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ds := datasources.NewClientDataSource()

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
