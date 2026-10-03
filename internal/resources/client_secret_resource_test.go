package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	clientSecretTestID    = "99999999-9999-4999-8999-999999999999"
	clientSecretTestOther = "88888888-8888-4888-8888-888888888888"
	clientSecretTestValue = "wo-value-for-unit-tests-0123"
	clientSecretTestGen   = "GENERATEDgenerated0123456789abcd"
)

// clientSecretFake is a Pocket ID that serves one client, "app", and its
// secrets list.
type clientSecretFake struct {
	mu            *sync.Mutex
	version       string // "" answers the router's 404 for the version route
	public        bool
	clientMissing bool
	listed        []string // secret objects listed before any POST
	postStatus    int
	postBody      string
	appearOnPost  string // a secret object listed after the POST
	deleteStatus  int
	listAfterDel  []string
	posts         int
	deletes       int
	deletedIDs    []string // DELETEs of secrets other than clientSecretTestID
	postedBody    string
}

func clientSecretObject(id, prefix string) string {
	return `{"id":"` + id + `","prefix":"` + prefix + `","createdAt":"2026-10-02T10:00:00.5Z","expiresAt":null,"isActive":true}`
}

func (f *clientSecretFake) serve(t *testing.T) *client.Client {
	t.Helper()
	f.mu = &sync.Mutex{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		missingClient := `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`
		switch r.Method + " " + r.URL.Path {
		case "GET /api/version/current":
			if f.version == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"error":"API endpoint not found"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"currentVersion":%q}`, f.version)
		case "GET /api/oidc/clients/app":
			if f.clientMissing {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, missingClient)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":"app","name":"app","callbackURLs":[],"isPublic":%t}`, f.public)
		case "GET /api/oidc/clients/app/secrets":
			if f.clientMissing {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, missingClient)
				return
			}
			_, _ = fmt.Fprint(w, "["+strings.Join(f.listed, ",")+"]")
		case "POST /api/oidc/clients/app/secrets":
			f.posts++
			body, _ := io.ReadAll(r.Body)
			f.postedBody = string(body)
			if f.appearOnPost != "" {
				f.listed = append(f.listed, f.appearOnPost)
			}
			w.WriteHeader(f.postStatus)
			_, _ = fmt.Fprint(w, f.postBody)
		case "DELETE /api/oidc/clients/app/secrets/" + clientSecretTestID:
			f.deletes++
			if f.listAfterDel != nil {
				f.listed = f.listAfterDel
			}
			w.WriteHeader(f.deleteStatus)
		default:
			if id, ok := strings.CutPrefix(r.URL.Path, "/api/oidc/clients/app/secrets/"); ok && r.Method == http.MethodDelete {
				f.deletedIDs = append(f.deletedIDs, id)
				kept := f.listed[:0]
				for _, secret := range f.listed {
					if !strings.Contains(secret, `"id":"`+id+`"`) {
						kept = append(kept, secret)
					}
				}
				f.listed = kept
				w.WriteHeader(http.StatusNoContent)
				return
			}
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)
	return c
}

func clientSecretTestSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := resource.SchemaResponse{}
	(&clientSecretResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

func clientSecretPlanned() clientSecretResourceModel {
	return clientSecretResourceModel{
		ID: types.StringUnknown(), ClientID: types.StringValue("app"), ExpiresAt: types.StringNull(),
		Secret: types.StringUnknown(), SecretWO: types.StringNull(), SecretWOVersion: types.StringNull(),
		Prefix: types.StringUnknown(), CreatedAt: types.StringUnknown(), IsActive: types.BoolUnknown(),
	}
}

// clientSecretCreate runs Create with config as the configuration (its
// write-only value included) and the plan derived from it.
func clientSecretCreate(t *testing.T, c *client.Client, config clientSecretResourceModel) (resource.CreateResponse, *clientSecretResourceModel) {
	t.Helper()
	ctx := context.Background()
	s := clientSecretTestSchema(t)
	configured := tfsdk.Plan{Schema: s}
	require.False(t, configured.Set(ctx, &config).HasError())
	planned := config
	planned.SecretWO = types.StringNull()
	plan := tfsdk.Plan{Schema: s}
	require.False(t, plan.Set(ctx, &planned).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
	(&clientSecretResource{client: c}).Create(ctx, resource.CreateRequest{Plan: plan, Config: tfsdk.Config{Schema: s, Raw: configured.Raw}}, &resp)
	if resp.State.Raw.IsNull() {
		return resp, nil
	}
	var state clientSecretResourceModel
	require.False(t, resp.State.Get(ctx, &state).HasError())
	return resp, &state
}

func clientSecretDiagText(diags diag.Diagnostics) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary() + ": " + d.Detail() + "\n")
	}
	return b.String()
}

