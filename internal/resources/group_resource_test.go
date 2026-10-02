package resources_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/resources"
)

func TestNewGroupResource(t *testing.T) {
	r := resources.NewGroupResource()
	assert.NotNil(t, r)
}

func TestGroupResource_Metadata(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupResource()

	req := resource.MetadataRequest{
		ProviderTypeName: "pocketid",
	}
	resp := &resource.MetadataResponse{}

	r.Metadata(ctx, req, resp)

	assert.Equal(t, "pocketid_group", resp.TypeName)
}

func TestGroupResource_Schema(t *testing.T) {
	ctx := context.Background()
	schemaRequest := resource.SchemaRequest{}
	schemaResponse := &resource.SchemaResponse{}

	resources.NewGroupResource().Schema(ctx, schemaRequest, schemaResponse)

	if schemaResponse.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %+v", schemaResponse.Diagnostics)
	}

	// Verify required attributes
	nameAttr, ok := schemaResponse.Schema.Attributes["name"]
	assert.True(t, ok, "name attribute should exist")
	assert.True(t, nameAttr.IsRequired(), "name should be required")

	friendlyNameAttr, ok := schemaResponse.Schema.Attributes["friendly_name"]
	assert.True(t, ok, "friendly_name attribute should exist")
	assert.True(t, friendlyNameAttr.IsRequired(), "friendly_name should be required")

	// Verify computed attributes
	idAttr, ok := schemaResponse.Schema.Attributes["id"]
	assert.True(t, ok, "id attribute should exist")
	assert.True(t, idAttr.IsComputed(), "id should be computed")
}

// Test Schema validation for Group Resource
func TestGroupResource_SchemaValidation(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupResource()

	req := resource.SchemaRequest{}
	resp := &resource.SchemaResponse{}
	r.Schema(ctx, req, resp)

	assert.False(t, resp.Diagnostics.HasError())

	// Verify all expected attributes exist
	attrs := resp.Schema.Attributes

	// Required attributes
	nameAttr, ok := attrs["name"].(schema.StringAttribute)
	assert.True(t, ok, "name should be StringAttribute")
	assert.True(t, nameAttr.Required, "name should be required")

	friendlyNameAttr, ok := attrs["friendly_name"].(schema.StringAttribute)
	assert.True(t, ok, "friendly_name should be StringAttribute")
	assert.True(t, friendlyNameAttr.Required, "friendly_name should be required")

	// Computed attributes
	idAttr, ok := attrs["id"].(schema.StringAttribute)
	assert.True(t, ok, "id should be StringAttribute")
	assert.True(t, idAttr.Computed, "id should be computed")
}

func TestGroupResource_Configure(t *testing.T) {
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
			providerData:  struct{}{},
			expectError:   true,
			errorContains: "Expected *client.Client",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := resources.NewGroupResource()

			configurable, ok := r.(resource.ResourceWithConfigure)
			require.True(t, ok, "Group resource should implement ResourceWithConfigure")

			req := resource.ConfigureRequest{
				ProviderData: tc.providerData,
			}
			resp := &resource.ConfigureResponse{}

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

// Test Group Resource API errors
func TestGroupResource_APIErrors(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error": "Group name already exists"}`))
	})

	testClient := createMockServer(t, handler)

	// Test that the client returns an error
	_, err := testClient.CreateUserGroup(context.Background(), &client.UserGroupCreateRequest{
		Name:         "test-group",
		FriendlyName: "Test Group",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 409")
}

// Configure rejects provider data that is not a *client.Client.
func TestGroupResource_ConfigureErrorHandling(t *testing.T) {
	ctx := context.Background()
	configurable := resources.NewGroupResource().(resource.ResourceWithConfigure)

	req := resource.ConfigureRequest{
		ProviderData: "invalid-type",
	}
	resp := &resource.ConfigureResponse{}

	configurable.Configure(ctx, req, resp)

	assert.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "Expected *client.Client")
}

func TestGroupResource_PlanModifiers(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupResource()
	req := resource.SchemaRequest{}
	resp := &resource.SchemaResponse{}
	r.Schema(ctx, req, resp)

	// Check that computed attributes have UseStateForUnknown plan modifier
	idAttr, _ := resp.Schema.Attributes["id"].(schema.StringAttribute)
	assert.NotNil(t, idAttr.PlanModifiers, "id should have plan modifiers")
}

func TestGroupResource_Interfaces(t *testing.T) {
	res := resources.NewGroupResource()

	_, ok := res.(resource.ResourceWithConfigure)
	assert.True(t, ok, "GroupResource should implement resource.ResourceWithConfigure")

	_, ok = res.(resource.ResourceWithImportState)
	assert.True(t, ok, "GroupResource should implement resource.ResourceWithImportState")
}
