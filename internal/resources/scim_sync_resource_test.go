package resources_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/resources"
)

func scimSyncResource(t *testing.T, c *client.Client) resource.Resource {
	t.Helper()
	r := resources.NewScimSyncResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	return r
}

func scimSyncCreate(t *testing.T, r resource.Resource, sch schema.Schema, serviceProviderID string) *resource.CreateResponse {
	t.Helper()
	plan := tfsdk.Plan{Schema: sch, Raw: scimObject(t, sch, map[string]any{
		"service_provider_id": serviceProviderID,
		"id":                  tftypes.UnknownValue, "synced_at": tftypes.UnknownValue,
	})}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)
	return resp
}

func TestScimSyncResource_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	resources.NewScimSyncResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "pocketid"}, resp)
	assert.Equal(t, "pocketid_scim_sync", resp.TypeName)
}

func TestScimSyncResource_Schema(t *testing.T) {
	sch := scimSchema(t, resources.NewScimSyncResource())

	assert.Contains(t, sch.Description, "SCIM")
	assert.True(t, sch.Attributes["id"].IsComputed())
	assert.True(t, sch.Attributes["synced_at"].IsComputed())

	provider, ok := sch.Attributes["service_provider_id"].(schema.StringAttribute)
	require.True(t, ok)
	assert.True(t, provider.Required)
	assert.NotEmpty(t, provider.PlanModifiers, "a different service provider needs a new sync")
	assert.NotEmpty(t, provider.Validators)

	triggers, ok := sch.Attributes["triggers"].(schema.MapAttribute)
	require.True(t, ok)
	assert.True(t, triggers.Optional)
	assert.False(t, triggers.Computed)
	assert.NotEmpty(t, triggers.PlanModifiers, "changing triggers must run a new sync")
}

func TestScimSyncResource_Configure(t *testing.T) {
	r, ok := resources.NewScimSyncResource().(resource.ResourceWithConfigure)
	require.True(t, ok)

	resp := &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	assert.False(t, resp.Diagnostics.HasError())

	resp = &resource.ConfigureResponse{}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "invalid"}, resp)
	assert.True(t, resp.Diagnostics.HasError())
}

func TestScimSyncResource_CreateRunsOneSyncAndRecordsIt(t *testing.T) {
	fake, c := newScimFake(t)
	r := scimSyncResource(t, c)
	sch := scimSchema(t, r)

	before := time.Now().UTC().Add(-time.Second)
	resp := scimSyncCreate(t, r, sch, scimTestProviderID)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	mutations := fake.mutations()
	require.Len(t, mutations, 1)
	assert.Equal(t, http.MethodPost, mutations[0].Method)
	assert.Equal(t, "/api/scim/service-provider/"+scimTestProviderID+"/sync", mutations[0].Path)

	assert.Equal(t, types.StringValue(scimTestProviderID), scimStateString(t, resp.State, "id"))
	syncedAt, err := time.Parse(time.RFC3339, scimStateString(t, resp.State, "synced_at").ValueString())
	require.NoError(t, err)
	assert.True(t, syncedAt.After(before))
}

// A sync is never repeated: a failure is reported once, with what is known
// about it, and nothing is recorded in state.
func TestScimSyncResource_CreateReportsEachFailureOnceAndRecordsNothing(t *testing.T) {
	cases := []struct {
		name    string
		failure scimFailure
		want    string
	}{
		{"the SCIM endpoint fails", scimFailure{500, `{"error":"scim request failed with status 502: secret-body"}`}, "server error"},
		{"the service provider is gone", scimFailure{404, scimNotFoundBody}, "no longer exists"},
		{"rate limited", scimFailure{429, `{"error":"slow down"}`}, "429"},
		{"a rejected key", scimFailure{403, `{"error":"no","code":"missing_permission"}`}, "403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, c := newScimFake(t)
			failure := tc.failure
			fake.failWith = &failure
			r := scimSyncResource(t, c)
			sch := scimSchema(t, r)

			resp := scimSyncCreate(t, r, sch, scimTestProviderID)
			require.True(t, resp.Diagnostics.HasError())
			assert.Len(t, fake.recorded(), 1, "a sync must not be sent twice")
			assert.True(t, resp.State.Raw.IsNull(), "a failed sync records nothing")
			for _, d := range resp.Diagnostics {
				assert.Contains(t, d.Detail(), tc.want)
				assert.NotContains(t, d.Detail(), "secret-body", "a response body is never echoed")
			}
		})
	}
}

func TestScimSyncResource_CreateWithNoAnswerSaysTheOutcomeIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hang up without answering.
		hijacker, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := hijacker.Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	r := scimSyncResource(t, c)
	sch := scimSchema(t, r)

	resp := scimSyncCreate(t, r, sch, scimTestProviderID)
	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "may still be running")
	assert.True(t, resp.State.Raw.IsNull())
}

func TestScimSyncResource_CreateRefusesAnIdentifierThatIsNotAUUIDWithoutARequest(t *testing.T) {
	fake, c := newScimFake(t)
	r := scimSyncResource(t, c)
	sch := scimSchema(t, r)

	resp := scimSyncCreate(t, r, sch, "../oidc/clients")
	require.True(t, resp.Diagnostics.HasError())
	assert.Empty(t, fake.recorded())
}

func TestScimSyncResource_ReadKeepsStateUpdateFailsAndDeleteIsLocal(t *testing.T) {
	fake, c := newScimFake(t)
	r := scimSyncResource(t, c)
	sch := scimSchema(t, r)
	values := map[string]any{
		"id": scimTestProviderID, "service_provider_id": scimTestProviderID,
		"synced_at": "2026-01-01T00:00:00Z",
	}
	state := scimState(t, sch, values)

	read := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, read)
	require.False(t, read.Diagnostics.HasError())
	assert.True(t, state.Raw.Equal(read.State.Raw), "a sync has nothing to refresh")

	update := &resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: sch, Raw: state.Raw}}, update)
	assert.True(t, update.Diagnostics.HasError())

	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resource.DeleteResponse{})
	assert.Empty(t, fake.recorded(), "none of these may reach Pocket ID")
}
