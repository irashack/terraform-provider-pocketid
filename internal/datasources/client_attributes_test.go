package datasources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// fullClientJSON is a client as Pocket ID 2.17 returns it, with every field
// the data sources report.
const fullClientJSON = `{"id":"c1","name":"App","description":"An app","hasLogo":true,"hasDarkLogo":true,
"launchURL":"https://app.example.invalid/","requiresReauthentication":true,"clientType":"standard",
"callbackURLs":["https://app.example.invalid/cb"],"logoutCallbackURLs":[],"backchannelLogoutURL":"https://app.example.invalid/bc",
"isPublic":false,"pkceEnabled":false,"pkceSupported":true,"requiresPushedAuthorizationRequests":true,"skipConsent":true,
"isGroupRestricted":true,"accessTokenDurationMinutes":5,"refreshTokenDurationMinutes":10,
"credentials":{"federatedIdentities":[{"issuer":"https://issuer.example.invalid","subject":"s","publicKeys":[{"kty":"OKP","crv":"Ed25519","kid":"k","x":"AAAA"}],"replayProtection":true}],
"secrets":[{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","prefix":"abcd","createdAt":"2026-01-02T03:04:05Z","expiresAt":"2027-01-02T03:04:05Z","isActive":true},
{"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","prefix":"","createdAt":"2025-01-02T03:04:05Z","expiresAt":null,"isActive":true}]},
"allowedUserGroups":[{"id":"bbbbbbbb-0000-4000-8000-000000000002"},{"id":"bbbbbbbb-0000-4000-8000-000000000001"}]}`

func TestClientDataSourceReadsEveryField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/oidc/clients/c1", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, fullClientJSON)
	}))
	defer server.Close()
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)

	ctx := context.Background()
	d := &clientDataSource{client: c}
	schemaResp := datasource.SchemaResponse{}
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	s := schemaResp.Schema
	// The configuration sets only id.
	built := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	require.False(t, built.SetAttribute(ctx, path.Root("id"), types.StringValue("c1")).HasError())
	config := tfsdk.Config{Schema: s, Raw: built.Raw}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(ctx, datasource.ReadRequest{Config: config}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	var got clientModel
	require.False(t, resp.State.Get(ctx, &got).HasError())
	assert.Equal(t, "An app", got.Description.ValueString())
	assert.True(t, got.SkipConsent.ValueBool())
	assert.True(t, got.PkceSupported.ValueBool())
	assert.True(t, got.HasDarkLogo.ValueBool())
	assert.Equal(t, "standard", got.ClientType.ValueString())
	assert.True(t, got.RequiresPushedAuthorizationRequests.ValueBool())
	assert.True(t, got.IsGroupRestricted.ValueBool())
	assert.Equal(t, int64(5), got.AccessTokenDurationMinutes.ValueInt64())
	assert.Equal(t, int64(10), got.RefreshTokenDurationMinutes.ValueInt64())
	assert.Equal(t, 2, len(got.AllowedUserGroups.Elements()))
	require.Len(t, got.FederatedIdentities, 1)
	assert.Equal(t, "s", got.FederatedIdentities[0].Subject.ValueString())
	assert.True(t, got.FederatedIdentities[0].Audience.IsNull())
	assert.Len(t, got.FederatedIdentities[0].PublicKeys.Elements(), 1)
	require.Len(t, got.Secrets, 2)
	assert.Equal(t, "abcd", got.Secrets[0].Prefix.ValueString())
	assert.Equal(t, "2026-01-02T03:04:05Z", got.Secrets[0].CreatedAt.ValueString())
	assert.Equal(t, "2027-01-02T03:04:05Z", got.Secrets[0].ExpiresAt.ValueString())
	assert.True(t, got.Secrets[1].ExpiresAt.IsNull())
	assert.True(t, got.LogoutCallbackURLs.IsNull())
}

// A client without identities or secrets reports empty lists, so that
// configurations can iterate over them.
func TestClientModelEmptyLists(t *testing.T) {
	model := clientModelFromAPI(context.Background(), client.OIDCClient{ID: "c1", Name: "n"})
	assert.NotNil(t, model.FederatedIdentities)
	assert.Empty(t, model.FederatedIdentities)
	assert.NotNil(t, model.Secrets)
	assert.True(t, model.RequiresPushedAuthorizationRequests.IsNull())
	assert.True(t, model.AllowedUserGroups.IsNull())
}
