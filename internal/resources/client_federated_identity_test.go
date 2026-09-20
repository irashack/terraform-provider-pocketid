package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

const testPublicJWK = `{"kty":"EC","crv":"P-256","kid":"key-1","use":"sig","x":"ScFVPMb2zxk2ZDS5IJu91DBAzf4L7bKikkOXdV6I4_w","y":"yJAFYZTNNfNKrBFfEnzqepcQkSEfyWOyr0l5U3l5aTM"}`

func identityObject(t *testing.T, issuer, subject string, replay types.Bool) attr.Value {
	t.Helper()
	object, diags := types.ObjectValue(federatedIdentityAttrTypes, map[string]attr.Value{
		"issuer":            types.StringValue(issuer),
		"subject":           types.StringValue(subject),
		"audience":          types.StringNull(),
		"jwks":              types.StringNull(),
		"public_keys":       types.ListNull(publicKeysListType.ElemType),
		"replay_protection": replay,
	})
	require.False(t, diags.HasError(), "%v", diags)
	return object
}

func identityList(t *testing.T, objects ...attr.Value) types.List {
	t.Helper()
	list, diags := types.ListValue(types.ObjectType{AttrTypes: federatedIdentityAttrTypes}, objects)
	require.False(t, diags.HasError(), "%v", diags)
	return list
}

func plannedReplayProtection(t *testing.T, state, plan types.List) []types.Bool {
	t.Helper()
	resp := &planmodifier.ListResponse{PlanValue: plan}
	federatedReplayProtectionModifier{}.PlanModifyList(context.Background(),
		planmodifier.ListRequest{StateValue: state, PlanValue: plan}, resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	values := make([]types.Bool, 0, len(resp.PlanValue.Elements()))
	for _, element := range resp.PlanValue.Elements() {
		values = append(values, element.(types.Object).Attributes()["replay_protection"].(types.Bool))
	}
	return values
}

func TestReplayProtectionPlan_NewIdentityGetsAdminUIDefault(t *testing.T) {
	state := types.ListNull(types.ObjectType{AttrTypes: federatedIdentityAttrTypes})
	plan := identityList(t, identityObject(t, "https://a.example", "s", types.BoolUnknown()))

	assert.Equal(t, []types.Bool{types.BoolValue(true)}, plannedReplayProtection(t, state, plan))
}

func TestReplayProtectionPlan_ExplicitValueWins(t *testing.T) {
	state := identityList(t, identityObject(t, "https://a.example", "s", types.BoolValue(true)))
	plan := identityList(t, identityObject(t, "https://a.example", "s", types.BoolValue(false)))

	assert.Equal(t, []types.Bool{types.BoolValue(false)}, plannedReplayProtection(t, state, plan))
}

// Inserting an identity ahead of an existing one must not hand the existing
// identity's setting to the new one, which index-based state reuse would do.
func TestReplayProtectionPlan_FollowsIdentityNotIndex(t *testing.T) {
	state := identityList(t,
		identityObject(t, "https://a.example", "s", types.BoolValue(false)),
		identityObject(t, "https://b.example", "s", types.BoolValue(true)),
	)
	plan := identityList(t,
		identityObject(t, "https://new.example", "s", types.BoolUnknown()),
		identityObject(t, "https://b.example", "s", types.BoolUnknown()),
		identityObject(t, "https://a.example", "s", types.BoolUnknown()),
	)

	assert.Equal(t,
		[]types.Bool{types.BoolValue(true), types.BoolValue(true), types.BoolValue(false)},
		plannedReplayProtection(t, state, plan))
}

// State written before the attribute existed, planned without a refresh, holds
// null. The plan must stay unknown so apply reads the real value instead of
// assuming one.
func TestReplayProtectionPlan_UntrackedPriorStaysUnknown(t *testing.T) {
	state := identityList(t, identityObject(t, "https://a.example", "s", types.BoolNull()))
	plan := identityList(t, identityObject(t, "https://a.example", "s", types.BoolUnknown()))

	assert.Equal(t, []types.Bool{types.BoolUnknown()}, plannedReplayProtection(t, state, plan))
}

func TestResolveReplayProtection(t *testing.T) {
	current := []client.OIDCClientFederatedIdentity{
		{Issuer: "https://a.example", Subject: "s", ReplayProtection: false},
		{Issuer: "https://b.example", Subject: "s", ReplayProtection: true},
	}
	identity := func(issuer string, replay types.Bool) clientFederatedIdentityModel {
		return clientFederatedIdentityModel{
			Issuer: types.StringValue(issuer), Subject: types.StringValue("s"),
			Audience: types.StringNull(), ReplayProtection: replay,
		}
	}

	assert.False(t, resolveReplayProtection(identity("https://b.example", types.BoolValue(false)), current), "explicit value wins")
	assert.False(t, resolveReplayProtection(identity("https://a.example", types.BoolUnknown()), current), "server value kept")
	assert.True(t, resolveReplayProtection(identity("https://b.example", types.BoolUnknown()), current), "server value kept")
	assert.True(t, resolveReplayProtection(identity("https://new.example", types.BoolUnknown()), current), "new identity default")
	assert.True(t, resolveReplayProtection(identity("https://new.example", types.BoolUnknown()), nil), "create default")
}

func TestFederatedIdentityRoundTrip(t *testing.T) {
	ctx := context.Background()
	api := []client.OIDCClientFederatedIdentity{{
		Issuer:           "https://issuer.example.com",
		PublicKeys:       []json.RawMessage{json.RawMessage(testPublicJWK)},
		ReplayProtection: true,
	}}

	list := federatedIdentitiesToList(ctx, api)
	var models []clientFederatedIdentityModel
	require.False(t, list.ElementsAs(ctx, &models, false).HasError())
	require.Len(t, models, 1)
	assert.True(t, models[0].ReplayProtection.ValueBool())
	assert.True(t, models[0].JWKS.IsNull())
	require.Len(t, models[0].PublicKeys.Elements(), 1)

	credentials := buildCredentialsFromPlan(ctx, &clientResourceModel{FederatedIdentities: list}, nil)
	require.Len(t, credentials.FederatedIdentities, 1)
	assert.True(t, credentials.FederatedIdentities[0].ReplayProtection)
	require.Len(t, credentials.FederatedIdentities[0].PublicKeys, 1)
	assert.JSONEq(t, testPublicJWK, string(credentials.FederatedIdentities[0].PublicKeys[0]))
}

// Pocket ID re-encodes stored keys, so member order and whitespace change.
func TestPublicKeysCompareByJSONSemantics(t *testing.T) {
	configured := jsontypes.NewNormalizedValue(`{ "kid": "key-1", "kty": "EC" }`)
	stored := jsontypes.NewNormalizedValue(`{"kty":"EC","kid":"key-1"}`)

	equal, diags := configured.StringSemanticEquals(context.Background(), stored)
	require.False(t, diags.HasError())
	assert.True(t, equal)
}

// The request must state replayProtection even when false, and must not send
// publicKeys to servers that predate them unless keys are configured.
func TestFederatedIdentityRequestEncoding(t *testing.T) {
	encoded, err := json.Marshal(client.OIDCClientFederatedIdentity{Issuer: "https://issuer.example.com"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"issuer":"https://issuer.example.com","replayProtection":false}`, string(encoded))
}

func TestPublicJWKProblem(t *testing.T) {
	cases := map[string]struct {
		key     string
		problem bool
	}{
		"public EC key":       {testPublicJWK, false},
		"no use member":       {`{"kty":"RSA","kid":"k","n":"AQAB","e":"AQAB"}`, false},
		"not an object":       {`["a"]`, true},
		"JSON null":           {`null`, true},
		"missing kty":         {`{"kid":"k"}`, true},
		"symmetric":           {`{"kty":"oct","kid":"k","k":"c2VjcmV0"}`, true},
		"private EC key":      {`{"kty":"EC","crv":"P-256","kid":"k","x":"a","y":"b","d":"c"}`, true},
		"private RSA CRT":     {`{"kty":"RSA","kid":"k","n":"AQAB","e":"AQAB","p":"AQAB"}`, true},
		"missing kid":         {`{"kty":"EC","crv":"P-256","x":"a","y":"b"}`, true},
		"encryption-only use": {`{"kty":"EC","crv":"P-256","kid":"k","use":"enc","x":"a","y":"b"}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.problem, publicJWKProblem(tc.key) != "")
		})
	}
}