func TestClientSecretResource_Schema(t *testing.T) {
	s := clientSecretTestSchema(t)
	wo := s.Attributes["secret_wo"].(schema.StringAttribute)
	assert.True(t, wo.WriteOnly)
	assert.True(t, wo.Sensitive)
	assert.False(t, wo.Computed)
	secret := s.Attributes["secret"].(schema.StringAttribute)
	assert.True(t, secret.Sensitive)
	assert.True(t, secret.Computed)
	assert.False(t, secret.Optional, "the value is never configured in plain text")
	for _, name := range []string{"client_id", "secret_wo_version"} {
		assert.NotEmpty(t, s.Attributes[name].(schema.StringAttribute).PlanModifiers, name+" requires replacement")
	}
	var resp resource.MetadataResponse
	(&clientSecretResource{}).Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "pocketid"}, &resp)
	assert.Equal(t, "pocketid_client_secret", resp.TypeName)
}

// Nothing is created when the server cannot hold several secrets, when the
// client is public or missing, or when it already holds the most secrets
// Pocket ID allows.
func TestClientSecretResource_CreateGuards(t *testing.T) {
	full := make([]string, 0, client.MaxClientSecrets)
	for i := 0; i < client.MaxClientSecrets; i++ {
		full = append(full, clientSecretObject(fmt.Sprintf("00000000-0000-4000-8000-%012d", i), fmt.Sprintf("p%03d", i)))
	}
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	for name, tc := range map[string]struct {
		fake    clientSecretFake
		expires string
		want    []string
	}{
		"before 2.14":         {fake: clientSecretFake{version: "2.13.0"}, want: []string{"Pocket ID version not supported", "2.14.0"}},
		"no version endpoint": {fake: clientSecretFake{}, want: []string{"Pocket ID version not supported"}},
		"public client":       {fake: clientSecretFake{version: "2.17.0", public: true}, want: []string{"Public clients have no secrets"}},
		"client missing":      {fake: clientSecretFake{version: "2.17.0", clientMissing: true}, want: []string{"OIDC client not found"}},
		"limit reached": {fake: clientSecretFake{version: "2.16.0", listed: full},
			want: []string{"Client secret limit reached", "holds 20 secrets", "at most 20", "00000000-0000-4000-8000-000000000019  prefix p019"}},
		"past expiry": {fake: clientSecretFake{version: "2.17.0"}, expires: past, want: []string{"not in the future"}},
	} {
		t.Run(name, func(t *testing.T) {
			fake := tc.fake
			config := clientSecretPlanned()
			if tc.expires != "" {
				config.ExpiresAt = types.StringValue(tc.expires)
			}
			resp, state := clientSecretCreate(t, fake.serve(t), config)
			require.True(t, resp.Diagnostics.HasError())
			text := clientSecretDiagText(resp.Diagnostics)
			for _, want := range tc.want {
				assert.Contains(t, text, want)
			}
			assert.Zero(t, fake.posts, "no secret POST")
			assert.Nil(t, state)
		})
	}
}

