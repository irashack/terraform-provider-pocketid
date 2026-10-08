package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// clientLogoPresetFake is Pocket ID 2.18.0 with an icon library: client
// "app", GET /api/oidc/logo-presets, and an icon server of its own that
// serves jellyfin (with a white variant) and plex (without one).
type clientLogoPresetFake struct {
	mu            *sync.Mutex
	presetsStatus int
	presetsBody   string
	icons         map[string]string
	iconRequests  []string
	iconAuth      []string
	searches      []string
	uploads       []string
	hasLogo       bool
	hasDarkLogo   bool
}

func (f *clientLogoPresetFake) serve(t *testing.T) *client.Client {
	t.Helper()
	f.mu = &sync.Mutex{}
	icons := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.iconRequests = append(f.iconRequests, r.URL.Path)
		f.iconAuth = append(f.iconAuth, r.Header.Get("X-API-Key")+r.Header.Get("Authorization")+r.Header.Get("Cookie"))
		content, ok := f.icons[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, content)
	}))
	t.Cleanup(icons.Close)
	if f.icons == nil {
		f.icons = map[string]string{
			"/svg/jellyfin.svg":       "<svg>jellyfin</svg>",
			"/svg/jellyfin-light.svg": "<svg>jellyfin white</svg>",
			"/png/plex.png":           "plex png",
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /api/oidc/logo-presets":
			f.searches = append(f.searches, r.URL.Query().Get("search"))
			if f.presetsStatus != 0 {
				w.WriteHeader(f.presetsStatus)
				_, _ = fmt.Fprint(w, f.presetsBody)
				return
			}
			_, _ = fmt.Fprintf(w, `[{"name":"Jellyfin","reference":"jellyfin","logoUrl":"%[1]s/svg/jellyfin.svg","darkLogoUrl":"%[1]s/svg/jellyfin-light.svg"},`+
				`{"name":"Jellyfin Vue","reference":"jellyfin-vue","logoUrl":"%[1]s/svg/jellyfin-vue.svg","darkLogoUrl":null},`+
				`{"name":"Plex","reference":"plex","logoUrl":"%[1]s/png/plex.png","darkLogoUrl":null}]`, icons.URL)
		case "GET /api/oidc/clients/app":
			_, _ = fmt.Fprintf(w, `{"id":"app","name":"app","callbackURLs":[],"hasLogo":%t,"hasDarkLogo":%t,"allowedUserGroups":[]}`, f.hasLogo, f.hasDarkLogo)
		case "GET /api/oidc/clients/app/logo":
			_, _ = fmt.Fprint(w, "served")
		case "POST /api/oidc/clients/app/logo":
			light := r.URL.Query().Get("light") == "true"
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("upload is not multipart: %v", err)
			}
			_, header, err := r.FormFile("file")
			require.NoError(t, err)
			f.uploads = append(f.uploads, r.URL.RawQuery+" "+header.Filename)
			if light {
				f.hasLogo = true
			} else {
				f.hasDarkLogo = true
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)
	return c
}

func clientLogoPresetModel(variant, preset string) clientLogoResourceModel {
	return clientLogoResourceModel{ID: types.StringValue("app/" + variant), ClientID: types.StringValue("app"), Variant: types.StringValue(variant),
		Source: types.StringNull(), Preset: types.StringValue(preset), SHA256: types.StringUnknown()}
}

func diagnosticText(diags diag.Diagnostics) string {
	var text strings.Builder
	for _, d := range diags {
		text.WriteString(d.Summary() + ": " + d.Detail() + "\n")
	}
	return text.String()
}

// clientLogoTestPrivate is a private-state store for the tests.
type clientLogoTestPrivate map[string][]byte

func (p clientLogoTestPrivate) GetKey(_ context.Context, key string) ([]byte, diag.Diagnostics) {
	return p[key], nil
}

func (p clientLogoTestPrivate) SetKey(_ context.Context, key string, value []byte) diag.Diagnostics {
	if value == nil {
		delete(p, key)
	} else {
		p[key] = value
	}
	return nil
}

