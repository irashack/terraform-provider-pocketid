package resources

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// logKey is the API key of these tests, which the configured text below
// carries by mistake.
const logKey = "Synthetic-Log-Key-0123456789"

// logServer answers the version request and refuses anything else: none of
// the creates below may send a request whose body carries the key.
func logServer(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/version/current" {
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, logKey, false, 5)
	require.NoError(t, err)
	return c
}

// No configured text reaches the provider's log, at any level: a username,
// e-mail address, name, friendly name, callback URL or SCIM endpoint that
// carries the API key by mistake is never logged, and the request that would
// carry it is not sent.
func TestCreateLogsNoConfiguredText(t *testing.T) {
	for name, create := range map[string]func(ctx context.Context, t *testing.T, c *client.Client) resource.CreateResponse{
		"pocketid_user": func(ctx context.Context, t *testing.T, c *client.Client) resource.CreateResponse {
			r := &userResource{client: c}
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			model := defaultsPlanModel(types.SetNull(types.StringType), types.MapNull(types.StringType))
			model.Username = types.StringValue("u" + logKey)
			model.Email = types.StringValue(logKey + "@example.invalid")
			model.FirstName, model.LastName = types.StringValue(logKey), types.StringValue(logKey)
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			return resp
		},
		"pocketid_group": func(ctx context.Context, t *testing.T, c *client.Client) resource.CreateResponse {
			r := &groupResource{client: c}
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			model := groupResourceModel{
				ID: types.StringUnknown(), Name: types.StringValue("g" + logKey), FriendlyName: types.StringValue(logKey),
				CustomClaims: types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("a")}),
			}
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			return resp
		},
		"pocketid_client": func(ctx context.Context, t *testing.T, c *client.Client) resource.CreateResponse {
			r := &clientResource{client: c}
			s := clientSchema(t).Schema
			model := lifecycleModel()
			model.Name = types.StringValue("app " + logKey)
			model.CallbackURLs = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("https://app.example.invalid/" + logKey)})
			model.IsPublic = types.BoolValue(true)
			model.GenerateSecret = types.BoolValue(false)
			plan := tfsdk.Plan{Schema: s}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			return resp
		},
		"pocketid_scim_service_provider": func(ctx context.Context, t *testing.T, c *client.Client) resource.CreateResponse {
			r := &scimServiceProviderResource{client: c}
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			model := scimServiceProviderResourceModel{
				ID: types.StringUnknown(), ClientID: types.StringValue("app"), Endpoint: types.StringValue("https://scim.example.invalid/" + logKey),
				Token: types.StringValue("scim-token"), TokenWO: types.StringNull(), TokenWOVer: types.StringNull(),
				LastSyncedAt: types.StringUnknown(), CreatedAt: types.StringUnknown(),
			}
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			config := model
			config.ID, config.LastSyncedAt, config.CreatedAt = types.StringNull(), types.StringNull(), types.StringNull()
			cfg := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, cfg.Set(ctx, &config).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan, Config: tfsdk.Config{Schema: sr.Schema, Raw: cfg.Raw}}, &resp)
			return resp
		},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)
			resp := create(ctx, t, logServer(t))
			require.True(t, resp.Diagnostics.HasError(), "the request carrying the key is refused")
			assert.NotEmpty(t, logs.String(), "the create logs at debug level")
			assert.NotContains(t, logs.String(), logKey)
			for _, d := range resp.Diagnostics {
				assert.NotContains(t, d.Summary()+d.Detail(), logKey)
			}
		})
	}
}