func TestClientSecretResource_CreateGenerated(t *testing.T) {
	fake := clientSecretFake{version: "2.16.0", listed: []string{clientSecretObject(clientSecretTestOther, "unma")}, postStatus: http.StatusCreated,
		postBody: `{"id":"` + clientSecretTestID + `","prefix":"GENE","createdAt":"2026-10-02T10:00:00.123Z","expiresAt":null,"isActive":true,"secret":"` + clientSecretTestGen + `"}`}
	resp, state := clientSecretCreate(t, fake.serve(t), clientSecretPlanned())
	require.False(t, resp.Diagnostics.HasError(), clientSecretDiagText(resp.Diagnostics))
	require.NotNil(t, state)
	assert.Equal(t, 1, fake.posts)
	assert.Empty(t, fake.postedBody, "a generated secret without expiry sends no body")
	assert.Equal(t, clientSecretTestID, state.ID.ValueString())
	assert.Equal(t, clientSecretTestGen, state.Secret.ValueString())
	assert.Equal(t, "GENE", state.Prefix.ValueString())
	assert.Equal(t, "2026-10-02T10:00:00.123Z", state.CreatedAt.ValueString())
	assert.True(t, state.IsActive.ValueBool())
	assert.True(t, state.ExpiresAt.IsNull())
	assert.Zero(t, fake.deletes, "the client's other secret is never touched")
}

func TestClientSecretResource_CreateWriteOnly(t *testing.T) {
	expires := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	configured := expires.In(time.FixedZone("", 2*3600)).Format(time.RFC3339)
	response := func(value, expiresAt string) string {
		return `{"id":"` + clientSecretTestID + `","prefix":"` + value[:4] + `","createdAt":"2026-10-02T10:00:00Z","expiresAt":` + expiresAt + `,"isActive":true,"secret":"` + value + `"}`
	}
	serverExpiry := `"` + expires.UTC().Format(time.RFC3339) + `"`
	config := clientSecretPlanned()
	config.SecretWO = types.StringValue(clientSecretTestValue)
	config.SecretWOVersion = types.StringValue("1")
	config.ExpiresAt = types.StringValue(configured)
	config.Secret = types.StringNull()

	t.Run("value stays out of state", func(t *testing.T) {
		fake := clientSecretFake{version: "2.17.0", postStatus: http.StatusCreated, postBody: response(clientSecretTestValue, serverExpiry)}
		resp, state := clientSecretCreate(t, fake.serve(t), config)
		require.False(t, resp.Diagnostics.HasError(), clientSecretDiagText(resp.Diagnostics))
		var sent struct {
			Secret    string    `json:"secret"`
			ExpiresAt time.Time `json:"expiresAt"`
		}
		require.NoError(t, json.Unmarshal([]byte(fake.postedBody), &sent))
		assert.Equal(t, clientSecretTestValue, sent.Secret)
		assert.True(t, sent.ExpiresAt.Equal(expires))
		assert.True(t, state.Secret.IsNull())
		assert.True(t, state.SecretWO.IsNull())
		assert.Equal(t, configured, state.ExpiresAt.ValueString(), "the configured spelling is kept")
		assert.NotContains(t, resp.State.Raw.String(), clientSecretTestValue)
	})
	for name, body := range map[string]string{
		"server ignored the value":  response("SERVERchoseADifferentValue0123", serverExpiry),
		"server ignored the expiry": response(clientSecretTestValue, "null"),
	} {
		t.Run(name, func(t *testing.T) {
			fake := clientSecretFake{version: "2.17.0", postStatus: http.StatusCreated, postBody: body}
			resp, state := clientSecretCreate(t, fake.serve(t), config)
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, clientSecretDiagText(resp.Diagnostics), "not created as planned")
			require.NotNil(t, state, "the created secret's identity is kept, so the next apply replaces it")
			assert.Equal(t, clientSecretTestID, state.ID.ValueString())
			assert.True(t, state.Secret.IsNull(), "no value is stored for a write-only secret")
			assert.NotContains(t, resp.State.Raw.String(), "SERVERchose")
			assert.NotContains(t, clientSecretDiagText(resp.Diagnostics), clientSecretTestValue)
		})
	}
}