// The icon is found by its exact reference, downloaded without credentials
// and uploaded under its own file type.
func TestClientLogoResource_UploadFromPreset(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		variant, preset, icon, upload, content string
	}{
		"light svg":     {"light", "jellyfin", "/svg/jellyfin.svg", "light=true logo.svg", "<svg>jellyfin</svg>"},
		"dark is white": {"dark", "jellyfin", "/svg/jellyfin-light.svg", "light=false logo.svg", "<svg>jellyfin white</svg>"},
		"png icon":      {"light", "plex", "/png/plex.png", "light=true logo.png", "plex png"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &clientLogoPresetFake{}
			r := &clientLogoResource{client: fake.serve(t)}
			plan := clientLogoPresetModel(tc.variant, tc.preset)
			var diags diag.Diagnostics
			uploaded, served := r.upload(ctx, &plan, &diags)
			require.False(t, diags.HasError(), diagnosticText(diags))
			require.True(t, uploaded)
			assert.Equal(t, clientLogoSHA256([]byte("served")), served)
			assert.Equal(t, []string{tc.preset}, fake.searches)
			assert.Equal(t, []string{tc.icon}, fake.iconRequests)
			assert.Equal(t, []string{""}, fake.iconAuth, "no credential reaches the icon server")
			assert.Equal(t, []string{tc.upload}, fake.uploads)
			assert.Equal(t, clientLogoSHA256([]byte(tc.content)), plan.SHA256.ValueString())

			private := clientLogoTestPrivate{}
			require.False(t, setClientLogoPresetHash(ctx, private, &plan).HasError())
			recorded, _ := clientLogoPrivateHash(ctx, private, clientLogoPresetKey)
			assert.Equal(t, plan.SHA256.ValueString(), recorded)

			fromFile := clientLogoStored("light")
			require.False(t, setClientLogoPresetHash(ctx, private, &fromFile).HasError())
			recorded, _ = clientLogoPrivateHash(ctx, private, clientLogoPresetKey)
			assert.Empty(t, recorded, "an upload from source removes the record")
		})
	}
}

func TestClientLogoResource_PresetFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		fake    clientLogoPresetFake
		variant string
		preset  string
		want    string
	}{
		"unknown icon":        {variant: "light", preset: "jelly", want: `no icon with the reference "jelly"`},
		"no white variant":    {variant: "dark", preset: "plex", want: "no white variant"},
		"icon missing on cdn": {variant: "light", preset: "jellyfin-vue", want: "HTTP 404"},
		"library disabled": {fake: clientLogoPresetFake{presetsStatus: http.StatusForbidden,
			presetsBody: `{"error":"Logo presets are disabled","code":"logo_presets_disabled"}`}, variant: "light", preset: "jellyfin", want: "ICON_LIBRARY_URL=disabled"},
		"library unavailable": {fake: clientLogoPresetFake{presetsStatus: http.StatusBadGateway,
			presetsBody: `{"error":"Logo presets could not be loaded","code":"logo_presets_unavailable"}`}, variant: "light", preset: "jellyfin", want: "could not load its icon library"},
		"older server": {fake: clientLogoPresetFake{presetsStatus: http.StatusNotFound,
			presetsBody: `{"error":"API endpoint not found"}`}, variant: "light", preset: "jellyfin", want: "requires Pocket ID 2.18.0"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &tc.fake
			resp := clientLogoCreate(t, fake.serve(t), clientLogoPresetModel(tc.variant, tc.preset))
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, diagnosticText(resp.Diagnostics), tc.want)
			assert.Empty(t, fake.uploads, "nothing is uploaded")
			assert.True(t, resp.State.Raw.IsNull())
		})
	}
}

func TestClientLogoPresetUnchanged(t *testing.T) {
	stored := clientLogoPresetModel("light", "jellyfin")
	stored.SHA256 = types.StringValue("icon-hash")
	plan := clientLogoPresetModel("light", "jellyfin")
	other := clientLogoPresetModel("light", "plex")
	imported := stored
	imported.Preset = types.StringNull()

	assert.True(t, clientLogoPresetUnchanged(&stored, &plan, "icon-hash"), "unchanged")
	assert.False(t, clientLogoPresetUnchanged(&stored, &plan, "uploaded-hash"), "replaced outside Terraform")
	assert.False(t, clientLogoPresetUnchanged(&stored, &plan, ""), "no record")
	assert.False(t, clientLogoPresetUnchanged(&stored, &other, "icon-hash"), "other preset")
	assert.False(t, clientLogoPresetUnchanged(&imported, &plan, "icon-hash"), "after an import")
	assert.False(t, clientLogoPresetUnchanged(nil, &plan, "icon-hash"), "before creation")
}

