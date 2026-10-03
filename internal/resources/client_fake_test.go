package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// fakeClient is the one OIDC client a fakePocketID holds, with the fields its
// update replaces (OidcService.UpdateClient and updateOIDCClientModelFromDto).
type fakeClient struct {
	ID, Name, Description, LaunchURL, BackchannelURL, ClientType string
	Callbacks, LogoutCallbacks                                   []string
	IsPublic, PkceEnabled, PkceSupported, PAR, Reauth            bool
	SkipConsent, Restricted, HasLogo, HasDarkLogo                bool
	AccessMinutes, RefreshMinutes                                int64
	Allowed                                                      []string
	FederatedIdentities                                          []map[string]any
}

// fakeSecret is one of the client's secrets.
type fakeSecret struct {
	ID, Prefix string
	Created    time.Time
}

// fakePocketID is a stateful stand-in for Pocket ID 2.14 to 2.17's client
// endpoints. It applies writes the way the server does, records every
// request, and models the back-channel logout notifications of 2.17: turning
// a client's group restriction on, or changing the allowed groups of a
// restricted client, signs out every authorized user who is in none of the
// allowed groups at that moment (backchannellogout.targetsForLostGroupAccess).
type fakePocketID struct {
	t       *testing.T
	mu      sync.Mutex
	version string
	client  *fakeClient
	secrets []fakeSecret
	// groups are the IDs of existing user groups; others are dropped.
	groups map[string]bool
	// members maps each user who authorized the client to their groups.
	members map[string][]string
	// signedOut lists the users notified of lost access, in order.
	signedOut []string
	calls     []string
	puts      []map[string]any
	// fail answers "METHOD path" with this status instead of handling it.
	fail       map[string]int
	nextSecret int
	// ignoreRestriction makes client writes ignore isGroupRestricted.
	ignoreRestriction bool
	// lostResponse lists "METHOD path" calls that are carried out but
	// answered with a truncated body, as when a response is lost.
	lostResponse map[string]bool
}

func newFakePocketID(t *testing.T, version string, c *fakeClient) *fakePocketID {
	t.Helper()
	if c.ClientType == "" {
		c.ClientType = "standard"
	}
	return &fakePocketID{t: t, version: version, client: c, groups: map[string]bool{}, members: map[string][]string{}, fail: map[string]int{}}
}

// atLeast compares the fake's version.
func (f *fakePocketID) atLeast(version string) bool {
	return semver.Compare("v"+f.version, "v"+version) >= 0
}

func (f *fakePocketID) start() *client.Client {
	f.t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	f.t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(f.t, err)
	return c
}

// called reports how often "METHOD path" was requested.
func (f *fakePocketID) called(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

// mutations returns the non-GET requests, in order.
func (f *fakePocketID) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "GET ") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakePocketID) clientJSON() map[string]any {
	c := f.client
	groups := make([]map[string]any, 0, len(c.Allowed))
	for _, id := range c.Allowed {
		groups = append(groups, map[string]any{"id": id, "name": "g", "friendlyName": "g"})
	}
	secrets := make([]map[string]any, 0, len(f.secrets))
	for _, s := range f.secrets {
		secrets = append(secrets, map[string]any{"id": s.ID, "prefix": s.Prefix, "createdAt": s.Created.Format(time.RFC3339), "expiresAt": nil, "isActive": true})
	}
	var launch any
	if c.LaunchURL != "" {
		launch = c.LaunchURL
	}
	body := map[string]any{
		"id": c.ID, "name": c.Name, "description": c.Description, "hasLogo": c.HasLogo, "hasDarkLogo": c.HasDarkLogo,
		"launchURL": launch, "requiresReauthentication": c.Reauth, "clientType": c.ClientType,
		"callbackURLs": c.Callbacks, "logoutCallbackURLs": c.LogoutCallbacks, "isPublic": c.IsPublic, "pkceEnabled": c.PkceEnabled,
		"requiresPushedAuthorizationRequests": c.PAR, "skipConsent": c.SkipConsent, "isGroupRestricted": c.Restricted,
		"accessTokenDurationMinutes": c.AccessMinutes, "refreshTokenDurationMinutes": c.RefreshMinutes,
		"credentials":       map[string]any{"federatedIdentities": c.FederatedIdentities, "secrets": secrets},
		"allowedUserGroups": groups,
	}
	if c.PkceSupported {
		body["pkceSupported"] = true
	}
	if f.atLeast("2.17.0") {
		body["backchannelLogoutURL"] = c.BackchannelURL
	}
	return body
}

