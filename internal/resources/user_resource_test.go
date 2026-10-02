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

func TestNewUserResource(t *testing.T) {
	r := resources.NewUserResource()
	assert.NotNil(t, r)
}

func TestUserResource_Metadata(t *testing.T) {
	ctx := context.Background()
	r := resources.NewUserResource()

	req := resource.MetadataRequest{
		ProviderTypeName: "pocketid",
	}
	resp := &resource.MetadataResponse{}

	r.Metadata(ctx, req, resp)

	assert.Equal(t, "pocketid_user", resp.TypeName)
}

func TestUserResource_Schema(t *testing.T) {
	ctx := context.Background()
	schemaRequest := resource.SchemaRequest{}
	schemaResponse := &resource.SchemaResponse{}

	resources.NewUserResource().Schema(ctx, schemaRequest, schemaResponse)

	if schemaResponse.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %+v", schemaResponse.Diagnostics)
	}

	// Verify required attributes
	usernameAttr, ok := schemaResponse.Schema.Attributes["username"]
	assert.True(t, ok, "username attribute should exist")
	assert.True(t, usernameAttr.IsRequired(), "username should be required")

	emailAttr, ok := schemaResponse.Schema.Attributes["email"]
	assert.True(t, ok, "email attribute should exist")
	assert.True(t, emailAttr.IsRequired(), "email should be required")

	// Verify computed attributes
	idAttr, ok := schemaResponse.Schema.Attributes["id"]
	assert.True(t, ok, "id attribute should exist")
	assert.True(t, idAttr.IsComputed(), "id should be computed")

	// Verify optional attributes with defaults
	isAdminAttr, ok := schemaResponse.Schema.Attributes["is_admin"]
	assert.True(t, ok, "is_admin attribute should exist")
	assert.True(t, isAdminAttr.IsOptional(), "is_admin should be optional")
	assert.True(t, isAdminAttr.IsComputed(), "is_admin should be computed")

	disabledAttr, ok := schemaResponse.Schema.Attributes["disabled"]
	assert.True(t, ok, "disabled attribute should exist")
	assert.True(t, disabledAttr.IsOptional(), "disabled should be optional")
	assert.True(t, disabledAttr.IsComputed(), "disabled should be computed")

	emailVerifiedAttr, ok := schemaResponse.Schema.Attributes["email_verified"]
	assert.True(t, ok, "email_verified attribute should exist")
	assert.True(t, emailVerifiedAttr.IsOptional(), "email_verified should be optional")
	assert.True(t, emailVerifiedAttr.IsComputed(), "email_verified should be computed")
}

// Test Schema validation for User Resource
func TestUserResource_SchemaValidation(t *testing.T) {
	ctx := context.Background()
	r := resources.NewUserResource()

	req := resource.SchemaRequest{}
	resp := &resource.SchemaResponse{}
	r.Schema(ctx, req, resp)

	assert.False(t, resp.Diagnostics.HasError())

	// Verify all expected attributes exist
	attrs := resp.Schema.Attributes

	// Required attributes
	usernameAttr, ok := attrs["username"].(schema.StringAttribute)
	assert.True(t, ok, "username should be StringAttribute")
	assert.True(t, usernameAttr.Required, "username should be required")

	emailAttr, ok := attrs["email"].(schema.StringAttribute)
	assert.True(t, ok, "email should be StringAttribute")
	assert.True(t, emailAttr.Required, "email should be required")

	// Computed attributes
	idAttr, ok := attrs["id"].(schema.StringAttribute)
	assert.True(t, ok, "id should be StringAttribute")
	assert.True(t, idAttr.Computed, "id should be computed")

	// Optional attributes
	firstNameAttr, ok := attrs["first_name"].(schema.StringAttribute)
	assert.True(t, ok, "first_name should be StringAttribute")
	assert.True(t, firstNameAttr.Optional, "first_name should be optional")

	lastNameAttr, ok := attrs["last_name"].(schema.StringAttribute)
	assert.True(t, ok, "last_name should be StringAttribute")
	assert.True(t, lastNameAttr.Optional, "last_name should be optional")

	// Optional with defaults
	isAdminAttr, ok := attrs["is_admin"].(schema.BoolAttribute)
	assert.True(t, ok, "is_admin should be BoolAttribute")
	assert.True(t, isAdminAttr.Optional, "is_admin should be optional")
	assert.True(t, isAdminAttr.Computed, "is_admin should be computed")

	disabledAttr, ok := attrs["disabled"].(schema.BoolAttribute)
	assert.True(t, ok, "disabled should be BoolAttribute")
	assert.True(t, disabledAttr.Optional, "disabled should be optional")
	assert.True(t, disabledAttr.Computed, "disabled should be computed")

	// Groups attribute
	groupsAttr, ok := attrs["groups"].(schema.SetAttribute)
	assert.True(t, ok, "groups should be SetAttribute")
	assert.True(t, groupsAttr.Optional, "groups should be optional")
}

func TestUserResource_Configure(t *testing.T) {
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
			providerData:  []string{"invalid"},
			expectError:   true,
			errorContains: "Expected *client.Client",
		},
		{
			name:          "invalid_provider_data_map",
			providerData:  map[string]string{"key": "value"},
			expectError:   true,
			errorContains: "Expected *client.Client",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := resources.NewUserResource()

			configurable, ok := r.(resource.ResourceWithConfigure)
			require.True(t, ok, "User resource should implement ResourceWithConfigure")

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

// Test User Resource API errors
func TestUserResource_APIErrors(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error": "Username already exists"}`))
	})

	testClient := createMockServer(t, handler)

	// Test that the client returns an error
	_, err := testClient.CreateUser(context.Background(), &client.UserCreateRequest{
		Username: "testuser",
		Email:    "test@example.com",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 409")
}

// Configure rejects provider data that is not a *client.Client.
func TestUserResource_ConfigureErrorHandling(t *testing.T) {
	ctx := context.Background()
	configurable := resources.NewUserResource().(resource.ResourceWithConfigure)

	req := resource.ConfigureRequest{
		ProviderData: "invalid-type",
	}
	resp := &resource.ConfigureResponse{}

	configurable.Configure(ctx, req, resp)

	assert.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "Expected *client.Client")
}

func TestUserResource_PlanModifiers(t *testing.T) {
	ctx := context.Background()
	r := resources.NewUserResource()
	req := resource.SchemaRequest{}
	resp := &resource.SchemaResponse{}
	r.Schema(ctx, req, resp)

	// Check that computed attributes have UseStateForUnknown plan modifier
	idAttr, _ := resp.Schema.Attributes["id"].(schema.StringAttribute)
	assert.NotNil(t, idAttr.PlanModifiers, "id should have plan modifiers")
}

func TestUserResource_Interfaces(t *testing.T) {
	res := resources.NewUserResource()

	_, ok := res.(resource.ResourceWithConfigure)
	assert.True(t, ok, "UserResource should implement resource.ResourceWithConfigure")

	_, ok = res.(resource.ResourceWithImportState)
	assert.True(t, ok, "UserResource should implement resource.ResourceWithImportState")
}
