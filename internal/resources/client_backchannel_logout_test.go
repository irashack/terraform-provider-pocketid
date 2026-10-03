package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// The cases mirror Pocket ID 2.17.0's TestBackchannelLogoutURLValidation.
func TestBackchannelLogoutURLProblem(t *testing.T) {
	for _, tc := range []struct {
		name     string
		url      string
		isPublic bool
		problem  bool
	}{
		{name: "https confidential", url: "https://rp.example/logout"},
		{name: "https public", url: "https://rp.example/logout", isPublic: true},
		{name: "http confidential", url: "http://rp.example:8080/logout?tenant=test"},
		{name: "http public", url: "http://rp.example/logout", isPublic: true, problem: true},
		{name: "fragment", url: "https://rp.example/logout#fragment", problem: true},
		{name: "empty fragment", url: "https://rp.example/logout#", problem: true},
		{name: "encoded hash in query", url: "https://rp.example/logout?tenant=%23test", isPublic: true},
		{name: "relative", url: "/logout", problem: true},
		{name: "missing host", url: "https:///logout", problem: true},
		{name: "unsupported scheme", url: "ftp://rp.example/logout", problem: true},
		{name: "upper-case scheme", url: "HTTPS://rp.example/logout", isPublic: true},
		{name: "empty", url: "", problem: true},
		{name: "unparseable", url: "https://rp.example/%zz", problem: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.problem, backchannelLogoutURLProblem(tc.url, tc.isPublic) != "")
		})
	}
}

func TestBackchannelLogoutURLValidator(t *testing.T) {
	for name, tc := range map[string]struct {
		value   types.String
		problem bool
	}{
		"null":     {types.StringNull(), false},
		"unknown":  {types.StringUnknown(), false},
		"valid":    {types.StringValue("http://rp.example/logout"), false},
		"fragment": {types.StringValue("https://rp.example/logout#x"), true},
		"empty":    {types.StringValue(""), true},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &validator.StringResponse{}
			backchannelLogoutURLValidator{}.ValidateString(context.Background(), validator.StringRequest{ConfigValue: tc.value}, resp)
			assert.Equal(t, tc.problem, resp.Diagnostics.HasError())
		})
	}
}

// https for a public client needs is_public, so ValidateConfig checks it.
func TestClientValidateConfigBackchannelLogoutURL(t *testing.T) {
	ctx := context.Background()
	r := &clientResource{}
	schemaResp := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	for name, tc := range map[string]struct {
		url      types.String
		isPublic types.Bool
		errors   int
	}{
		"http public":            {types.StringValue("http://rp.example/logout"), types.BoolValue(true), 1},
		"https public":           {types.StringValue("https://rp.example/logout"), types.BoolValue(true), 0},
		"http confidential":      {types.StringValue("http://rp.example/logout"), types.BoolValue(false), 0},
		"http default":           {types.StringValue("http://rp.example/logout"), types.BoolNull(), 0},
		"http unknown is_public": {types.StringValue("http://rp.example/logout"), types.BoolUnknown(), 0},
		"unknown url":            {types.StringUnknown(), types.BoolValue(true), 0},
		// Reported once, by the attribute validator, not again here.
		"fragment public": {types.StringValue("http://rp.example/logout#x"), types.BoolValue(true), 0},
	} {
		t.Run(name, func(t *testing.T) {
			model := lifecycleModel()
			model.ID = types.StringNull()
			model.HasLogo = types.BoolNull()
			model.ClientSecret = types.StringNull()
			model.BackchannelLogoutURL = tc.url
			model.IsPublic = tc.isPublic
			// tfsdk.Config cannot be set from a model; a Plan builds the value.
			built := tfsdk.Plan{Schema: schemaResp.Schema}
			require.False(t, built.Set(ctx, &model).HasError())
			config := tfsdk.Config{Schema: schemaResp.Schema, Raw: built.Raw}
			resp := &resource.ValidateConfigResponse{}
			r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: config}, resp)
			assert.Equal(t, tc.errors, resp.Diagnostics.ErrorsCount(), "%v", resp.Diagnostics)
		})
	}
}

// backchannelServer is a fake Pocket ID that records what client writes send.
type backchannelServer struct {
	version  string
	current  string // the client's backchannelLogoutURL before the write
	writes   []map[string]any
	versions int
}