func TestCheckFederatedPublicKeysSupport(t *testing.T) {
	withKeys := client.OIDCClientCredentials{FederatedIdentities: []client.OIDCClientFederatedIdentity{
		{Issuer: "https://issuer.example.com", PublicKeys: []json.RawMessage{json.RawMessage(testPublicJWK)}},
	}}
	withoutKeys := client.OIDCClientCredentials{FederatedIdentities: []client.OIDCClientFederatedIdentity{
		{Issuer: "https://issuer.example.com"},
	}}

	serverReporting := func(t *testing.T, version string) (*client.Client, *int) {
		t.Helper()
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			assert.Equal(t, "/api/version/current", r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]string{"currentVersion": version})
		}))
		t.Cleanup(server.Close)
		api, err := client.NewClient(server.URL, "token", false, 5)
		require.NoError(t, err)
		return api, &requests
	}

	t.Run("no keys asks the server nothing", func(t *testing.T) {
		api, requests := serverReporting(t, "2.14.0")
		assert.NoError(t, checkFederatedPublicKeysSupport(api, withoutKeys))
		assert.Zero(t, *requests)
	})
	t.Run("2.14 refuses before mutation", func(t *testing.T) {
		api, _ := serverReporting(t, "2.14.0")
		assert.ErrorContains(t, checkFederatedPublicKeysSupport(api, withKeys), "2.15.0 or later")
	})
	t.Run("2.15 accepts", func(t *testing.T) {
		api, _ := serverReporting(t, "v2.15.0")
		assert.NoError(t, checkFederatedPublicKeysSupport(api, withKeys))
	})
	t.Run("unverifiable version refuses", func(t *testing.T) {
		api, _ := serverReporting(t, "not-a-version")
		assert.Error(t, checkFederatedPublicKeysSupport(api, withKeys))
	})
}
