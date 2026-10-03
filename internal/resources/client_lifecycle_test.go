package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// clientNotFoundBody is Pocket ID's structured not-found error for an OIDC
// client (apperror.NotFound("OIDC client"), v2.14.0 to v2.17.0).
const clientNotFoundBody = `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"},"request_id":"r"}`

func TestClientPartialCreation(t *testing.T) {
	const existing = `{"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc"}`
	for _, tc := range []struct {
		name                                   string
		secretStatus, deleteStatus, readStatus int
		readBody                               string
		retained                               bool
		deletes                                int
		title, detail                          string
	}{
		{"rejected_rollback", 400, 204, 200, existing, false, 1, "OIDC client creation rolled back", ""},
		{"cleanup_failed", 400, 403, 200, existing, true, 1, "OIDC client cleanup failed", "A read found the client still exists."},
		// Only Pocket ID's own not-found error for the client confirms it is gone.
		{"cleanup_uncertain_confirmed_absent", 400, 503, 404, clientNotFoundBody, false, 1, "OIDC client rollback verified", ""},
		// Any other 404 keeps the ID in state, reported as unconfirmed.
		{"cleanup_uncertain_bare_404", 400, 503, 404, ``, true, 1, "OIDC client cleanup failed", "could not be confirmed"},
		{"cleanup_uncertain_proxy_404", 400, 503, 404, `<html>Not Found</html>`, true, 1, "OIDC client cleanup failed", "could not be confirmed"},
		{"cleanup_uncertain_missing_route", 400, 503, 404, `{"error":"API endpoint not found"}`, true, 1, "OIDC client cleanup failed", "could not be confirmed"},
		{"cleanup_uncertain_other_resource_404", 400, 503, 404, `{"error":"Client secret not found","code":"not_found","details":{"resource":"Client secret"}}`, true, 1, "OIDC client cleanup failed", "could not be confirmed"},
		{"secret_uncertain", 503, 204, 200, existing, true, 0, "OIDC client creation result uncertain", ""},
		{"secret_uncertain_read_failed", 503, 204, 403, existing, true, 0, "OIDC client creation result uncertain", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, deletes, reads := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /api/version/current":
					_, _ = fmt.Fprint(w, `{"currentVersion":"2.14.0"}`)
				case "POST /api/oidc/clients":
					_, _ = fmt.Fprint(w, `{"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","name":"fixture","callbackURLs":["https://example.invalid/callback"],"pkceEnabled":true}`)
				case "POST /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc/secrets":
					posts++
					w.WriteHeader(tc.secretStatus)
					_, _ = fmt.Fprint(w, `{"error":"synthetic-secret"}`)
				case "DELETE /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc":
					deletes++
					w.WriteHeader(tc.deleteStatus)
				case "GET /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc":
					reads++
					w.WriteHeader(tc.readStatus)
					_, _ = fmt.Fprint(w, tc.readBody)
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
			require.Equal(t, tc.title, response.Diagnostics[len(response.Diagnostics)-1].Summary())
			require.Contains(t, response.Diagnostics[len(response.Diagnostics)-1].Detail(), tc.detail)
			for _, d := range response.Diagnostics {
				require.NotContains(t, d.Detail(), "synthetic-secret")
				require.NotContains(t, d.Detail(), "synthetic-token")
			}
			if tc.retained {
				var state clientResourceModel
				require.False(t, response.State.Get(ctx, &state).HasError())
				require.Equal(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", state.ID.ValueString())
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
		LogoutCallbackURLs: types.ListNull(types.StringType), AllowedUserGroups: types.SetNull(types.StringType),
		FederatedIdentities: types.ListNull(types.ObjectType{AttrTypes: federatedIdentityAttrTypes}),
		IsPublic:            types.BoolValue(false), PkceEnabled: types.BoolValue(true), HasLogo: types.BoolUnknown(),
		RequiresReauthentication: types.BoolValue(false), RequiresPushedAuthorizationRequests: types.BoolValue(false),
		LaunchURL: types.StringNull(), ClientSecret: types.StringUnknown(), BackchannelLogoutURL: types.StringNull(),
		GenerateSecret: types.BoolValue(true), ClientSecretID: types.StringUnknown(), IsGroupRestricted: types.BoolUnknown(),
		Description: types.StringUnknown(), SkipConsent: types.BoolUnknown(), AccessTokenDurationMinutes: types.Int64Unknown(),
		RefreshTokenDurationMinutes: types.Int64Unknown(), HasDarkLogo: types.BoolUnknown(), ClientType: types.StringUnknown(),
		PkceSupported: types.BoolUnknown(),
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
					_, _ = fmt.Fprint(w, `{"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","name":"fixture","isPublic":true,"callbackURLs":["https://example.invalid/callback"],"pkceEnabled":true}`)
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

// Pocket ID 2.17.0 can return a secret it generated for the new client. The
// provider revokes it before generating its own, so the client ends with one
// secret, and a failed revoke is handled like a failed secret generation.
func TestClientCreateRevokesServerCreatedSecret(t *testing.T) {
	const created = `{"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","name":"fixture","callbackURLs":["https://example.invalid/callback"],"pkceEnabled":true%s,"createdSecret":{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","prefix":"synt","secret":"synthetic-auto-secret"}}`
	for _, tc := range []struct {
		name           string
		public         bool
		createdID      string // "" sends createdSecret without an id
		revokeStatus   int
		listStatus     int
		listBody       string
		wantError      bool
		wantRevokes    int
		wantSecretPost int
		wantDeletes    int
		retained       bool
		// Cleanup of the new client after a rejected revoke: the DELETE's
		// status (0 means 204) and what the verification read returns.
		cleanupStatus int
		readStatus    int
		readBody      string
		wantTitle     string
	}{
		{name: "revoked_then_generated", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 204, wantRevokes: 1, wantSecretPost: 1},
		{name: "public_client_revoked_without_generation", public: true, createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 204, wantRevokes: 1},
		{name: "revoke_rejected_rolls_back", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 403, listStatus: 200, listBody: `[{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}]`, wantError: true, wantRevokes: 1, wantDeletes: 1},
		{name: "revoke_rejected_list_failed_rolls_back", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 404, listStatus: 403, wantError: true, wantRevokes: 1, wantDeletes: 1},
		{name: "revoke_uncertain_retained", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 503, listStatus: 200, listBody: `[{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}]`, wantError: true, wantRevokes: 1, retained: true},
		{name: "revoke_uncertain_but_confirmed_gone", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 503, listStatus: 200, listBody: `[]`, wantRevokes: 1, wantSecretPost: 1},
		{name: "revoke_404_confirmed_gone", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 404, listStatus: 200, listBody: `[{"id":"other"}]`, wantRevokes: 1, wantSecretPost: 1},
		{name: "unidentified_secret_rolls_back", createdID: "", wantError: true, wantDeletes: 1},
		// Revoke rejected, then the rollback DELETE fails: only Pocket ID's own
		// not-found error proves the client (and its secret) is gone.
		{name: "revoke_rejected_cleanup_failed_confirmed_absent", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 403, listStatus: 200, listBody: `[{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}]`,
			cleanupStatus: 503, readStatus: 404, readBody: clientNotFoundBody, wantError: true, wantRevokes: 1, wantDeletes: 1, wantTitle: "OIDC client rollback verified"},
		{name: "revoke_rejected_cleanup_failed_bare_404_retained", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 403, listStatus: 200, listBody: `[{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}]`,
			cleanupStatus: 503, readStatus: 404, readBody: ``, wantError: true, wantRevokes: 1, wantDeletes: 1, retained: true, wantTitle: "OIDC client cleanup failed"},
		{name: "revoke_rejected_cleanup_failed_proxy_404_retained", createdID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", revokeStatus: 403, listStatus: 200, listBody: `[{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}]`,
			cleanupStatus: 503, readStatus: 404, readBody: `<html>Not Found</html>`, wantError: true, wantRevokes: 1, wantDeletes: 1, retained: true, wantTitle: "OIDC client cleanup failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			revokes, secretPosts, deletes := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /api/version/current":
					_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
				case "POST /api/oidc/clients":
					body := fmt.Sprintf(created, fmt.Sprintf(`,"isPublic":%t`, tc.public))
					if tc.createdID == "" {
						body = strings.Replace(body, `"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",`, "", 1)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprint(w, body)
				case "DELETE /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc/secrets/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa":
					revokes++
					order = append(order, "revoke")
					w.WriteHeader(tc.revokeStatus)
				case "GET /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc/secrets":
					w.WriteHeader(tc.listStatus)
					_, _ = fmt.Fprint(w, tc.listBody)
				case "POST /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc/secrets":
					secretPosts++
					order = append(order, "generate")
					w.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprint(w, `{"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","secret":"synthetic-managed-secret"}`)
				case "DELETE /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc":
					deletes++
					if tc.cleanupStatus != 0 {
						w.WriteHeader(tc.cleanupStatus)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case "GET /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc":
					if tc.readStatus != 0 {
						w.WriteHeader(tc.readStatus)
						_, _ = fmt.Fprint(w, tc.readBody)
						return
					}
					_, _ = fmt.Fprint(w, `{"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc"}`)
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
			model.IsPublic = types.BoolValue(tc.public)
			plan := tfsdk.Plan{Schema: schemaResp.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &response)

			require.Equal(t, tc.wantError, response.Diagnostics.HasError(), "%v", response.Diagnostics)
			require.Equal(t, tc.wantRevokes, revokes, "the revoke is never retried")
			require.Equal(t, tc.wantSecretPost, secretPosts)
			require.Equal(t, tc.wantDeletes, deletes)
			if tc.wantTitle != "" {
				last := response.Diagnostics[len(response.Diagnostics)-1]
				require.Equal(t, tc.wantTitle, last.Summary())
				if tc.retained {
					require.Contains(t, last.Detail(), "could not be confirmed")
					require.Contains(t, last.Detail(), "may still be valid")
				}
			}
			if tc.wantRevokes > 0 && tc.wantSecretPost > 0 {
				require.Equal(t, []string{"revoke", "generate"}, order)
			}
			for _, d := range response.Diagnostics {
				require.NotContains(t, d.Detail(), "synthetic-auto-secret")
				require.NotContains(t, d.Detail(), "synthetic-managed-secret")
				require.NotContains(t, d.Detail(), "synthetic-token")
				if tc.createdID != "" {
					require.Contains(t, d.Detail(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "the diagnostic names the secret left behind")
				}
			}
			switch {
			case !tc.wantError:
				var state clientResourceModel
				require.False(t, response.State.Get(ctx, &state).HasError())
				require.Equal(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", state.ID.ValueString())
				if tc.public {
					require.True(t, state.ClientSecret.IsNull())
				} else {
					require.Equal(t, "synthetic-managed-secret", state.ClientSecret.ValueString())
				}
			case tc.retained:
				var state clientResourceModel
				require.False(t, response.State.Get(ctx, &state).HasError())
				require.Equal(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", state.ID.ValueString())
				require.True(t, state.ClientSecret.IsNull())
			default:
				require.True(t, response.State.Raw.IsNull())
			}
		})
	}
}

// A rejected allowed-groups update after the provider generated its secret
// rolls the client back. If that cleanup fails and the read cannot confirm the
// client is gone, the ID and the generated secret stay in state, so neither the
// client nor its credential is orphaned.
func TestClientCreateGroupFailureCleanupUnconfirmed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readBody string
		retained bool
		title    string
	}{
		{"confirmed_absent", clientNotFoundBody, false, "OIDC client rollback verified"},
		{"bare_404_retained", ``, true, "OIDC client cleanup failed"},
		{"missing_route_retained", `{"error":"API endpoint not found"}`, true, "OIDC client cleanup failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deletes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /api/version/current":
					_, _ = fmt.Fprint(w, `{"currentVersion":"2.16.0"}`)
				case "POST /api/oidc/clients":
					w.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprint(w, `{"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","name":"fixture","callbackURLs":["https://example.invalid/callback"],"pkceEnabled":true}`)
				case "POST /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc/secrets":
					w.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprint(w, `{"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","secret":"synthetic-managed-secret"}`)
				case "PUT /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc/allowed-user-groups":
					w.WriteHeader(http.StatusBadRequest)
				case "DELETE /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc":
					deletes++
					w.WriteHeader(http.StatusServiceUnavailable)
				case "GET /api/oidc/clients/cccccccc-cccc-4ccc-8ccc-cccccccccccc":
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, tc.readBody)
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
			model.AllowedUserGroups = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("group-1")})
			plan := tfsdk.Plan{Schema: schemaResp.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &response)

			require.True(t, response.Diagnostics.HasError())
			require.Equal(t, 1, deletes, "the cleanup DELETE is never retried")
			last := response.Diagnostics[len(response.Diagnostics)-1]
			require.Equal(t, tc.title, last.Summary())
			require.NotContains(t, last.Detail(), "synthetic-managed-secret")
			if !tc.retained {
				require.True(t, response.State.Raw.IsNull())
				return
			}
			require.Contains(t, last.Detail(), "could not be confirmed")
			var state clientResourceModel
			require.False(t, response.State.Get(ctx, &state).HasError())
			require.Equal(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", state.ID.ValueString())
			require.Equal(t, "synthetic-managed-secret", state.ClientSecret.ValueString(), "the generated secret is kept, not orphaned")
		})
	}
}