// notifyLostAccess signs out authorized users in none of the allowed groups.
func (f *fakePocketID) notifyLostAccess() {
	if !f.atLeast("2.17.0") || !f.client.Restricted {
		return
	}
	for user, groups := range f.members {
		allowed := false
		for _, g := range groups {
			allowed = allowed || slices.Contains(f.client.Allowed, g)
		}
		if !allowed {
			f.signedOut = append(f.signedOut, user)
		}
	}
	slices.Sort(f.signedOut)
}

func (f *fakePocketID) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	w.Header().Set("Content-Type", "application/json")
	if status, ok := f.fail[call]; ok {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, `{"error":"synthetic failure"}`)
		return
	}
	base := "/api/oidc/clients/" + f.client.ID
	write := func(status int, v any) {
		w.WriteHeader(status)
		if f.lostResponse[call] {
			_, _ = fmt.Fprint(w, `{"id":`)
			return
		}
		require.NoError(f.t, json.NewEncoder(w).Encode(v))
	}
	switch {
	case call == "GET /api/version/current":
		write(200, map[string]string{"currentVersion": f.version})
	case call == "POST /api/oidc/clients":
		var in map[string]any
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		f.puts = append(f.puts, in)
		if id, ok := in["id"].(string); ok {
			f.client.ID = id
		}
		f.applyUpdate(in)
		body := f.clientJSON()
		if f.atLeast("2.17.0") && !f.client.IsPublic {
			// autoCreateOidcClientSecret, on by default.
			auto := fakeSecret{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Prefix: "auto", Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			f.secrets = append(f.secrets, auto)
			body["createdSecret"] = map[string]any{"id": auto.ID, "prefix": auto.Prefix, "secret": "autosynthetic-server-created"}
		}
		write(201, body)
	case call == "GET "+base:
		write(200, f.clientJSON())
	case call == "PUT "+base:
		var in map[string]any
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		f.puts = append(f.puts, in)
		f.applyUpdate(in)
		write(200, f.clientJSON())
	case call == "DELETE "+base:
		w.WriteHeader(204)
	case call == "PUT "+base+"/allowed-user-groups":
		var in struct {
			UserGroupIDs *[]string `json:"userGroupIds"`
		}
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
		if in.UserGroupIDs == nil {
			write(400, map[string]string{"error": "userGroupIds is required"})
			return
		}
		f.client.Allowed = nil
		for _, id := range *in.UserGroupIDs {
			if f.groups[id] {
				f.client.Allowed = append(f.client.Allowed, id)
			}
		}
		f.notifyLostAccess()
		// OidcClientDto: no allowedUserGroups.
		body := f.clientJSON()
		delete(body, "allowedUserGroups")
		write(200, body)
	case call == "GET "+base+"/secrets":
		list := make([]map[string]any, 0, len(f.secrets))
		for _, s := range f.secrets {
			list = append(list, map[string]any{"id": s.ID, "prefix": s.Prefix, "createdAt": s.Created.Format(time.RFC3339), "expiresAt": nil, "isActive": true})
		}
		write(200, list)
	case call == "POST "+base+"/secrets":
		if f.client.IsPublic {
			write(400, map[string]string{"error": "Cannot create a secret for a public client", "code": "validation_error"})
			return
		}
		f.nextSecret++
		id := fmt.Sprintf("%08d-0000-4000-8000-000000000000", f.nextSecret)
		value := fmt.Sprintf("gen%dsynthetic-generated-secret-value", f.nextSecret)
		f.secrets = append(f.secrets, fakeSecret{ID: id, Prefix: value[:4], Created: time.Date(2026, 1, f.nextSecret, 0, 0, 0, 0, time.UTC)})
		write(201, map[string]any{"id": id, "prefix": value[:4], "createdAt": time.Now().UTC().Format(time.RFC3339), "isActive": true, "secret": value})
	case strings.HasPrefix(call, "DELETE "+base+"/secrets/"):
		id := strings.TrimPrefix(r.URL.Path, base+"/secrets/")
		before := len(f.secrets)
		f.secrets = slices.DeleteFunc(f.secrets, func(s fakeSecret) bool { return s.ID == id })
		if len(f.secrets) == before {
			write(404, map[string]any{"error": "Client secret not found", "code": "not_found", "details": map[string]string{"resource": "Client secret"}})
			return
		}
		w.WriteHeader(204)
	default:
		f.t.Errorf("unexpected %s", call)
		w.WriteHeader(400)
	}
}

