package resources

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// clientLogoTestPNGHeader is the start of a PNG file declaring the given
// size: enough for image.DecodeConfig, which reads only the header.
func clientLogoTestPNGHeader(width, height uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], width)
	binary.BigEndian.PutUint32(ihdr[4:], height)
	ihdr[8], ihdr[9] = 8, 0 // 8-bit greyscale
	_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func clientLogoTestFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(file, content, 0o600))
	return file
}

func TestReadClientLogoSource(t *testing.T) {
	content := []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")
	file := clientLogoTestFile(t, "logo.SVG", content)
	got, sum, err := readClientLogoSource(file)
	require.NoError(t, err)
	assert.Equal(t, content, got)
	assert.Equal(t, clientLogoSHA256(content), sum)
	assert.Len(t, sum, 64)

	_, _, err = readClientLogoSource(filepath.Join(t.TempDir(), "missing.png"))
	assert.ErrorIs(t, err, fs.ErrNotExist)

	_, _, err = readClientLogoSource(clientLogoTestFile(t, "logo.bmp", content))
	assert.ErrorContains(t, err, "no extension Pocket ID accepts")

	_, _, err = readClientLogoSource(clientLogoTestFile(t, "big.svg", bytes.Repeat([]byte{'x'}, client.ClientLogoMaxBytes+1)))
	assert.ErrorContains(t, err, "larger than")
	_, _, err = readClientLogoSource(clientLogoTestFile(t, "edge.svg", bytes.Repeat([]byte{'x'}, client.ClientLogoMaxBytes)))
	assert.NoError(t, err)

	_, _, err = readClientLogoSource(clientLogoTestFile(t, "huge.png", clientLogoTestPNGHeader(4001, 4000)))
	assert.ErrorContains(t, err, "4001x4000 pixels")
	_, _, err = readClientLogoSource(clientLogoTestFile(t, "fine.png", clientLogoTestPNGHeader(4000, 4000)))
	assert.NoError(t, err, "16 million pixels are allowed")
	_, _, err = readClientLogoSource(clientLogoTestFile(t, "undecodable.png", []byte("not a png")))
	assert.NoError(t, err, "Pocket ID accepts what it cannot decode, so this does not refuse it")

	dir := filepath.Join(t.TempDir(), "dir.png")
	require.NoError(t, os.Mkdir(dir, 0o700))
	_, _, err = readClientLogoSource(dir)
	assert.ErrorContains(t, err, "not a regular file")
}

func TestClientLogoCompareServed(t *testing.T) {
	const file, served, other = "file-hash", "served-hash", "other-hash"
	state := clientLogoResourceModel{SHA256: types.StringValue(file)}
	assert.Empty(t, clientLogoCompareServed(&state, served, served), "unchanged: nothing to record")
	assert.Equal(t, file, state.SHA256.ValueString(), "and nothing to upload")

	state = clientLogoResourceModel{SHA256: types.StringValue(file)}
	assert.Empty(t, clientLogoCompareServed(&state, served, other))
	assert.Equal(t, other, state.SHA256.ValueString(), "replaced outside Terraform: the next plan uploads again")

	state = clientLogoResourceModel{SHA256: types.StringNull()}
	assert.Equal(t, served, clientLogoCompareServed(&state, "", served), "imported: the served image is the reference")
	assert.Equal(t, served, state.SHA256.ValueString())

	state = clientLogoResourceModel{SHA256: types.StringValue(file)}
	assert.Equal(t, served, clientLogoCompareServed(&state, "", served), "read back failed after the upload: record now")
	assert.Equal(t, file, state.SHA256.ValueString(), "the uploaded file's hash is kept")
}

// clientLogoFake serves client "app" with configurable logo flags.
type clientLogoFake struct {
	mu           *sync.Mutex
	hasLogo      bool
	hasDarkLogo  bool
	clientStatus int
	uploadStatus int
	deleteStatus int
	uploads      []string
	deletes      []string
}

