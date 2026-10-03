package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pocketidprovider "github.com/irashack/terraform-provider-pocketid/internal/provider"
)

const scimResourceType = "pocketid_scim_service_provider"

// scimSignupProtocolServer starts the provider behind a fake Pocket ID served by
// handler and returns the configured provider server and its schemas.
func scimSignupProtocolServer(t *testing.T, handler http.Handler) (tfprotov6.ProviderServer, *tfprotov6.GetProviderSchemaResponse) {
	t.Helper()
	ctx := context.Background()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)

	server := providerserver.NewProtocol6(pocketidprovider.New("test")())()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)
	require.Empty(t, schemas.Diagnostics)

	providerType, ok := schemas.Provider.ValueType().(tftypes.Object)
	require.True(t, ok)
	providerConfig := map[string]tftypes.Value{}
	for name, attrType := range providerType.AttributeTypes {
		providerConfig[name] = tftypes.NewValue(attrType, nil)
	}
	providerConfig["base_url"] = tftypes.NewValue(tftypes.String, api.URL)
	providerConfig["api_token"] = tftypes.NewValue(tftypes.String, "test-token")
	config, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, providerConfig))
	require.NoError(t, err)
	configured, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &config})
	require.NoError(t, err)
	require.Empty(t, configured.Diagnostics)
	return server, schemas
}

func scimSignupObjectType(t *testing.T, schemas *tfprotov6.GetProviderSchemaResponse, typeName string) tftypes.Object {
	t.Helper()
	resourceSchema, ok := schemas.ResourceSchemas[typeName]
	require.True(t, ok)
	objectType, ok := resourceSchema.ValueType().(tftypes.Object)
	require.True(t, ok)
	return objectType
}

// scimProtocolServer starts the provider behind a fake Pocket ID that holds
// one SCIM service provider with the given token, and returns the configured
// provider server together with the resource's object type.
func scimProtocolServer(t *testing.T, serverToken string) (tfprotov6.ProviderServer, tftypes.Object) {
	t.Helper()
	server, schemas := scimSignupProtocolServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/oidc/clients/scim-client/scim-service-provider" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "33333333-3333-4333-8333-333333333333", "endpoint": "https://scim.example.com/v2",
				"token": serverToken, "lastSyncedAt": nil, "createdAt": "2026-01-01T00:00:00Z",
				"oidcClient": map[string]any{"id": "scim-client", "name": "SCIM client"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	return server, scimSignupObjectType(t, schemas, scimResourceType)
}

func scimProtocolObject(t *testing.T, objectType tftypes.Object, values map[string]any) tftypes.Value {
	t.Helper()
	attributes := map[string]tftypes.Value{}
	for name, attrType := range objectType.AttributeTypes {
		attributes[name] = tftypes.NewValue(attrType, values[name])
	}
	return tftypes.NewValue(objectType, attributes)
}

func scimDynamic(t *testing.T, objectType tftypes.Object, value tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dynamic, err := tfprotov6.NewDynamicValue(objectType, value)
	require.NoError(t, err)
	return &dynamic
}

// State written before token_wo and token_wo_version existed must upgrade,
// refresh and plan empty under an unchanged configuration, with no state
// upgrader: the new attributes simply decode as null. The JSON below is the
// state shape of the 2.4.x releases.
func TestScimServiceProvider_StateFromTheEarlierSchemaPlansEmpty(t *testing.T) {
	cases := []struct {
		name        string
		stateToken  any // JSON value of "token" in the old state
		serverToken string
		configToken any // the configured token, nil when unconfigured
	}{
		{"a configured token", "abc", "abc", "abc"},
		{"no token", nil, "", nil},
		{"an explicitly empty token", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			server, objectType := scimProtocolServer(t, tc.serverToken)

			old, err := json.Marshal(map[string]any{
				"id": "33333333-3333-4333-8333-333333333333", "client_id": "scim-client",
				"endpoint": "https://scim.example.com/v2", "token": tc.stateToken,
				"last_synced_at": nil, "created_at": "2026-01-01T00:00:00Z",
			})
			require.NoError(t, err)

			upgraded, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
				TypeName: scimResourceType, Version: 0, RawState: &tfprotov6.RawState{JSON: old},
			})
			require.NoError(t, err)
			require.Empty(t, upgraded.Diagnostics)

			read, err := server.ReadResource(ctx, &tfprotov6.ReadResourceRequest{TypeName: scimResourceType, CurrentState: upgraded.UpgradedState})
			require.NoError(t, err)
			require.Empty(t, read.Diagnostics)
			prior, err := read.NewState.Unmarshal(objectType)
			require.NoError(t, err)

			var priorValues map[string]tftypes.Value
			require.NoError(t, prior.As(&priorValues))
			assert.True(t, priorValues["token_wo"].IsNull())
			assert.True(t, priorValues["token_wo_version"].IsNull())

			config := scimProtocolObject(t, objectType, map[string]any{
				"client_id": "scim-client", "endpoint": "https://scim.example.com/v2", "token": tc.configToken,
			})
			// Terraform proposes the configuration for configurable attributes
			// and the prior value for computed-only ones.
			proposed := scimProtocolObject(t, objectType, map[string]any{
				"id": "33333333-3333-4333-8333-333333333333", "client_id": "scim-client",
				"endpoint": "https://scim.example.com/v2", "token": tc.configToken,
				"last_synced_at": nil, "created_at": "2026-01-01T00:00:00Z",
			})
			planned, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
				TypeName:         scimResourceType,
				PriorState:       read.NewState,
				ProposedNewState: scimDynamic(t, objectType, proposed),
				Config:           scimDynamic(t, objectType, config),
			})
			require.NoError(t, err)
			require.Empty(t, planned.Diagnostics)
			plannedValue, err := planned.PlannedState.Unmarshal(objectType)
			require.NoError(t, err)
			assert.True(t, prior.Equal(plannedValue), "the plan must equal the refreshed state")
		})
	}
}