// A create whose outcome is uncertain is not retried, keeps nothing it cannot
// name, and lists the client's secrets (IDs and prefixes, never values).
func TestClientSecretResource_CreateUncertain(t *testing.T) {
	before := clientSecretObject(clientSecretTestOther, "olde")
	appeared := clientSecretObject(clientSecretTestID, "newp")
	for name, tc := range map[string]struct {
		status   int
		body     string
		appear   string
		want     []string
		retained bool
	}{
		"server failure, one secret appeared": {status: 503, body: `{"error":"synthetic-secret"}`, appear: appeared,
			want: []string{"result uncertain", "not retried", clientSecretTestID + "  prefix newp", "[new since this attempt]", clientSecretTestOther + "  prefix olde", "app/<secret_id>"}},
		"server failure, nothing appeared": {status: 503, body: ``,
			want: []string{"result uncertain", "No new secret is listed"}},
		"unreadable response": {status: 201, body: `{"secret":"synthetic-secret"`, appear: appeared,
			want: []string{"result uncertain", "[new since this attempt]"}},
		"identity without value": {status: 201, body: `{"id":"` + clientSecretTestID + `","prefix":"newp","createdAt":"2026-10-02T10:00:00Z","isActive":true}`,
			want: []string{"Client secret value not returned", clientSecretTestID}, retained: true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := clientSecretFake{version: "2.17.0", listed: []string{before}, postStatus: tc.status, postBody: tc.body, appearOnPost: tc.appear}
			resp, state := clientSecretCreate(t, fake.serve(t), clientSecretPlanned())
			require.True(t, resp.Diagnostics.HasError())
			text := clientSecretDiagText(resp.Diagnostics)
			for _, want := range tc.want {
				assert.Contains(t, text, want)
			}
			assert.NotContains(t, text, "synthetic-secret")
			assert.NotContains(t, text, "synthetic-token")
			assert.Equal(t, 1, fake.posts, "never retried")
			if tc.retained {
				require.NotNil(t, state)
				assert.Equal(t, clientSecretTestID, state.ID.ValueString())
				assert.True(t, state.Secret.IsNull())
			} else {
				assert.Nil(t, state)
			}
		})
	}
	t.Run("definite rejection at the limit", func(t *testing.T) {
		full := make([]string, 0, client.MaxClientSecrets)
		for i := 0; i < client.MaxClientSecrets; i++ {
			full = append(full, clientSecretObject(fmt.Sprintf("00000000-0000-4000-8000-%012d", i), "pref"))
		}
		// Another process filled the client between the check and the POST.
		fake := clientSecretFake{version: "2.17.0", listed: full[:19], postStatus: 400,
			postBody: `{"error":"A client cannot have more than 20 secrets","code":"validation_failed"}`, appearOnPost: full[19]}
		resp, state := clientSecretCreate(t, fake.serve(t), clientSecretPlanned())
		require.True(t, resp.Diagnostics.HasError())
		text := clientSecretDiagText(resp.Diagnostics)
		assert.Contains(t, text, "at most 20")
		assert.Contains(t, text, "no secret was created")
		assert.Nil(t, state)
	})
}

func clientSecretRead(t *testing.T, c *client.Client, prior clientSecretResourceModel) (resource.ReadResponse, *clientSecretResourceModel) {
	t.Helper()
	ctx := context.Background()
	s := clientSecretTestSchema(t)
	state := tfsdk.State{Schema: s}
	require.False(t, state.Set(ctx, &prior).HasError())
	resp := resource.ReadResponse{State: state}
	(&clientSecretResource{client: c}).Read(ctx, resource.ReadRequest{State: state}, &resp)
	if resp.State.Raw.IsNull() {
		return resp, nil
	}
	var got clientSecretResourceModel
	require.False(t, resp.State.Get(ctx, &got).HasError())
	return resp, &got
}

func clientSecretStored() clientSecretResourceModel {
	return clientSecretResourceModel{
		ID: types.StringValue(clientSecretTestID), ClientID: types.StringValue("app"), ExpiresAt: types.StringValue("2030-01-02T05:04:05+02:00"),
		Secret: types.StringValue(clientSecretTestGen), SecretWO: types.StringNull(), SecretWOVersion: types.StringNull(),
		Prefix: types.StringValue("GENE"), CreatedAt: types.StringValue("2026-10-02T10:00:00Z"), IsActive: types.BoolValue(true),
	}
}