// applyUpdate applies a client PUT like updateOIDCClientModelFromDto.
func (f *fakePocketID) applyUpdate(in map[string]any) {
	c := f.client
	str := func(k string) string { v, _ := in[k].(string); return v }
	boolean := func(k string) bool { v, _ := in[k].(bool); return v }
	minutes := func(k string, fallback int64) int64 {
		if v, ok := in[k].(float64); ok && v != 0 {
			return int64(v)
		}
		return fallback
	}
	strs := func(k string) []string {
		raw, _ := in[k].([]any)
		var out []string
		for _, v := range raw {
			out = append(out, v.(string))
		}
		return out
	}
	was := c.Restricted
	c.Description = str("description")
	c.Reauth = boolean("requiresReauthentication")
	c.PAR = boolean("requiresPushedAuthorizationRequests")
	c.SkipConsent = boolean("skipConsent")
	c.LaunchURL = str("launchURL")
	if !f.ignoreRestriction {
		c.Restricted = boolean("isGroupRestricted")
	}
	c.AccessMinutes = minutes("accessTokenDurationMinutes", 60)
	c.RefreshMinutes = minutes("refreshTokenDurationMinutes", 43200)
	if !c.Restricted {
		c.Allowed = nil
	}
	if c.ClientType != "cimd" {
		c.Name = str("name")
		c.Callbacks = strs("callbackURLs")
		c.LogoutCallbacks = strs("logoutCallbackURLs")
		c.BackchannelURL = str("backchannelLogoutURL")
		c.IsPublic = boolean("isPublic")
		c.PkceEnabled = c.IsPublic || boolean("pkceEnabled")
		if !boolean("pkceEnabled") {
			c.PkceSupported = false
		}
	}
	if !was && c.Restricted {
		f.notifyLostAccess()
	}
}

// clientSchema returns the resource's schema.
func clientSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	resp := resource.SchemaResponse{}
	(&clientResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp
}

// managedModel is the state of a confidential client "c1" as this provider
// writes it, with every attribute known.
func managedModel() clientResourceModel {
	m := lifecycleModel()
	m.ID = types.StringValue("c1")
	m.ClientID = types.StringNull()
	m.HasLogo = types.BoolValue(false)
	m.ClientSecret = types.StringValue("gen0synthetic-held-secret")
	m.ClientSecretID = types.StringValue("00000000-0000-4000-8000-000000000000")
	m.IsGroupRestricted = types.BoolValue(false)
	m.Description, m.SkipConsent = types.StringValue(""), types.BoolValue(false)
	m.AccessTokenDurationMinutes, m.RefreshTokenDurationMinutes = types.Int64Value(60), types.Int64Value(43200)
	m.HasDarkLogo, m.ClientType, m.PkceSupported = types.BoolValue(false), types.StringValue("standard"), types.BoolValue(false)
	return m
}

