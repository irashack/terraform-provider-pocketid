package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

func TestClientPartialCreation(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		secretStatus, deleteStatus, readStatus int
		retained                               bool
		deletes                                int
	}{
		{"rejected_rollback", 400, 204, 200, false, 1},
		{"cleanup_failed", 400, 403, 200, true, 1},
		{"cleanup_uncertain_absent", 400, 503, 404, false, 1},
		{"secret_uncertain", 503, 204, 200, true, 0},
		{"secret_uncertain_read_failed", 503, 204, 403, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, deletes, reads := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /api/version/current":
					_, _ = fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
				case "POST /api/oidc/clients":
					_, _ = fmt.Fprint(w, `{"id":"new-fixture","name":"fixture","callbackURLs":["https://example.invalid/callback"],"pkceEnabled":true}`)
				case "POST /api/oidc/clients/new-fixture/secrets":
					posts++
					w.WriteHeader(tc.secretStatus)
					_, _ = fmt.Fprint(w, `{"error":"synthetic-secret"}`)
				case "DELETE /api/oidc/clients/new-fixture":
					deletes++
					w.WriteHeader(tc.deleteStatus)
				case "GET /api/oidc/clients/new-fixture":
					reads++
					w.WriteHeader(tc.readStatus)
					_, _ = fmt.Fprint(w, `{"id":"new-fixture"}`)
				default:
					t.Errorf("unexpected method/path %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			c, _ := client.NewClient(server.URL, "synthetic-token", false, 1)
			r := &clientResource{client: c}
			ctx := context.Background()
			schemaResp := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
			model := lifecycleModel()
			plan := tfsdk.Plan{Schema: schemaResp.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &response)
			require.True(t, response.Diagnostics.HasError())
			require.Equal(t, 1, posts)
			require.Equal(t, tc.deletes, deletes)
			for _, d := range response.Diagnostics {
				require.NotContains(t, d.Detail(), "synthetic-secret")
				require.NotContains(t, d.Detail(), "synthetic-token")
			}
			if tc.retained {
				var state clientResourceModel
				require.False(t, response.State.Get(ctx, &state).HasError())
				require.Equal(t, "new-fixture", state.ID.ValueString())
				require.True(t, state.ClientSecret.IsNull())
				require.Greater(t, reads, 0)
			} else {
				require.True(t, response.State.Raw.IsNull())
			}
		})
	}
}

func lifecycleModel() clientResourceModel {
	return clientResourceModel{
		Name: types.StringValue("fixture"), ClientID: types.StringNull(), ID: types.StringUnknown(),
		CallbackURLs:       types.ListValueMust(types.StringType, []attr.Value{types.StringValue("https://example.invalid/callback")}),
		LogoutCallbackURLs: types.ListNull(types.StringType), AllowedUserGroups: types.ListNull(types.StringType),
		FederatedIdentities: types.ListNull(types.ObjectType{AttrTypes: federatedIdentityAttrTypes}),
		IsPublic:            types.BoolValue(false), PkceEnabled: types.BoolValue(true), HasLogo: types.BoolUnknown(),
		RequiresReauthentication: types.BoolValue(false), RequiresPushedAuthorizationRequests: types.BoolValue(false),
		LaunchURL: types.StringNull(), ClientSecret: types.StringUnknown(),
	}
}

func TestClientCreateGuards(t *testing.T) {
	for _, scenario := range []string{"public", "version_failure", "existing_id", "uncertain_fixed_id"} {
		t.Run(scenario, func(t *testing.T) {
			posts, secrets, deletes, versionReads, clientReads := 0, 0, 0, 0, 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch {
				case req.URL.Path == "/api/version/current":
					versionReads++
					if scenario == "version_failure" {
						w.WriteHeader(403)
						return
					}
					_, _ = fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
				case req.Method == "GET":
					clientReads++
					if scenario == "uncertain_fixed_id" && clientReads == 1 {
						w.WriteHeader(404)
						return
					}
					_, _ = fmt.Fprint(w, `{"id":"fixed-fixture"}`)
				case req.Method == "POST" && req.URL.Path == "/api/oidc/clients":
					posts++
					if scenario == "uncertain_fixed_id" {
						w.WriteHeader(503)
						return
					}
					_, _ = fmt.Fprint(w, `{"id":"new-fixture","name":"fixture","isPublic":true,"callbackURLs":["https://example.invalid/callback"],"pkceEnabled":true}`)
				case req.Method == "POST":
					secrets++
					w.WriteHeader(400)
				case req.Method == "DELETE":
					deletes++
					w.WriteHeader(204)
				}
			}))
			defer s.Close()
			c, _ := client.NewClient(s.URL, "fixture-token", false, 1)
			r := &clientResource{client: c}
			ctx := context.Background()
			sr := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			m := lifecycleModel()
			if scenario == "public" {
				m.IsPublic = types.BoolValue(true)
			}
			if scenario == "existing_id" || scenario == "uncertain_fixed_id" {
				m.ClientID = types.StringValue("fixed-fixture")
			}
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, &m).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			require.Zero(t, secrets)
			require.Zero(t, deletes)
			switch scenario {
			case "public":
				require.False(t, resp.Diagnostics.HasError())
				require.Equal(t, 1, posts)
				require.Zero(t, versionReads)
			case "uncertain_fixed_id":
				require.True(t, resp.Diagnostics.HasError())
				require.Equal(t, 1, posts)
				require.Equal(t, 2, clientReads)
				var state clientResourceModel
				require.False(t, resp.State.Get(ctx, &state).HasError())
				require.Equal(t, "fixed-fixture", state.ID.ValueString())
			default:
				require.True(t, resp.Diagnostics.HasError())
				require.Zero(t, posts)
			}
		})
	}
}