// Planning an upload from a preset checks that the icon exists, downloads
// nothing, and leaves sha256 unknown.
func TestClientLogoResource_ModifyPlanPreset(t *testing.T) {
	ctx := context.Background()
	s := clientLogoTestSchema(t)
	run := func(t *testing.T, fake *clientLogoPresetFake, preset string) (resource.ModifyPlanResponse, clientLogoResourceModel) {
		t.Helper()
		model := clientLogoPresetModel("light", preset)
		model.ID = types.StringUnknown()
		plan := tfsdk.Plan{Schema: s}
		require.False(t, plan.Set(ctx, &model).HasError())
		req := resource.ModifyPlanRequest{Plan: plan, State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
		resp := resource.ModifyPlanResponse{Plan: plan}
		(&clientLogoResource{client: fake.serve(t)}).ModifyPlan(ctx, req, &resp)
		var got clientLogoResourceModel
		require.False(t, resp.Plan.Get(ctx, &got).HasError())
		return resp, got
	}

	fake := &clientLogoPresetFake{}
	resp, got := run(t, fake, "jellyfin")
	require.False(t, resp.Diagnostics.HasError(), diagnosticText(resp.Diagnostics))
	assert.True(t, got.SHA256.IsUnknown())
	assert.Equal(t, "app/light", got.ID.ValueString())
	assert.Equal(t, []string{"jellyfin"}, fake.searches)
	assert.Empty(t, fake.iconRequests, "the plan downloads nothing")

	resp, _ = run(t, &clientLogoPresetFake{}, "nope")
	assert.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, diagnosticText(resp.Diagnostics), `"nope"`)
}

func TestClientLogoNeedsUploadPreset(t *testing.T) {
	model := func(source, preset, sum string) *clientLogoResourceModel {
		m := &clientLogoResourceModel{Source: types.StringNull(), Preset: types.StringNull(), SHA256: types.StringValue(sum)}
		if source != "" {
			m.Source = types.StringValue(source)
		}
		if preset != "" {
			m.Preset = types.StringValue(preset)
		}
		if sum == "" {
			m.SHA256 = types.StringUnknown()
		}
		return m
	}
	for name, tc := range map[string]struct {
		state, plan *clientLogoResourceModel
		upload      bool
	}{
		"preset unchanged":      {model("", "jellyfin", "h1"), model("", "jellyfin", "h1"), false},
		"preset to plan":        {model("", "jellyfin", "h1"), model("", "jellyfin", ""), true},
		"other preset":          {model("", "jellyfin", "h1"), model("", "plex", "h1"), true},
		"file to preset":        {model("a.svg", "", "h1"), model("", "jellyfin", "h1"), true},
		"preset to file, same":  {model("", "jellyfin", "h1"), model("a.svg", "", "h1"), true},
		"preset to file, other": {model("", "jellyfin", "h1"), model("a.svg", "", "h2"), true},
	} {
		assert.Equal(t, tc.upload, clientLogoNeedsUpload(tc.plan, tc.state), name)
	}
}

func TestClientLogoPresetValidator(t *testing.T) {
	ctx := context.Background()
	for preset, ok := range map[string]bool{"jellyfin": true, "home-assistant": true, "a.b_c": true, "0ad": true,
		"Jellyfin": false, "-x": false, "../x": false, "a/b": false, "": false, "x y": false} {
		resp := &validator.StringResponse{}
		clientLogoPresetValidator{}.ValidateString(ctx, validator.StringRequest{Path: path.Root("preset"), ConfigValue: types.StringValue(preset)}, resp)
		assert.Equal(t, ok, !resp.Diagnostics.HasError(), preset)
	}
	validators := (&clientLogoResource{}).ConfigValidators(ctx)
	require.Len(t, validators, 1, "exactly one of source and preset")
}