func TestClientSecretResource_Read(t *testing.T) {
	t.Run("refreshes metadata and keeps the expiry's spelling", func(t *testing.T) {
		fake := clientSecretFake{listed: []string{
			clientSecretObject(clientSecretTestOther, "othr"),
			`{"id":"` + clientSecretTestID + `","prefix":"GENE","createdAt":"2026-10-02T10:00:00Z","expiresAt":"2030-01-02T03:04:05Z","isActive":false}`,
		}}
		resp, got := clientSecretRead(t, fake.serve(t), clientSecretStored())
		require.False(t, resp.Diagnostics.HasError(), clientSecretDiagText(resp.Diagnostics))
		require.NotNil(t, got)
		assert.Equal(t, "2030-01-02T05:04:05+02:00", got.ExpiresAt.ValueString())
		assert.False(t, got.IsActive.ValueBool(), "an expired secret is reported, not removed")
		assert.Equal(t, clientSecretTestGen, got.Secret.ValueString())
	})
	t.Run("a different expiry is shown", func(t *testing.T) {
		fake := clientSecretFake{listed: []string{
			`{"id":"` + clientSecretTestID + `","prefix":"GENE","createdAt":"2026-10-02T10:00:00Z","expiresAt":"2031-01-01T00:00:00Z","isActive":true}`,
		}}
		_, got := clientSecretRead(t, fake.serve(t), clientSecretStored())
		assert.Equal(t, "2031-01-01T00:00:00Z", got.ExpiresAt.ValueString())
	})
	t.Run("a secret no longer listed is removed", func(t *testing.T) {
		fake := clientSecretFake{listed: []string{clientSecretObject(clientSecretTestOther, "othr")}}
		resp, got := clientSecretRead(t, fake.serve(t), clientSecretStored())
		require.False(t, resp.Diagnostics.HasError())
		assert.Nil(t, got)
	})
	t.Run("a confirmed missing client removes it", func(t *testing.T) {
		fake := clientSecretFake{clientMissing: true}
		resp, got := clientSecretRead(t, fake.serve(t), clientSecretStored())
		require.False(t, resp.Diagnostics.HasError())
		assert.Nil(t, got)
	})
	t.Run("any other 404 is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `<html>proxy</html>`)
		}))
		defer server.Close()
		c, _ := client.NewClient(server.URL, "synthetic-token", false, 2)
		resp, got := clientSecretRead(t, c, clientSecretStored())
		require.True(t, resp.Diagnostics.HasError())
		require.NotNil(t, got, "state is kept")
	})
}

func TestClientSecretResource_Delete(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		after   []string
		wantErr bool
	}{
		"revoked":                 {status: 204, after: []string{clientSecretObject(clientSecretTestOther, "othr")}},
		"failed but gone":         {status: 503, after: []string{}},
		"failed and still listed": {status: 503, after: []string{clientSecretObject(clientSecretTestID, "GENE")}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := clientSecretFake{deleteStatus: tc.status, listAfterDel: tc.after}
			ctx := context.Background()
			s := clientSecretTestSchema(t)
			state := tfsdk.State{Schema: s}
			stored := clientSecretStored()
			require.False(t, state.Set(ctx, &stored).HasError())
			resp := resource.DeleteResponse{State: state}
			(&clientSecretResource{client: fake.serve(t)}).Delete(ctx, resource.DeleteRequest{State: state}, &resp)
			assert.Equal(t, 1, fake.deletes)
			assert.Equal(t, tc.wantErr, resp.Diagnostics.HasError())
			if tc.wantErr {
				assert.Contains(t, clientSecretDiagText(resp.Diagnostics), clientSecretTestID)
				assert.NotContains(t, clientSecretDiagText(resp.Diagnostics), clientSecretTestGen)
			}
		})
	}
}

func TestClientSecretResource_ImportState(t *testing.T) {
	ctx := context.Background()
	s := clientSecretTestSchema(t)
	for id, ok := range map[string]bool{
		"app/" + clientSecretTestID:       true,
		"app":                             false,
		clientSecretTestID:                false,
		"app/not-a-uuid":                  false,
		"a/b/" + clientSecretTestID:       false,
		"bad id/" + clientSecretTestID:    false,
		"app/" + clientSecretTestID + "/": false,
	} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
		(&clientSecretResource{}).ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		assert.Equal(t, ok, !resp.Diagnostics.HasError(), id)
		if ok {
			var clientID, secretID types.String
			resp.State.GetAttribute(ctx, path.Root("client_id"), &clientID)
			resp.State.GetAttribute(ctx, path.Root("id"), &secretID)
			assert.Equal(t, "app", clientID.ValueString())
			assert.Equal(t, clientSecretTestID, secretID.ValueString())
		}
	}
}

