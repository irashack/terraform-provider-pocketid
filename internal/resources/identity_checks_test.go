package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// identityKey is the API key these tests authenticate with. It is UUID-shaped,
// as a static key may be, so an identifier equal to it passes every form check.
const identityKey = "5a5a5a5a-1111-4111-8111-111111111111"

// identityServer counts the requests it receives; none is expected.
func identityServer(t *testing.T) (*client.Client, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, identityKey, false, 5)
	require.NoError(t, err)
	return c, &requests
}

func requireRefusedQuietly(t *testing.T, diags diag.Diagnostics, requests *atomic.Int32) {
	t.Helper()
	require.True(t, diags.HasError())
	for _, d := range diags {
		assert.NotContains(t, d.Summary()+d.Detail(), identityKey)
		assert.NotContains(t, d.Summary()+d.Detail(), "1111-4111")
	}
	assert.Zero(t, requests.Load(), "nothing is sent")
}

// An identifier from configuration, state or an import ID that contains the
// API key is refused before any request, and no diagnostic names it: the
// diagnostics that explain a failure would otherwise repeat it.
func TestIdentifiersCarryingTheKeyAreRefusedBeforeUse(t *testing.T) {
	ctx := context.Background()
	const user = "aaaaaaaa-0000-4000-8000-0000000000a1"
	const group = "bbbbbbbb-0000-4000-8000-0000000000b1"

	t.Run("group membership create", func(t *testing.T) {
		c, requests := identityServer(t)
		r := &groupMembershipResource{client: c}
		sr := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		for _, model := range []groupMembershipResourceModel{
			{GroupID: types.StringValue(identityKey), UserID: types.StringValue(user)},
			{GroupID: types.StringValue(group), UserID: types.StringValue(identityKey)},
		} {
			model.ID, model.UnresolvedCreation = types.StringUnknown(), types.BoolUnknown()
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			requireRefusedQuietly(t, resp.Diagnostics, requests)
			assert.True(t, resp.State.Raw.IsNull())
		}
	})
	t.Run("group membership import", func(t *testing.T) {
		c, requests := identityServer(t)
		r := &groupMembershipResource{client: c}
		sr := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		for _, id := range []string{group + "/" + identityKey, identityKey + "/" + user, "not-an-id/" + user} {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
			r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
			requireRefusedQuietly(t, resp.Diagnostics, requests)
		}
	})
	t.Run("client secret create and import", func(t *testing.T) {
		c, requests := identityServer(t)
		config := clientSecretPlanned()
		config.ClientID = types.StringValue("app-" + identityKey)
		resp, state := clientSecretCreate(t, c, config)
		requireRefusedQuietly(t, resp.Diagnostics, requests)
		assert.Nil(t, state)

		r := &clientSecretResource{client: c}
		s := clientSecretTestSchema(t)
		imported := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: "app/" + identityKey}, &imported)
		requireRefusedQuietly(t, imported.Diagnostics, requests)
	})
	t.Run("user and client import", func(t *testing.T) {
		c, requests := identityServer(t)
		u := &userResource{client: c}
		sr := resource.SchemaResponse{}
		u.Schema(ctx, resource.SchemaRequest{}, &sr)
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
		u.ImportState(ctx, resource.ImportStateRequest{ID: identityKey}, &resp)
		requireRefusedQuietly(t, resp.Diagnostics, requests)

		cr := &clientResource{client: c}
		csr := resource.SchemaResponse{}
		cr.Schema(ctx, resource.SchemaRequest{}, &csr)
		cresp := resource.ImportStateResponse{State: tfsdk.State{Schema: csr.Schema, Raw: tftypes.NewValue(csr.Schema.Type().TerraformType(ctx), nil)}}
		cr.ImportState(ctx, resource.ImportStateRequest{ID: "app-" + identityKey}, &cresp)
		requireRefusedQuietly(t, cresp.Diagnostics, requests)
	})
}