// The three token attributes interact: the plain token conflicts with the
// write-only one, and the write-only token and its version go together.
func TestScimServiceProvider_WriteOnlyTokenConfigurationRules(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]any
		valid  bool
	}{
		{"plain token only", map[string]any{"token": "abc"}, true},
		{"write-only token with its version", map[string]any{"token_wo": "abc", "token_wo_version": "1"}, true},
		{"no token", map[string]any{}, true},
		{"plain and write-only token together", map[string]any{"token": "abc", "token_wo": "abc", "token_wo_version": "1"}, false},
		{"write-only token without a version", map[string]any{"token_wo": "abc"}, false},
		{"a version without the write-only token", map[string]any{"token_wo_version": "1"}, false},
		{"an empty version", map[string]any{"token_wo": "abc", "token_wo_version": ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			server, objectType := scimProtocolServer(t, "")
			values := map[string]any{"client_id": "scim-client", "endpoint": "https://scim.example.com/v2"}
			for k, v := range tc.values {
				values[k] = v
			}
			resp, err := server.ValidateResourceConfig(ctx, &tfprotov6.ValidateResourceConfigRequest{
				TypeName:           scimResourceType,
				Config:             scimDynamic(t, objectType, scimProtocolObject(t, objectType, values)),
				ClientCapabilities: &tfprotov6.ValidateResourceConfigClientCapabilities{WriteOnlyAttributesAllowed: true},
			})
			require.NoError(t, err)
			if tc.valid {
				assert.Empty(t, resp.Diagnostics)
			} else {
				assert.NotEmpty(t, resp.Diagnostics)
				for _, d := range resp.Diagnostics {
					assert.NotContains(t, d.Summary+d.Detail, "abc", "a diagnostic must not carry the token")
				}
			}
		})
	}
}

