package resources

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func TestIdentifySecret(t *testing.T) {
	secret := func(id, prefix string) client.ClientSecretMetadata {
		return client.ClientSecretMetadata{ID: id, Prefix: prefix, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	}
	const value = "abcdEFGHijklMNOPqrstUVWXyz012345"
	for name, tc := range map[string]struct {
		value   string
		secrets []client.ClientSecretMetadata
		id      string
		gone    bool
		err     bool
	}{
		"single match":                {value, []client.ClientSecretMetadata{secret("s1", "zzzz"), secret("s2", "abcd")}, "s2", false, false},
		"no secrets: gone":            {value, nil, "", true, false},
		"no match: gone":              {value, []client.ClientSecretMetadata{secret("s1", "zzzz")}, "", true, false},
		"two matches":                 {value, []client.ClientSecretMetadata{secret("s1", "abcd"), secret("s2", "abcd")}, "", false, true},
		"migrated secret could be it": {value, []client.ClientSecretMetadata{secret("s1", "")}, "", false, true},
		"match beside migrated":       {value, []client.ClientSecretMetadata{secret("s1", "abcd"), secret("s2", "")}, "", false, true},
		"value without a prefix":      {"abcd", []client.ClientSecretMetadata{secret("s1", "abcd")}, "", false, true},
	} {
		t.Run(name, func(t *testing.T) {
			id, gone, err := identifySecret(tc.value, tc.secrets)
			assert.Equal(t, tc.id, id)
			assert.Equal(t, tc.gone, gone)
			assert.Equal(t, tc.err, err != nil, "%v", err)
			if err != nil {
				assert.ErrorIs(t, err, errSecretNotIdentified)
			}
		})
	}
}

func TestPlanSecretAttributes(t *testing.T) {
	held := managedModel()
	legacy := managedModel() // written by 2.4.x: neither attribute recorded
	legacy.GenerateSecret = types.BoolNull()
	legacy.ClientSecretID = types.StringNull()
	imported := managedModel() // a confidential client imported: no secret
	imported.ClientSecret, imported.ClientSecretID = types.StringNull(), types.StringNull()
	off := imported
	off.GenerateSecret = types.BoolValue(false)

	unknown := types.StringUnknown()
	null := types.StringNull()
	for name, tc := range map[string]struct {
		prior           *clientResourceModel
		generate        types.Bool
		public          bool
		secret, secret2 types.String
	}{
		"create confidential":          {nil, types.BoolValue(true), false, unknown, unknown},
		"create without secret":        {nil, types.BoolValue(false), false, null, null},
		"create public":                {nil, types.BoolValue(true), true, null, null},
		"create, generate unknown":     {nil, types.BoolUnknown(), false, unknown, unknown},
		"keep":                         {&held, types.BoolValue(true), false, held.ClientSecret, held.ClientSecretID},
		"keep legacy state":            {&legacy, types.BoolValue(true), false, legacy.ClientSecret, null},
		"imported keeps none":          {&imported, types.BoolValue(true), false, null, null},
		"true to false revokes":        {&held, types.BoolValue(false), false, null, null},
		"legacy true to false revokes": {&legacy, types.BoolValue(false), false, null, null},
		"false to true generates":      {&off, types.BoolValue(true), false, unknown, unknown},
		"false stays false":            {&off, types.BoolValue(false), false, null, null},
		"generate unknown on update":   {&held, types.BoolUnknown(), false, unknown, unknown},
	} {
		t.Run(name, func(t *testing.T) {
			proposed := managedModel()
			if tc.prior != nil {
				proposed = *tc.prior
			}
			proposed.Name = types.StringValue("renamed")
			proposed.GenerateSecret = tc.generate
			proposed.IsPublic = types.BoolValue(tc.public)
			proposed.ClientSecret, proposed.ClientSecretID = types.StringUnknown(), types.StringUnknown()
			if tc.prior == nil {
				proposed.ID = types.StringUnknown()
				proposed.HasLogo = types.BoolUnknown()
			}
			resp, planned := runModifyPlan(t, tc.prior, proposed, configOf(proposed))
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, tc.secret, planned.ClientSecret, "client_secret")
			assert.Equal(t, tc.secret2, planned.ClientSecretID, "client_secret_id")
		})
	}
}