func TestClientSecretResource_ExpiryChange(t *testing.T) {
	check := func(prior, planned types.String) bool {
		resp := &stringplanmodifier.RequiresReplaceIfFuncResponse{}
		clientSecretExpiryChanged(context.Background(), planmodifier.StringRequest{StateValue: prior, PlanValue: planned}, resp)
		return resp.RequiresReplace
	}
	z := types.StringValue("2030-01-02T03:04:05Z")
	assert.False(t, check(z, types.StringValue("2030-01-02T05:04:05+02:00")), "the same time written differently")
	assert.True(t, check(z, types.StringValue("2030-01-02T03:04:06Z")))
	assert.True(t, check(z, types.StringNull()), "removing the expiry")
	assert.True(t, check(types.StringNull(), z), "adding an expiry")
	assert.True(t, check(z, types.StringUnknown()))
}

func TestClientSecretResource_ValueValidator(t *testing.T) {
	for value, ok := range map[string]bool{
		"sixteen-chars-ok":       true,
		"has spaces and ~ marks": true,
		"tiny-val":               false,
		"sixteen-chars-é!":       false,
		"tab\tcharacters-here":   false,
	} {
		resp := &validator.StringResponse{}
		clientSecretValueValidator{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("secret_wo"), ConfigValue: types.StringValue(value)}, resp)
		assert.Equal(t, ok, !resp.Diagnostics.HasError(), value)
		assert.NotContains(t, clientSecretDiagText(resp.Diagnostics), value, "the value is never repeated")
	}
}

func TestClientSecretResource_ModifyPlan(t *testing.T) {
	ctx := context.Background()
	s := clientSecretTestSchema(t)
	run := func(config clientSecretResourceModel) (resource.ModifyPlanResponse, clientSecretResourceModel) {
		configured := tfsdk.Plan{Schema: s}
		require.False(t, configured.Set(ctx, &config).HasError())
		planned := config
		planned.SecretWO = types.StringNull()
		plan := tfsdk.Plan{Schema: s}
		require.False(t, plan.Set(ctx, &planned).HasError())
		resp := resource.ModifyPlanResponse{Plan: plan}
		(&clientSecretResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{
			Plan: plan, Config: tfsdk.Config{Schema: s, Raw: configured.Raw},
			State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)},
		}, &resp)
		var got clientSecretResourceModel
		require.False(t, resp.Plan.Get(ctx, &got).HasError())
		return resp, got
	}
	_, got := run(clientSecretPlanned())
	assert.True(t, got.Secret.IsUnknown(), "a generated value is known after apply")

	writeOnly := clientSecretPlanned()
	writeOnly.SecretWO = types.StringValue(clientSecretTestValue)
	writeOnly.SecretWOVersion = types.StringValue("1")
	_, got = run(writeOnly)
	assert.True(t, got.Secret.IsNull(), "a supplied value is never in state")

	expired := clientSecretPlanned()
	expired.ExpiresAt = types.StringValue("2020-01-01T00:00:00Z")
	resp, _ := run(expired)
	assert.True(t, resp.Diagnostics.HasError())

	// secret_wo unknown while planning (decided during the apply): secret
	// stays unknown, and either outcome at apply fits that plan.
	undecided := clientSecretPlanned()
	undecided.SecretWO = types.StringUnknown()
	undecided.SecretWOVersion = types.StringUnknown()
	_, got = run(undecided)
	assert.True(t, got.Secret.IsUnknown(), "presence of secret_wo is not known yet")

	t.Run("unknown then null: a generated value", func(t *testing.T) {
		fake := clientSecretFake{version: "2.17.0", postStatus: http.StatusCreated,
			postBody: `{"id":"` + clientSecretTestID + `","prefix":"GENE","createdAt":"2026-10-02T10:00:00Z","isActive":true,"secret":"` + clientSecretTestGen + `"}`}
		resp, state := clientSecretCreate(t, fake.serve(t), clientSecretPlanned())
		require.False(t, resp.Diagnostics.HasError(), clientSecretDiagText(resp.Diagnostics))
		assert.Equal(t, clientSecretTestGen, state.Secret.ValueString(), "a known value fills the unknown planned secret")
	})
	t.Run("unknown then supplied: no value stored", func(t *testing.T) {
		fake := clientSecretFake{version: "2.17.0", postStatus: http.StatusCreated,
			postBody: `{"id":"` + clientSecretTestID + `","prefix":"wo-v","createdAt":"2026-10-02T10:00:00Z","isActive":true,"secret":"` + clientSecretTestValue + `"}`}
		config := clientSecretPlanned()
		config.SecretWO = types.StringValue(clientSecretTestValue)
		config.SecretWOVersion = types.StringValue("1")
		resp, state := clientSecretCreate(t, fake.serve(t), config)
		require.False(t, resp.Diagnostics.HasError(), clientSecretDiagText(resp.Diagnostics))
		assert.True(t, state.Secret.IsNull(), "null fills the unknown planned secret")
	})
}

