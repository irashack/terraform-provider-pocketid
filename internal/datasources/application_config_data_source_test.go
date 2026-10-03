package datasources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

func TestNewApplicationConfigDataSource(t *testing.T) {
	d := datasources.NewApplicationConfigDataSource()
	assert.NotNil(t, d)

	_, ok := d.(datasource.DataSourceWithConfigure)
	assert.True(t, ok, "should implement DataSourceWithConfigure")
}

func TestApplicationConfigDataSource_Metadata(t *testing.T) {
	ctx := context.Background()
	d := datasources.NewApplicationConfigDataSource()

	req := datasource.MetadataRequest{ProviderTypeName: "pocketid"}
	resp := &datasource.MetadataResponse{}

	d.Metadata(ctx, req, resp)

	assert.Equal(t, "pocketid_application_config", resp.TypeName)
}

func TestApplicationConfigDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := datasources.NewApplicationConfigDataSource()

	req := datasource.SchemaRequest{}
	resp := &datasource.SchemaResponse{}

	d.Schema(ctx, req, resp)

	require.False(t, resp.Diagnostics.HasError())
	assert.NotEmpty(t, resp.Schema.Description)

	// All attributes are computed.
	for _, name := range []string{"id", "app_name", "webauthn_user_verification", "webauthn_allow_synced_passkeys", "webauthn_authenticator_attachment", "cimd_url_allowlist", "ldap_enabled", "auto_create_oidc_client_secret"} {
		attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		require.True(t, ok, "attribute %s should exist", name)
		assert.True(t, attr.Computed, "attribute %s should be computed", name)
	}

	// The secrets are not exposed at all.
	for _, name := range []string{"smtp_password", "ldap_bind_password"} {
		assert.NotContains(t, resp.Schema.Attributes, name)
	}
}

func TestApplicationConfigDataSource_Configure(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name          string
		providerData  interface{}
		expectError   bool
		errorContains string
	}{
		{name: "valid_client", providerData: &client.Client{}, expectError: false},
		{name: "nil_provider_data", providerData: nil, expectError: false},
		{name: "invalid_type", providerData: 123, expectError: true, errorContains: "Expected *client.Client"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			d := datasources.NewApplicationConfigDataSource()
			configurable, ok := d.(datasource.DataSourceWithConfigure)
			require.True(t, ok)

			req := datasource.ConfigureRequest{ProviderData: tc.providerData}
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

// readApplicationConfigDataSource runs the data source's Read against a fake
// server that reports vars.
func readApplicationConfigDataSource(t *testing.T, vars []client.AppConfigVariable) map[string]tftypes.Value {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/application-configuration/all", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vars)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)

	ctx := context.Background()
	d := datasources.NewApplicationConfigDataSource()
	d.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{ProviderData: c}, &datasource.ConfigureResponse{})
	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	objectType := schemaResp.Schema.Type().TerraformType(ctx)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objectType, nil)}}
	d.Read(ctx, datasource.ReadRequest{}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	values := map[string]tftypes.Value{}
	require.NoError(t, resp.State.Raw.As(&values))
	return values
}

func TestApplicationConfigDataSource_AutoCreateOIDCClientSecret(t *testing.T) {
	values := readApplicationConfigDataSource(t, []client.AppConfigVariable{{Key: "appName", Value: "Fixture"}, {Key: "autoCreateOidcClientSecret", Value: "false"}})
	var value string
	require.NoError(t, values["auto_create_oidc_client_secret"].As(&value))
	assert.Equal(t, "false", value)

	values = readApplicationConfigDataSource(t, []client.AppConfigVariable{{Key: "appName", Value: "Fixture"}})
	assert.True(t, values["auto_create_oidc_client_secret"].IsNull(), "null on a server without the setting")
}

func TestApplicationConfigDataSource_NoSecrets(t *testing.T) {
	values := readApplicationConfigDataSource(t, []client.AppConfigVariable{
		{Key: "appName", Value: "Fixture"},
		{Key: "smtpPassword", Value: "synthetic-smtp-password"},
		{Key: "ldapBindPassword", Value: "synthetic-ldap-password"},
	})
	for name, value := range values {
		var text string
		if value.IsKnown() && !value.IsNull() && value.As(&text) == nil {
			assert.NotContains(t, text, "synthetic-", "attribute %s", name)
		}
	}
	assert.NotContains(t, values, "smtp_password")
	assert.NotContains(t, values, "ldap_bind_password")
}