func (f *clientLogoFake) serve(t *testing.T) *client.Client {
	t.Helper()
	f.mu = &sync.Mutex{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /api/oidc/clients/app":
			if f.clientStatus != 0 {
				w.WriteHeader(f.clientStatus)
				if f.clientStatus == http.StatusNotFound {
					_, _ = fmt.Fprint(w, `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`)
				}
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":"app","name":"app","callbackURLs":[],"hasLogo":%t,"hasDarkLogo":%t}`, f.hasLogo, f.hasDarkLogo)
		case "GET /api/oidc/clients/app/logo":
			if (r.URL.Query().Get("light") == "true" && !f.hasLogo) || (!f.hasLogo && !f.hasDarkLogo) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"error":"Image not found","code":"image_not_found"}`)
				return
			}
			_, _ = fmt.Fprint(w, "served")
		case "POST /api/oidc/clients/app/logo":
			f.uploads = append(f.uploads, r.URL.RawQuery)
			w.WriteHeader(f.uploadStatus)
		case "DELETE /api/oidc/clients/app/logo":
			f.deletes = append(f.deletes, r.URL.RawQuery)
			w.WriteHeader(f.deleteStatus)
			if f.deleteStatus == http.StatusNotFound {
				_, _ = fmt.Fprint(w, `{"error":"Image not found","code":"image_not_found"}`)
			}
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

func clientLogoTestSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := resource.SchemaResponse{}
	(&clientLogoResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

func clientLogoTestState(t *testing.T, model clientLogoResourceModel) tfsdk.State {
	t.Helper()
	s := clientLogoTestSchema(t)
	state := tfsdk.State{Schema: s}
	require.False(t, state.Set(context.Background(), &model).HasError())
	return state
}

func clientLogoStored(variant string) clientLogoResourceModel {
	return clientLogoResourceModel{
		ID: types.StringValue("app/" + variant), ClientID: types.StringValue("app"), Variant: types.StringValue(variant),
		Source: types.StringValue("logo.png"), SHA256: types.StringValue("file-hash"),
	}
}

// A logo the client no longer reports, an image Pocket ID no longer serves
// and a client confirmed missing leave state; any other failure is an error.
func TestClientLogoResource_ReadRemovals(t *testing.T) {
	for name, tc := range map[string]struct {
		fake    clientLogoFake
		variant string
		removed bool
		failed  bool
	}{
		"client gone":                      {fake: clientLogoFake{clientStatus: http.StatusNotFound}, variant: "light", removed: true},
		"light logo removed":               {fake: clientLogoFake{hasDarkLogo: true}, variant: "light", removed: true},
		"dark logo removed, light remains": {fake: clientLogoFake{hasLogo: true}, variant: "dark", removed: true},
		"client unreadable":                {fake: clientLogoFake{clientStatus: http.StatusForbidden}, variant: "light", failed: true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := tc.fake
			ctx := context.Background()
			state := clientLogoTestState(t, clientLogoStored(tc.variant))
			resp := resource.ReadResponse{State: state}
			(&clientLogoResource{client: fake.serve(t)}).Read(ctx, resource.ReadRequest{State: state}, &resp)
			assert.Equal(t, tc.failed, resp.Diagnostics.HasError())
			assert.Equal(t, tc.removed, resp.State.Raw.IsNull())
		})
	}
}

func TestClientLogoResource_Delete(t *testing.T) {
	for name, tc := range map[string]struct {
		fake    clientLogoFake
		variant string
		query   string
		failed  bool
	}{
		"removed":                 {fake: clientLogoFake{deleteStatus: http.StatusNoContent}, variant: "dark", query: "light=false"},
		"already gone":            {fake: clientLogoFake{deleteStatus: http.StatusNotFound}, variant: "light", query: "light=true"},
		"failed but gone":         {fake: clientLogoFake{deleteStatus: http.StatusServiceUnavailable, hasLogo: true}, variant: "dark", query: "light=false"},
		"failed and still there":  {fake: clientLogoFake{deleteStatus: http.StatusServiceUnavailable, hasDarkLogo: true}, variant: "dark", query: "light=false", failed: true},
		"refused and still there": {fake: clientLogoFake{deleteStatus: http.StatusForbidden, hasLogo: true}, variant: "light", query: "light=true", failed: true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := tc.fake
			state := clientLogoTestState(t, clientLogoStored(tc.variant))
			resp := resource.DeleteResponse{State: state}
			(&clientLogoResource{client: fake.serve(t)}).Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
			assert.Equal(t, []string{tc.query}, fake.deletes, "one DELETE of the right logo")
			assert.Equal(t, tc.failed, resp.Diagnostics.HasError())
		})
	}
}

func clientLogoCreate(t *testing.T, c *client.Client, model clientLogoResourceModel) resource.CreateResponse {
	t.Helper()
	ctx := context.Background()
	s := clientLogoTestSchema(t)
	plan := tfsdk.Plan{Schema: s}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
	(&clientLogoResource{client: c}).Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	return resp
}

func TestClientLogoResource_CreateFailures(t *testing.T) {
	content := []byte("<svg/>")
	source := clientLogoTestFile(t, "logo.svg", content)
	planned := func(sum string) clientLogoResourceModel {
		return clientLogoResourceModel{ID: types.StringValue("app/dark"), ClientID: types.StringValue("app"), Variant: types.StringValue("dark"),
			Source: types.StringValue(source), SHA256: types.StringValue(sum)}
	}
	for name, tc := range map[string]struct {
		fake    clientLogoFake
		sum     string
		uploads int
		want    string
	}{
		"file changed since the plan": {fake: clientLogoFake{}, sum: "planned-hash", want: "changed after the plan"},
		"server ignored the variant":  {fake: clientLogoFake{uploadStatus: http.StatusNoContent, hasLogo: true}, uploads: 1, want: "does not report a dark logo"},
		"refused":                     {fake: clientLogoFake{uploadStatus: http.StatusUnsupportedMediaType}, uploads: 1, want: "the client's logo is unchanged"},
		"uncertain":                   {fake: clientLogoFake{uploadStatus: http.StatusBadGateway}, uploads: 1, want: "not retried"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := tc.fake
			sum := tc.sum
			if sum == "" {
				sum = clientLogoSHA256(content)
			}
			resp := clientLogoCreate(t, fake.serve(t), planned(sum))
			require.True(t, resp.Diagnostics.HasError())
			var text strings.Builder
			for _, d := range resp.Diagnostics {
				text.WriteString(d.Detail())
			}
			assert.Contains(t, text.String(), tc.want)
			assert.Len(t, fake.uploads, tc.uploads)
			assert.True(t, resp.State.Raw.IsNull(), "nothing is recorded")
		})
	}
}

func TestClientLogoResource_ModifyPlan(t *testing.T) {
	ctx := context.Background()
	s := clientLogoTestSchema(t)
	run := func(source string) (resource.ModifyPlanResponse, clientLogoResourceModel) {
		model := clientLogoResourceModel{ID: types.StringUnknown(), ClientID: types.StringValue("app"), Variant: types.StringValue("light"),
			Source: types.StringValue(source), SHA256: types.StringUnknown()}
		plan := tfsdk.Plan{Schema: s}
		require.False(t, plan.Set(ctx, &model).HasError())
		resp := resource.ModifyPlanResponse{Plan: plan}
		(&clientLogoResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: plan,
			State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}, &resp)
		var got clientLogoResourceModel
		require.False(t, resp.Plan.Get(ctx, &got).HasError())
		return resp, got
	}
	content := []byte("<svg/>")
	resp, got := run(clientLogoTestFile(t, "logo.svg", content))
	require.False(t, resp.Diagnostics.HasError())
	assert.Equal(t, clientLogoSHA256(content), got.SHA256.ValueString())
	assert.Equal(t, "app/light", got.ID.ValueString())

	resp, got = run(filepath.Join(t.TempDir(), "later.svg"))
	require.False(t, resp.Diagnostics.HasError())
	assert.Equal(t, 1, resp.Diagnostics.WarningsCount(), "a missing file is read at apply time")
	assert.True(t, got.SHA256.IsUnknown())

	resp, _ = run(clientLogoTestFile(t, "huge.png", clientLogoTestPNGHeader(5000, 5000)))
	assert.True(t, resp.Diagnostics.HasError(), "too many pixels is refused at plan")
}

func TestClientLogoResource_ImportAndValidators(t *testing.T) {
	ctx := context.Background()
	s := clientLogoTestSchema(t)
	for id, ok := range map[string]bool{"app/light": true, "app/dark": true, "app": false, "app/Dark": false, "bad id/light": false, "/light": false} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
		(&clientLogoResource{}).ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		assert.Equal(t, ok, !resp.Diagnostics.HasError(), id)
		if ok {
			var variant types.String
			resp.State.GetAttribute(ctx, path.Root("variant"), &variant)
			assert.Equal(t, strings.SplitN(id, "/", 2)[1], variant.ValueString())
		}
	}
	for source, ok := range map[string]bool{"logo.png": true, "a/b/Logo.JPEG": true, "logo.webp": true, "logo.bmp": false, "logo": false} {
		resp := &validator.StringResponse{}
		clientLogoSourceValidator{}.ValidateString(ctx, validator.StringRequest{Path: path.Root("source"), ConfigValue: types.StringValue(source)}, resp)
		assert.Equal(t, ok, !resp.Diagnostics.HasError(), source)
	}
	var meta resource.MetadataResponse
	(&clientLogoResource{}).Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pocketid"}, &meta)
	assert.Equal(t, "pocketid_client_logo", meta.TypeName)
}

// clientLogoCachingServer is Pocket ID behind a shared cache that holds the
// logo it last fetched and answers the next matching GET with it once. In
// "url" mode the cache keys on the full URL and ignores request headers; in
// "path" mode it ignores the query string except light= and honours
// Cache-Control: no-cache with Pragma: no-cache.
type clientLogoCachingServer struct {
	mu      sync.Mutex
	mode    string
	current []byte
	cache   map[string][]byte
	stale   int
}

func (s *clientLogoCachingServer) serve(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /api/oidc/clients/app":
			_, _ = fmt.Fprint(w, `{"id":"app","name":"app","callbackURLs":[],"hasLogo":true}`)
		case "POST /api/oidc/clients/app/logo":
			s.current = []byte("stored:new")
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/oidc/clients/app/logo":
			key := r.URL.RequestURI()
			bypass := false
			if s.mode == "path" {
				key = r.URL.Path + "?light=" + r.URL.Query().Get("light")
				bypass = r.Header.Get("Cache-Control") == "no-cache" && r.Header.Get("Pragma") == "no-cache"
			}
			if cached, ok := s.cache[key]; ok && !bypass {
				delete(s.cache, key)
				s.stale++
				_, _ = w.Write(cached)
				return
			}
			s.cache[key] = s.current
			_, _ = w.Write(s.current)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)
	return c
}

// A cache that saw the logo before an upload must not answer the read-back
// after it or the next refresh: the recorded image is the uploaded one, and
// the refresh shows no drift.
func TestClientLogoResource_ReadsBypassCaches(t *testing.T) {
	ctx := context.Background()
	content := []byte("<svg>new</svg>")
	source := clientLogoTestFile(t, "logo.svg", content)
	for _, mode := range []string{"url", "path"} {
		t.Run(mode, func(t *testing.T) {
			server := &clientLogoCachingServer{mode: mode, current: []byte("stored:old"), cache: map[string][]byte{}}
			r := &clientLogoResource{client: server.serve(t)}
			state := clientLogoStored("light")

			// A refresh before the upload puts the old logo into the cache.
			var diags diag.Diagnostics
			gone, record := r.refresh(ctx, &state, "", &diags)
			require.False(t, diags.HasError())
			require.False(t, gone)
			require.Equal(t, clientLogoSHA256([]byte("stored:old")), record)

			plan := clientLogoResourceModel{ID: types.StringValue("app/light"), ClientID: types.StringValue("app"), Variant: types.StringValue("light"),
				Source: types.StringValue(source), SHA256: types.StringValue(clientLogoSHA256(content))}
			uploaded, served := r.upload(ctx, &plan, &diags)
			require.False(t, diags.HasError())
			require.True(t, uploaded)
			assert.Equal(t, clientLogoSHA256([]byte("stored:new")), served, "the read-back sees the uploaded logo")

			after := plan
			gone, record = r.refresh(ctx, &after, served, &diags)
			require.False(t, diags.HasError())
			assert.False(t, gone)
			assert.Empty(t, record)
			assert.Equal(t, clientLogoSHA256(content), after.SHA256.ValueString(), "no drift, so no second upload")
			assert.Zero(t, server.stale, "no cached copy was ever served")
		})
	}
}

// The same bytes under another file type are another logo for Pocket ID,
// which stores and serves it with the type of the uploaded file name.
func TestClientLogoNeedsUpload(t *testing.T) {
	model := func(source, sum string) *clientLogoResourceModel {
		m := &clientLogoResourceModel{Source: types.StringValue(source), SHA256: types.StringValue(sum)}
		if source == "" {
			m.Source = types.StringNull()
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
		"nothing changed":               {model("logos/a.png", "h1"), model("logos/a.png", "h1"), false},
		"content changed":               {model("logos/a.png", "h1"), model("logos/a.png", "h2"), true},
		"content not known yet":         {model("logos/a.png", "h1"), model("logos/a.png", ""), true},
		"same bytes, other extension":   {model("logos/a.png", "h1"), model("logos/a.jpg", "h1"), true},
		"same bytes, jpg to jpeg":       {model("logos/a.jpg", "h1"), model("logos/a.jpeg", "h1"), true},
		"same bytes, other path":        {model("logos/a.png", "h1"), model("other/b.png", "h1"), false},
		"same bytes, other letter case": {model("logos/a.png", "h1"), model("logos/a.PNG", "h1"), false},
		"after an import":               {model("", "h1"), model("logos/a.svg", "h1"), true},
	} {
		assert.Equal(t, tc.upload, clientLogoNeedsUpload(tc.plan, tc.state), name)
	}
}