// scimImportThenRead runs the provider's import and the refresh that follows it,
// as Terraform does, and returns the refreshed state's attributes.
func scimImportThenRead(t *testing.T, serverToken, importID string) map[string]tftypes.Value {
	t.Helper()
	ctx := context.Background()
	server, objectType := scimProtocolServer(t, serverToken)

	imported, err := server.ImportResourceState(ctx, &tfprotov6.ImportResourceStateRequest{TypeName: scimResourceType, ID: importID})
	require.NoError(t, err)
	require.Empty(t, imported.Diagnostics)
	require.Len(t, imported.ImportedResources, 1)

	read, err := server.ReadResource(ctx, &tfprotov6.ReadResourceRequest{TypeName: scimResourceType, CurrentState: imported.ImportedResources[0].State})
	require.NoError(t, err)
	require.Empty(t, read.Diagnostics)
	value, err := read.NewState.Unmarshal(objectType)
	require.NoError(t, err)
	require.False(t, value.IsNull(), "the imported provider must exist after the refresh")
	var attributes map[string]tftypes.Value
	require.NoError(t, value.As(&attributes))
	return attributes
}

func scimAttributeString(t *testing.T, attributes map[string]tftypes.Value, name string) (string, bool) {
	t.Helper()
	if attributes[name].IsNull() {
		return "", false
	}
	var out string
	require.NoError(t, attributes[name].As(&out))
	return out, true
}

// The ordinary import form is for configurations that use `token`: the
// refresh stores the token Pocket ID holds, which is documented.
func TestScimServiceProvider_OrdinaryImportStoresTheServersToken(t *testing.T) {
	attributes := scimImportThenRead(t, "server-token-value", "scim-client")

	token, ok := scimAttributeString(t, attributes, "token")
	require.True(t, ok)
	assert.Equal(t, "server-token-value", token)
	_, hasVersion := scimAttributeString(t, attributes, "token_wo_version")
	assert.False(t, hasVersion)
	clientID, _ := scimAttributeString(t, attributes, "client_id")
	assert.Equal(t, "scim-client", clientID)
}

// The write-only import form seeds token_wo_version before the first Read, so
// the refresh that follows the import never writes the token to state.
func TestScimServiceProvider_WriteOnlyImportNeverStoresTheServersToken(t *testing.T) {
	for _, version := range []string{"1", "2026-10", "a,b=c"} {
		t.Run(version, func(t *testing.T) {
			attributes := scimImportThenRead(t, "server-token-value", "scim-client,token_wo_version="+version)

			_, hasToken := scimAttributeString(t, attributes, "token")
			assert.False(t, hasToken, "the write-only import form must not store the token")
			_, hasWriteOnly := scimAttributeString(t, attributes, "token_wo")
			assert.False(t, hasWriteOnly)
			got, ok := scimAttributeString(t, attributes, "token_wo_version")
			require.True(t, ok)
			assert.Equal(t, version, got)
			clientID, _ := scimAttributeString(t, attributes, "client_id")
			assert.Equal(t, "scim-client", clientID)
			endpoint, _ := scimAttributeString(t, attributes, "endpoint")
			assert.Equal(t, "https://scim.example.com/v2", endpoint)
			for name, value := range attributes {
				if !value.IsNull() {
					var text string
					if value.As(&text) == nil {
						assert.NotContains(t, text, "server-token-value", "attribute %s", name)
					}
				}
			}
		})
	}
}

func TestScimServiceProvider_ImportRefusesMalformedIDs(t *testing.T) {
	for _, id := range []string{
		"", "x", "../x", "scim-client,", "scim-client,token_wo_version=", "scim-client,version=1",
		"scim-client,token_wo_version", "scim-client token_wo_version=1", ",token_wo_version=1",
	} {
		t.Run(id, func(t *testing.T) {
			server, _ := scimProtocolServer(t, "server-token-value")
			imported, err := server.ImportResourceState(context.Background(), &tfprotov6.ImportResourceStateRequest{TypeName: scimResourceType, ID: id})
			require.NoError(t, err)
			require.NotEmpty(t, imported.Diagnostics)
			assert.Empty(t, imported.ImportedResources)
		})
	}
}