// managedFake is the server side of managedModel, holding its secret.
func managedFake(t *testing.T, version string) *fakePocketID {
	f := newFakePocketID(t, version, &fakeClient{
		ID: "c1", Name: "fixture", Callbacks: []string{"https://example.invalid/callback"}, PkceEnabled: true,
		AccessMinutes: 60, RefreshMinutes: 43200,
	})
	f.secrets = []fakeSecret{{ID: "00000000-0000-4000-8000-000000000000", Prefix: "gen0", Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	return f
}

// configOf derives a configuration from a planned model: computed-only
// attributes are null in configuration.
func configOf(m clientResourceModel) clientResourceModel {
	m.ID = types.StringNull()
	m.HasLogo = types.BoolNull()
	m.ClientSecret = types.StringNull()
	m.ClientSecretID = types.StringNull()
	m.HasDarkLogo, m.ClientType, m.PkceSupported = types.BoolNull(), types.StringNull(), types.BoolNull()
	if !m.Description.IsUnknown() {
		m.Description, m.SkipConsent = types.StringNull(), types.BoolNull()
		m.AccessTokenDurationMinutes, m.RefreshTokenDurationMinutes = types.Int64Null(), types.Int64Null()
	}
	if !m.IsGroupRestricted.IsUnknown() {
		m.IsGroupRestricted = types.BoolNull()
	}
	return m
}

// runUpdate calls Update with the given prior state, plan and configuration
// and returns the response and the state it recorded.
func runUpdate(t *testing.T, r *clientResource, prior, planned, config clientResourceModel) (resource.UpdateResponse, clientResourceModel) {
	t.Helper()
	ctx := context.Background()
	s := clientSchema(t).Schema
	state := tfsdk.State{Schema: s}
	require.False(t, state.Set(ctx, &prior).HasError())
	plan := tfsdk.Plan{Schema: s}
	require.False(t, plan.Set(ctx, &planned).HasError())
	cfg := tfsdk.Plan{Schema: s}
	require.False(t, cfg.Set(ctx, &config).HasError())
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state.Raw.Copy()}}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config{Schema: s, Raw: cfg.Raw}}, &resp)
	var after clientResourceModel
	require.False(t, resp.State.Get(ctx, &after).HasError())
	return resp, after
}

// runRead calls Read on prior and returns the response and the new state.
func runRead(t *testing.T, r *clientResource, prior clientResourceModel) (resource.ReadResponse, clientResourceModel) {
	t.Helper()
	ctx := context.Background()
	s := clientSchema(t).Schema
	state := tfsdk.State{Schema: s}
	require.False(t, state.Set(ctx, &prior).HasError())
	resp := resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: state.Raw.Copy()}}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	var after clientResourceModel
	if !resp.State.Raw.IsNull() {
		require.False(t, resp.State.Get(ctx, &after).HasError())
	}
	return resp, after
}

// runModifyPlan runs the resource's plan modification on a proposed plan.
// prior nil plans a create.
func runModifyPlan(t *testing.T, prior *clientResourceModel, proposed, config clientResourceModel) (resource.ModifyPlanResponse, clientResourceModel) {
	t.Helper()
	ctx := context.Background()
	s := clientSchema(t).Schema
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if prior != nil {
		require.False(t, state.Set(ctx, prior).HasError())
	}
	plan := tfsdk.Plan{Schema: s}
	require.False(t, plan.Set(ctx, &proposed).HasError())
	cfg := tfsdk.Plan{Schema: s}
	require.False(t, cfg.Set(ctx, &config).HasError())
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw.Copy()}}
	(&clientResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: plan, State: state, Config: tfsdk.Config{Schema: s, Raw: cfg.Raw}}, &resp)
	var after clientResourceModel
	require.False(t, resp.Plan.Get(ctx, &after).HasError())
	return resp, after
}

// requireNoSecret fails when a diagnostic carries a secret value.
func requireNoSecret(t *testing.T, diags diag.Diagnostics, values ...string) {
	t.Helper()
	for _, d := range diags {
		for _, v := range append(values, "synthetic-token", "synthetic-held-secret", "synthetic-generated-secret-value") {
			require.NotContains(t, d.Summary()+d.Detail(), v)
		}
	}
}

// stringSet builds a set of strings.
func stringSet(values ...string) types.Set {
	elements := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elements = append(elements, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, elements)
}