func (s *backchannelServer) start(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		respond := func(url string) {
			body := map[string]any{"id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "name": "fixture", "callbackURLs": []string{"https://example.invalid/callback"}, "pkceEnabled": true, "allowedUserGroups": []any{}}
			if url != "" {
				body["backchannelLogoutURL"] = url
			}
			_ = json.NewEncoder(w).Encode(body)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/version/current":
			s.versions++
			_, _ = fmt.Fprintf(w, `{"currentVersion":%q}`, s.version)
		case "GET /api/oidc/clients/dddddddd-dddd-4ddd-8ddd-dddddddddddd":
			respond(s.current)
		case "POST /api/oidc/clients", "PUT /api/oidc/clients/dddddddd-dddd-4ddd-8ddd-dddddddddddd":
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			s.writes = append(s.writes, payload)
			url, _ := payload["backchannelLogoutURL"].(string)
			if s.version < "2.17.0" {
				url = "" // an older server ignores the field
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
			respond(url)
		default:
			t.Errorf("unexpected method/path %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c
}

func publicClientModel() clientResourceModel {
	model := lifecycleModel()
	model.IsPublic = types.BoolValue(true)
	model.ClientSecret = types.StringNull()
	model.ClientSecretID = types.StringNull()
	model.BackchannelLogoutURL = types.StringNull()
	return model
}

func TestClientCreateBackchannelLogoutURL(t *testing.T) {
	for name, tc := range map[string]struct {
		version string
		planned types.String
		sent    any // nil: field absent
		wantErr bool
	}{
		"set on 2.17":         {"2.17.0", types.StringValue("https://rp.example/logout"), "https://rp.example/logout", false},
		"null on 2.17":        {"2.17.0", types.StringNull(), nil, false},
		"null on 2.16":        {"2.16.0", types.StringNull(), nil, false},
		"set on 2.16 refused": {"2.16.0", types.StringValue("https://rp.example/logout"), nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &backchannelServer{version: tc.version}
			r := &clientResource{client: fake.start(t)}
			ctx := context.Background()
			schemaResp := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
			model := publicClientModel()
			model.BackchannelLogoutURL = tc.planned
			plan := tfsdk.Plan{Schema: schemaResp.Schema}
			require.False(t, plan.Set(ctx, &model).HasError())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

			require.Equal(t, tc.wantErr, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			if tc.wantErr {
				require.Empty(t, fake.writes, "refused before any mutation")
				require.Contains(t, resp.Diagnostics[0].Detail(), "requires Pocket ID 2.17.0 or later")
				return
			}
			require.Len(t, fake.writes, 1)
			assert.Equal(t, tc.sent, fake.writes[0]["backchannelLogoutURL"])
			_, present := fake.writes[0]["backchannelLogoutURL"]
			assert.Equal(t, tc.sent != nil, present)
			var state clientResourceModel
			require.False(t, resp.State.Get(ctx, &state).HasError())
			assert.Equal(t, tc.planned, state.BackchannelLogoutURL)
		})
	}
}

func TestClientUpdateBackchannelLogoutURL(t *testing.T) {
	const old, oob, next = "https://old.example/logout", "https://outside.example/logout", "https://new.example/logout"
	for name, tc := range map[string]struct {
		version        string
		prior, planned types.String
		server         string // the server's value before the update
		sent           any    // nil: field absent
		wantErr        bool
	}{
		// No change planned: whatever the server holds is sent back.
		"unchanged null, server empty":                  {"2.17.0", types.StringNull(), types.StringNull(), "", nil, false},
		"unchanged null, older server":                  {"2.16.0", types.StringNull(), types.StringNull(), "", nil, false},
		"unchanged null, set outside, unrefreshed":      {"2.17.0", types.StringNull(), types.StringNull(), oob, oob, false},
		"unchanged value, changed outside, unrefreshed": {"2.17.0", types.StringValue(old), types.StringValue(old), oob, oob, false},
		"unchanged value":                               {"2.17.0", types.StringValue(old), types.StringValue(old), old, old, false},
		// A planned change is sent as planned; null clears it.
		"set":                  {"2.17.0", types.StringNull(), types.StringValue(next), "", next, false},
		"changed":              {"2.17.0", types.StringValue(old), types.StringValue(next), old, next, false},
		"changed over outside": {"2.17.0", types.StringValue(old), types.StringValue(next), oob, next, false},
		"cleared":              {"2.17.0", types.StringValue(old), types.StringNull(), old, nil, false},
		"set on 2.16 refused":  {"2.16.0", types.StringNull(), types.StringValue(next), "", nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &backchannelServer{version: tc.version, current: tc.server}
			r := &clientResource{client: fake.start(t)}
			ctx := context.Background()
			schemaResp := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

			prior := publicClientModel()
			prior.ID = types.StringValue("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
			prior.HasLogo = types.BoolValue(false)
			prior.BackchannelLogoutURL = tc.prior
			planned := prior
			planned.Name = types.StringValue("fixture-renamed")
			planned.BackchannelLogoutURL = tc.planned

			resp, after := runUpdate(t, r, prior, planned, configOf(planned))

			require.Equal(t, tc.wantErr, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			if tc.wantErr {
				require.Empty(t, fake.writes, "refused before any mutation")
				require.Contains(t, resp.Diagnostics[0].Detail(), "requires Pocket ID 2.17.0 or later")
				return
			}
			require.Len(t, fake.writes, 1)
			_, present := fake.writes[0]["backchannelLogoutURL"]
			assert.Equal(t, tc.sent != nil, present)
			assert.Equal(t, tc.sent, fake.writes[0]["backchannelLogoutURL"])
			if tc.planned.IsNull() {
				assert.Zero(t, fake.versions, "a null value needs no version check")
			}
			// State always records the plan; a value kept from outside
			// Terraform shows up on the next refresh instead.
			assert.Equal(t, tc.planned, after.BackchannelLogoutURL)
		})
	}
}

func TestClientReadBackchannelLogoutURL(t *testing.T) {
	for name, tc := range map[string]struct {
		server string
		want   types.String
	}{
		"empty reads as null": {"", types.StringNull()},
		"value":               {"https://rp.example/logout", types.StringValue("https://rp.example/logout")},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &backchannelServer{version: "2.17.0", current: tc.server}
			r := &clientResource{client: fake.start(t)}
			ctx := context.Background()
			schemaResp := resource.SchemaResponse{}
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
			prior := publicClientModel()
			prior.ID = types.StringValue("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
			prior.HasLogo = types.BoolValue(false)
			// A stale value that Read must replace in both cases.
			prior.BackchannelLogoutURL = types.StringValue("https://stale.example/logout")
			state := tfsdk.State{Schema: schemaResp.Schema}
			require.False(t, state.Set(ctx, &prior).HasError())
			resp := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			var after clientResourceModel
			require.False(t, resp.State.Get(ctx, &after).HasError())
			assert.Equal(t, tc.want, after.BackchannelLogoutURL)
		})
	}
}
