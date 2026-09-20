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
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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

// omittedInConfig derives the configuration Terraform would have sent for a
// plan in which every unknown replay_protection was simply left out: the
// framework marks an omitted computed attribute unknown in the plan, while the
// configuration holds null.
func omittedInConfig(t *testing.T, plan types.List) types.List {
	t.Helper()
	objects := make([]attr.Value, 0, len(plan.Elements()))
	for _, element := range plan.Elements() {
		attributes := map[string]attr.Value{}
		for name, value := range element.(types.Object).Attributes() {
			attributes[name] = value
		}
		if attributes["replay_protection"].IsUnknown() {
			attributes["replay_protection"] = types.BoolNull()
		}
		object, diags := types.ObjectValue(federatedIdentityAttrTypes, attributes)
		require.False(t, diags.HasError(), "%v", diags)
		objects = append(objects, object)
	}
	return identityList(t, objects...)
}

func plannedReplayProtection(t *testing.T, state, plan types.List) []types.Bool {
	t.Helper()
	return plannedReplayProtectionWithConfig(t, state, omittedInConfig(t, plan), plan)
}

func plannedReplayProtectionWithConfig(t *testing.T, state, config, plan types.List) []types.Bool {
	t.Helper()
	resp := &planmodifier.ListResponse{PlanValue: plan}
	federatedReplayProtectionModifier{}.PlanModifyList(context.Background(),
		planmodifier.ListRequest{StateValue: state, ConfigValue: config, PlanValue: plan}, resp)
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

// replay_protection = some_resource.output is configured but unknown while
// planning. Planning a value over it contradicts the configuration, and
// Terraform rejects the plan ("planned value does not match config value").
func TestReplayProtectionPlan_ConfiguredUnknownIsLeftAlone(t *testing.T) {
	state := identityList(t, identityObject(t, "https://a.example", "s", types.BoolValue(true)))
	plan := identityList(t,
		identityObject(t, "https://a.example", "s", types.BoolUnknown()),
		identityObject(t, "https://new.example", "s", types.BoolUnknown()),
	)
	config := plan // unknown in the configuration itself, not omitted

	assert.Equal(t, []types.Bool{types.BoolUnknown(), types.BoolUnknown()},
		plannedReplayProtectionWithConfig(t, state, config, plan))
}

// Pocket ID accepts identities sharing issuer, subject and audience. A
// first-match lookup would give every twin the first one's setting and silently
// disable protection on the second here.
func TestReplayProtectionPlan_TwinsKeepTheirOwnSetting(t *testing.T) {
	state := identityList(t,
		identityObject(t, "https://twin.example", "s", types.BoolValue(false)),
		identityObject(t, "https://twin.example", "s", types.BoolValue(true)),
	)
	plan := identityList(t,
		identityObject(t, "https://twin.example", "s", types.BoolUnknown()),
		identityObject(t, "https://twin.example", "s", types.BoolUnknown()),
		identityObject(t, "https://twin.example", "s", types.BoolUnknown()),
	)

	assert.Equal(t,
		[]types.Bool{types.BoolValue(false), types.BoolValue(true), types.BoolValue(true)},
		plannedReplayProtection(t, state, plan), "a third twin is new and gets the default")

	// An explicit value on the first twin must not shift the second one's pairing.
	explicit := identityList(t,
		identityObject(t, "https://twin.example", "s", types.BoolValue(true)),
		identityObject(t, "https://twin.example", "s", types.BoolUnknown()),
	)
	assert.Equal(t, []types.Bool{types.BoolValue(true), types.BoolValue(true)},
		plannedReplayProtection(t, state, explicit))
}

func TestResolveReplayProtections(t *testing.T) {
	current := []client.OIDCClientFederatedIdentity{
		{Issuer: "https://a.example", Subject: "s", ReplayProtection: false},
		{Issuer: "https://twin.example", Subject: "s", ReplayProtection: false},
		{Issuer: "https://twin.example", Subject: "s", ReplayProtection: true},
	}
	identity := func(issuer string, replay types.Bool) clientFederatedIdentityModel {
		return clientFederatedIdentityModel{
			Issuer: types.StringValue(issuer), Subject: types.StringValue("s"),
			Audience: types.StringNull(), ReplayProtection: replay,
		}
	}

	assert.Equal(t, []bool{true, false, true, true, true},
		resolveReplayProtections([]clientFederatedIdentityModel{
			identity("https://a.example", types.BoolValue(true)),   // explicit value wins
			identity("https://twin.example", types.BoolUnknown()),  // first twin: server value
			identity("https://twin.example", types.BoolUnknown()),  // second twin: its own server value
			identity("https://twin.example", types.BoolUnknown()),  // third twin: new
			identity("https://other.example", types.BoolUnknown()), // new identity
		}, current))
	assert.Equal(t, []bool{true},
		resolveReplayProtections([]clientFederatedIdentityModel{identity("https://a.example", types.BoolUnknown())}, nil),
		"create has no server identities")

	// An empty subject or audience is the same identity as an absent one: the
	// API omits empty strings and Read maps them to null.
	withEmpty := clientFederatedIdentityModel{
		Issuer: types.StringValue("https://a.example"), Subject: types.StringValue("s"),
		Audience: types.StringValue(""), ReplayProtection: types.BoolUnknown(),
	}
	assert.Equal(t, []bool{false}, resolveReplayProtections([]clientFederatedIdentityModel{withEmpty}, current))
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
		"non-string kid":      {`{"kty":"EC","crv":"P-256","kid":7,"x":"a","y":"b"}`, true},
		"non-string use":      {`{"kty":"EC","crv":"P-256","kid":"k","use":["sig"],"x":"a","y":"b"}`, true},
		"RSA without modulus": {`{"kty":"RSA","kid":"k"}`, true},
		"EC without y":        {`{"kty":"EC","crv":"P-256","kid":"k","x":"a"}`, true},
		"public OKP key":      {`{"kty":"OKP","crv":"Ed25519","kid":"k","x":"a"}`, false},
		"unknown key type":    {`{"kty":"future","kid":"k"}`, false},
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

func TestPublicJWKValidatorRejectsNull(t *testing.T) {
	for name, tc := range map[string]struct {
		value   types.String
		problem bool
	}{
		"null":    {types.StringNull(), true},
		"unknown": {types.StringUnknown(), false},
		"valid":   {types.StringValue(testPublicJWK), false},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &validator.StringResponse{}
			publicJWKValidator{}.ValidateString(context.Background(), validator.StringRequest{ConfigValue: tc.value}, resp)
			assert.Equal(t, tc.problem, resp.Diagnostics.HasError())
		})
	}
}

func TestUniquePublicKeyIDValidator(t *testing.T) {
	keys := func(kids ...string) types.List {
		values := make([]attr.Value, 0, len(kids))
		for _, kid := range kids {
			values = append(values, jsontypes.NewNormalizedValue(`{"kty":"OKP","crv":"Ed25519","kid":"`+kid+`","x":"a"}`))
		}
		return types.ListValueMust(publicKeysListType.ElemType, values)
	}
	for name, tc := range map[string]struct {
		list    types.List
		problem bool
	}{
		"distinct":  {keys("a", "b"), false},
		"duplicate": {keys("a", "b", "a"), true},
		"null list": {types.ListNull(publicKeysListType.ElemType), false},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &validator.ListResponse{}
			uniquePublicKeyIDValidator{}.ValidateList(context.Background(), validator.ListRequest{ConfigValue: tc.list}, resp)
			assert.Equal(t, tc.problem, resp.Diagnostics.HasError())
			for _, diagnostic := range resp.Diagnostics {
				assert.NotContains(t, diagnostic.Detail(), `"x"`, "the key itself is never echoed")
			}
		})
	}
}