// Changing generate_secret from true to false revokes exactly the secret this
// resource generated and nulls both attributes; the client is not replaced.
func TestClientUpdateGenerateSecretOff(t *testing.T) {
	const heldID = "00000000-0000-4000-8000-000000000000"
	other := fakeSecret{ID: "99999999-0000-4000-8000-000000000000", Prefix: "othr", Created: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	for name, tc := range map[string]struct {
		legacy     bool // state without client_secret_id
		secrets    func(f *fakePocketID)
		deleteFail int
		wantErr    string
		wantPuts   int
		wantDelete bool
		keeps      bool // the secret stays in state
	}{
		"by client_secret_id":        {wantPuts: 1, wantDelete: true},
		"legacy state, by prefix":    {legacy: true, secrets: func(f *fakePocketID) { f.secrets = append(f.secrets, other) }, wantPuts: 1, wantDelete: true},
		"legacy state, already gone": {legacy: true, secrets: func(f *fakePocketID) { f.secrets = []fakeSecret{other} }, wantPuts: 1},
		"legacy state, two candidates": {legacy: true, secrets: func(f *fakePocketID) {
			f.secrets = append(f.secrets, fakeSecret{ID: other.ID, Prefix: "gen0", Created: other.Created})
		}, wantErr: "No change was made"},
		"legacy state, migrated secret could be it": {legacy: true, secrets: func(f *fakePocketID) {
			f.secrets = append(f.secrets, fakeSecret{ID: other.ID, Created: other.Created})
		}, wantErr: "No change was made"},
		"revoke unconfirmed":           {deleteFail: 503, wantErr: "may still be valid", wantPuts: 1, wantDelete: true, keeps: true},
		"secret already revoked by id": {secrets: func(f *fakePocketID) { f.secrets = nil }, wantPuts: 1, wantDelete: true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := managedFake(t, "2.17.0")
			if tc.secrets != nil {
				tc.secrets(fake)
			}
			if tc.deleteFail != 0 {
				fake.fail["DELETE /api/oidc/clients/c1/secrets/"+heldID] = tc.deleteFail
			}
			r := &clientResource{client: fake.start()}
			prior := managedModel()
			if tc.legacy {
				prior.ClientSecretID = types.StringNull()
				prior.GenerateSecret = types.BoolNull()
			}
			planned := prior
			planned.GenerateSecret = types.BoolValue(false)
			planned.ClientSecret, planned.ClientSecretID = types.StringNull(), types.StringNull()

			resp, after := runUpdate(t, r, prior, planned, configOf(planned))
			requireNoSecret(t, resp.Diagnostics)
			assert.Equal(t, tc.wantPuts, fake.called("PUT /api/oidc/clients/c1"))
			deletes := 0
			for _, call := range fake.mutations() {
				if call == "DELETE /api/oidc/clients/c1/secrets/"+heldID {
					deletes++
				} else {
					assert.Equal(t, "PUT /api/oidc/clients/c1", call, "only the held secret is revoked")
				}
			}
			assert.Equal(t, tc.wantDelete, deletes == 1)
			assert.LessOrEqual(t, deletes, 1, "a revoke is never retried")
			if tc.wantErr == "" {
				require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
				assert.True(t, after.ClientSecret.IsNull())
				assert.True(t, after.ClientSecretID.IsNull())
				assert.False(t, after.GenerateSecret.ValueBool())
				return
			}
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, resp.Diagnostics[0].Detail(), tc.wantErr)
			if tc.wantPuts == 0 {
				assert.Empty(t, fake.mutations(), "nothing changes when the secret cannot be identified")
				assert.Contains(t, resp.Diagnostics[0].Detail(), heldID, "the diagnostic lists the client's secrets")
				assert.Contains(t, resp.Diagnostics[0].Detail(), other.ID)
				assert.Equal(t, prior, after, "state is unchanged")
			}
			if tc.keeps {
				assert.Equal(t, prior.ClientSecret, after.ClientSecret, "an unconfirmed revoke keeps the secret in state")
				assert.Equal(t, heldID, after.ClientSecretID.ValueString())
			}
		})
	}
}

// Changing generate_secret from false to true generates a secret and records
// it; a failed generation records generate_secret = false so the next plan
// tries again, and says whether a secret may exist.
func TestClientUpdateGenerateSecretOn(t *testing.T) {
	for name, tc := range map[string]struct {
		fail    int
		wantErr string
	}{
		"generated": {},
		"rejected":  {fail: 400, wantErr: "none was created"},
		"uncertain": {fail: 503, wantErr: "may have been created"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := managedFake(t, "2.16.0")
			fake.secrets = nil
			if tc.fail != 0 {
				fake.fail["POST /api/oidc/clients/c1/secrets"] = tc.fail
			}
			r := &clientResource{client: fake.start()}
			prior := managedModel()
			prior.GenerateSecret = types.BoolValue(false)
			prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
			planned := prior
			planned.GenerateSecret = types.BoolValue(true)
			planned.ClientSecret, planned.ClientSecretID = types.StringUnknown(), types.StringUnknown()

			resp, after := runUpdate(t, r, prior, planned, configOf(planned))
			requireNoSecret(t, resp.Diagnostics)
			assert.Equal(t, 1, fake.called("POST /api/oidc/clients/c1/secrets"), "never retried")
			if tc.wantErr == "" {
				require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
				assert.Equal(t, "gen1synthetic-generated-secret-value", after.ClientSecret.ValueString())
				assert.Equal(t, "00000001-0000-4000-8000-000000000000", after.ClientSecretID.ValueString())
				assert.True(t, after.GenerateSecret.ValueBool())
				return
			}
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, resp.Diagnostics[0].Detail(), tc.wantErr)
			assert.Contains(t, resp.Diagnostics[0].Detail(), "The client itself was updated")
			assert.True(t, after.ClientSecret.IsNull())
			assert.False(t, after.GenerateSecret.ValueBool(), "the next plan shows the generation again")
		})
	}
}