// A create response naming a secret the client already held is not taken as
// the new secret: nothing is kept in state, so no later replacement can
// revoke that secret, and the secret that did appear is reported. The next
// apply creates a secret of its own, and destroying it revokes only that one.
func TestClientSecretResource_CreateNamesExistingSecret(t *testing.T) {
	const existing = clientSecretTestOther // say, the secret pocketid_client holds
	const recovered = "77777777-7777-4777-8777-777777777777"
	for name, body := range map[string]string{
		"with a value":    `{"id":"` + existing + `","prefix":"GENE","createdAt":"2026-10-02T10:00:00Z","isActive":true,"secret":"` + clientSecretTestGen + `"}`,
		"without a value": `{"id":"` + existing + `","prefix":"GENE","createdAt":"2026-10-02T10:00:00Z","isActive":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			fake := clientSecretFake{version: "2.17.0", listed: []string{clientSecretObject(existing, "own1")},
				postStatus: http.StatusCreated, postBody: body, appearOnPost: clientSecretObject(clientSecretTestID, "newp")}
			c := fake.serve(t)
			resp, state := clientSecretCreate(t, c, clientSecretPlanned())
			require.True(t, resp.Diagnostics.HasError())
			text := clientSecretDiagText(resp.Diagnostics)
			assert.Contains(t, text, "result uncertain")
			assert.Contains(t, text, "already held before this request")
			assert.Contains(t, text, clientSecretTestID+"  prefix newp  created 2026-10-02T10:00:00.5Z  active  [new since this attempt]")
			assert.NotContains(t, text, clientSecretTestGen)
			assert.Nil(t, state, "no ownership of the existing secret, so nothing is tainted or replaced")
			assert.Equal(t, 1, fake.posts)

			// Recovery: the next apply creates again, and gets a secret of
			// its own; destroying it revokes only that secret.
			fake.mu.Lock()
			fake.postBody = `{"id":"` + recovered + `","prefix":"RECO","createdAt":"2026-10-02T11:00:00Z","isActive":true,"secret":"RECOVEREDrecovered0123456789abcd"}`
			fake.appearOnPost = clientSecretObject(recovered, "RECO")
			fake.mu.Unlock()
			resp, state = clientSecretCreate(t, c, clientSecretPlanned())
			require.False(t, resp.Diagnostics.HasError(), clientSecretDiagText(resp.Diagnostics))
			require.Equal(t, recovered, state.ID.ValueString())

			ctx := context.Background()
			stored := tfsdk.State{Schema: clientSecretTestSchema(t)}
			require.False(t, stored.Set(ctx, state).HasError())
			deleteResp := resource.DeleteResponse{State: stored}
			(&clientSecretResource{client: c}).Delete(ctx, resource.DeleteRequest{State: stored}, &deleteResp)
			require.False(t, deleteResp.Diagnostics.HasError(), clientSecretDiagText(deleteResp.Diagnostics))
			assert.Equal(t, []string{recovered}, fake.deletedIDs)
			assert.Zero(t, fake.deletes, "the secret that appeared during the failed attempt is left to the operator")
			assert.NotContains(t, fake.deletedIDs, existing, "the pre-existing secret is never revoked")
		})
	}
}

// Secret metadata reaches state and diagnostics only once checked: a whole
// value in the prefix field is neither stored nor printed, and a list that
// cannot be relied on proves nothing about absence.
func TestClientSecretResource_MetadataChecked(t *testing.T) {
	leakedList := `{"id":"` + clientSecretTestID + `","prefix":"` + clientSecretTestGen + `","createdAt":"2026-10-02T10:00:00Z","isActive":true}`

	t.Run("create response with the value as prefix", func(t *testing.T) {
		fake := clientSecretFake{version: "2.17.0", postStatus: http.StatusCreated,
			postBody: `{"id":"` + clientSecretTestID + `","prefix":"` + clientSecretTestGen + `","createdAt":"2026-10-02T10:00:00Z","isActive":true,"secret":"` + clientSecretTestGen + `"}`}
		resp, state := clientSecretCreate(t, fake.serve(t), clientSecretPlanned())
		require.True(t, resp.Diagnostics.HasError())
		assert.Contains(t, clientSecretDiagText(resp.Diagnostics), "only its ID is kept")
		assert.NotContains(t, clientSecretDiagText(resp.Diagnostics), clientSecretTestGen)
		require.NotNil(t, state, "the new secret's identity is kept so the next apply revokes it")
		assert.Equal(t, clientSecretTestID, state.ID.ValueString())
		assert.True(t, state.Prefix.IsNull())
		assert.True(t, state.Secret.IsNull())
		assert.NotContains(t, resp.State.Raw.String(), clientSecretTestGen)
	})
	t.Run("create refuses an unusable list", func(t *testing.T) {
		fake := clientSecretFake{version: "2.17.0", listed: []string{leakedList}}
		resp, state := clientSecretCreate(t, fake.serve(t), clientSecretPlanned())
		require.True(t, resp.Diagnostics.HasError())
		assert.NotContains(t, clientSecretDiagText(resp.Diagnostics), clientSecretTestGen)
		assert.Contains(t, clientSecretDiagText(resp.Diagnostics), "malformed")
		assert.Nil(t, state)
		assert.Zero(t, fake.posts)
	})
	t.Run("uncertain create with an unusable list afterwards", func(t *testing.T) {
		fake := clientSecretFake{version: "2.17.0", postStatus: 503, appearOnPost: leakedList}
		resp, _ := clientSecretCreate(t, fake.serve(t), clientSecretPlanned())
		require.True(t, resp.Diagnostics.HasError())
		text := clientSecretDiagText(resp.Diagnostics)
		assert.Contains(t, text, "could not be listed afterwards")
		assert.NotContains(t, text, clientSecretTestGen)
	})
	for name, list := range map[string][]string{
		"read: empty object":    {`{}`},
		"read: value as prefix": {leakedList},
		"read: duplicate IDs":   {clientSecretObject(clientSecretTestOther, "othr"), clientSecretObject(clientSecretTestOther, "othr")},
	} {
		t.Run(name, func(t *testing.T) {
			fake := clientSecretFake{listed: list}
			resp, got := clientSecretRead(t, fake.serve(t), clientSecretStored())
			require.True(t, resp.Diagnostics.HasError(), "an unusable list is an error, not proof the secret is gone")
			require.NotNil(t, got, "state is kept")
			assert.NotContains(t, clientSecretDiagText(resp.Diagnostics), clientSecretTestGen[4:])
		})
	}
	t.Run("delete: an unusable list does not confirm revocation", func(t *testing.T) {
		fake := clientSecretFake{deleteStatus: 503, listAfterDel: []string{`{}`}}
		ctx := context.Background()
		state := tfsdk.State{Schema: clientSecretTestSchema(t)}
		stored := clientSecretStored()
		require.False(t, state.Set(ctx, &stored).HasError())
		resp := resource.DeleteResponse{State: state}
		(&clientSecretResource{client: fake.serve(t)}).Delete(ctx, resource.DeleteRequest{State: state}, &resp)
		require.True(t, resp.Diagnostics.HasError())
		assert.Contains(t, clientSecretDiagText(resp.Diagnostics), "could not confirm")
	})
}