// Refresh records generate_secret = true for state written before it existed,
// and client_secret_id when the stored secret can be identified by prefix.
func TestClientReadFillsSecretAttributes(t *testing.T) {
	for name, tc := range map[string]struct {
		version string
		extra   []fakeSecret
		want    types.String
		lists   int
	}{
		"identified":           {"2.16.0", nil, types.StringValue("00000000-0000-4000-8000-000000000000"), 1},
		"ambiguous stays null": {"2.16.0", []fakeSecret{{ID: "99999999-0000-4000-8000-000000000000", Prefix: "gen0"}}, types.StringNull(), 1},
		"migrated stays null":  {"2.16.0", []fakeSecret{{ID: "99999999-0000-4000-8000-000000000000"}}, types.StringNull(), 1},
	} {
		t.Run(name, func(t *testing.T) {
			fake := managedFake(t, tc.version)
			fake.secrets = append(fake.secrets, tc.extra...)
			r := &clientResource{client: fake.start()}
			prior := managedModel()
			prior.GenerateSecret = types.BoolNull()
			prior.ClientSecretID = types.StringNull()
			resp, after := runRead(t, r, prior)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, types.BoolValue(true), after.GenerateSecret)
			assert.Equal(t, tc.want, after.ClientSecretID)
			assert.Equal(t, prior.ClientSecret, after.ClientSecret)
			assert.Equal(t, tc.lists, fake.called("GET /api/oidc/clients/c1/secrets"))

			// Once recorded (or for a client without a secret), no list is needed.
			_, again := runRead(t, r, after)
			if !tc.want.IsNull() {
				assert.Equal(t, tc.lists, fake.called("GET /api/oidc/clients/c1/secrets"))
			}
			assert.Equal(t, tc.want, again.ClientSecretID)
		})
	}
}

// With generate_secret = false a confidential client gets no secret from this
// resource, and the one Pocket ID 2.17 creates with it is still revoked.
func TestClientCreateWithoutSecret(t *testing.T) {
	fake := newFakePocketID(t, "2.17.0", &fakeClient{ID: "c1"})
	r := &clientResource{client: fake.start()}
	ctx := context.Background()
	s := clientSchema(t).Schema
	model := lifecycleModel()
	model.ClientID = types.StringNull()
	model.GenerateSecret = types.BoolValue(false)
	model.ClientSecret, model.ClientSecretID = types.StringNull(), types.StringNull()
	plan := tfsdk.Plan{Schema: s}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	requireNoSecret(t, resp.Diagnostics, "autosynthetic-server-created")
	var after clientResourceModel
	require.False(t, resp.State.Get(ctx, &after).HasError())
	assert.True(t, after.ClientSecret.IsNull())
	assert.True(t, after.ClientSecretID.IsNull())
	assert.Empty(t, fake.secrets, "the server-created secret is revoked and none is generated")
	assert.Zero(t, fake.called("POST /api/oidc/clients/c1/secrets"))
	assert.Equal(t, 1, fake.called("DELETE /api/oidc/clients/c1/secrets/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"))
}

// A generated secret's ID is recorded with its value.
func TestClientCreateRecordsSecretID(t *testing.T) {
	fake := newFakePocketID(t, "2.17.0", &fakeClient{ID: "c1"})
	r := &clientResource{client: fake.start()}
	ctx := context.Background()
	s := clientSchema(t).Schema
	model := lifecycleModel()
	plan := tfsdk.Plan{Schema: s}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var after clientResourceModel
	require.False(t, resp.State.Get(ctx, &after).HasError())
	require.Len(t, fake.secrets, 1)
	assert.Equal(t, fake.secrets[0].ID, after.ClientSecretID.ValueString())
	assert.Equal(t, "gen1synthetic-generated-secret-value", after.ClientSecret.ValueString())
}
