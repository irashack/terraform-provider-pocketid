package resources

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// appImageFakeServer is a fake Pocket ID for one application image. served is what
// a GET returns (nil: no uploaded image), and an upload replaces it with
// the uploaded bytes passed through transform (Pocket ID strips metadata).
type appImageFakeServer struct {
	mu        sync.Mutex
	served    []byte
	transform func([]byte) []byte
	requests  []string
	getStatus int
	// cache, when set, stands for a shared cache in front of Pocket ID:
	// a GET for a URL it has seen gets the bytes served then.
	cache map[string][]byte
}

func (s *appImageFakeServer) client(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		switch r.Method {
		case http.MethodPut:
			require.NoError(t, r.ParseMultipartForm(1<<20))
			f, _, err := r.FormFile("file")
			require.NoError(t, err)
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(f)
			s.served = buf.Bytes()
			if s.transform != nil {
				s.served = s.transform(s.served)
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			if s.served == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"Image not found","code":"image_not_found"}`))
				return
			}
			s.served = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			if cached, ok := s.cache[r.URL.RequestURI()]; ok {
				_, _ = w.Write(cached)
				return
			}
			if s.cache != nil && s.served != nil {
				s.cache[r.URL.RequestURI()] = s.served
			}
			if s.getStatus != 0 {
				w.WriteHeader(s.getStatus)
				return
			}
			if s.served == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"Image not found","code":"image_not_found"}`))
				return
			}
			_, _ = w.Write(s.served)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
	require.NoError(t, err)
	return c
}

func appImageWriteFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	return path
}

const appImageSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`

func TestApplicationImageExtensionRules(t *testing.T) {
	for _, tc := range []struct {
		kind   client.ApplicationImage
		source string
		ok     bool
	}{
		{client.ApplicationImageLogoLight, "logo.svg", true},
		{client.ApplicationImageLogoLight, "Logo.PNG", true},
		{client.ApplicationImageLogoDark, "logo.heic", true},
		{client.ApplicationImageLogoLight, "logo.bmp", false},
		{client.ApplicationImageLogoLight, "logo", false},
		{client.ApplicationImageFavicon, "favicon.ico", true},
		{client.ApplicationImageFavicon, "favicon.jpg", false},
		{client.ApplicationImageEmailLogo, "mail.jpeg", true},
		{client.ApplicationImageEmailLogo, "mail.svg", false},
		{client.ApplicationImageBackground, "bg.webp", true},
		{client.ApplicationImageDefaultProfilePicture, "avatar.gif", true},
	} {
		problem := applicationImageExtensionProblem(tc.kind, tc.source)
		assert.Equal(t, tc.ok, problem == "", "%s %s: %s", tc.kind, tc.source, problem)
	}
}

// appImagePNGHeader returns a PNG whose header claims width x height; DecodeConfig
// reads only the header.
func appImagePNGHeader(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1))))
	data := buf.Bytes()
	// Signature (8), IHDR length (4), "IHDR" (4), then width and height.
	binary.BigEndian.PutUint32(data[16:20], width)
	binary.BigEndian.PutUint32(data[20:24], height)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	return data
}

func TestReadApplicationImageSourceLimits(t *testing.T) {
	small := appImageWriteFile(t, "logo.png", appImagePNGHeader(t, 4000, 4000))
	content, hash, err := readApplicationImageSource(client.ApplicationImageLogoLight, small)
	require.NoError(t, err)
	assert.Equal(t, appImageSHA256Hex(content), hash)

	tooManyPixels := appImageWriteFile(t, "logo.png", appImagePNGHeader(t, 4001, 4000))
	_, _, err = readApplicationImageSource(client.ApplicationImageLogoLight, tooManyPixels)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4001x4000 pixels; this provider uploads JPEG and PNG images of at most 16000000 pixels")

	// Not decodable: Pocket ID accepts it as it is, so it is not refused.
	_, _, err = readApplicationImageSource(client.ApplicationImageLogoLight, appImageWriteFile(t, "logo.png", []byte("not a png")))
	assert.NoError(t, err)

	tooLarge := appImageWriteFile(t, "background.webp", make([]byte, client.MaxApplicationImageBytes+1))
	_, _, err = readApplicationImageSource(client.ApplicationImageBackground, tooLarge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than")

	_, _, err = readApplicationImageSource(client.ApplicationImageFavicon, appImageWriteFile(t, "favicon.gif", []byte("GIF89a")))
	assert.Error(t, err)
}

func applicationImageTestSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	(&applicationImageResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

func applicationImageRaw(t *testing.T, s schema.Schema, m *applicationImageModel) tftypes.Value {
	t.Helper()
	if m == nil {
		return tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)
	}
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}
	require.False(t, state.Set(context.Background(), m).HasError())
	return state.Raw
}

func TestApplicationImageValidateConfig(t *testing.T) {
	s := applicationImageTestSchema(t)
	validate := func(kind, source string) diag.Diagnostics {
		var resp resource.ValidateConfigResponse
		(&applicationImageResource{}).ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config{Schema: s, Raw: applicationImageRaw(t, s, &applicationImageModel{
				ID: types.StringNull(), Kind: types.StringValue(kind), Source: types.StringValue(source), SHA256: types.StringNull(),
			})},
		}, &resp)
		return resp.Diagnostics
	}
	assert.False(t, validate("favicon", "icons/favicon.svg").HasError())
	diags := validate("favicon", "icons/favicon.jpg")
	require.True(t, diags.HasError())
	assert.Contains(t, diags.Errors()[0].Detail(), "accepts only ico, png, svg for the favicon")
}

func TestApplicationImageModifyPlan(t *testing.T) {
	s := applicationImageTestSchema(t)
	r := &applicationImageResource{}
	plan := func(source string) (types.String, diag.Diagnostics) {
		planned := &applicationImageModel{ID: types.StringUnknown(), Kind: types.StringValue("logo_light"), Source: types.StringValue(source), SHA256: types.StringUnknown()}
		raw := applicationImageRaw(t, s, planned)
		resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: raw}}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: tfsdk.Plan{Schema: s, Raw: raw}, State: tfsdk.State{Schema: s, Raw: applicationImageRaw(t, s, nil)}}, &resp)
		var got applicationImageModel
		require.False(t, resp.Plan.Get(context.Background(), &got).HasError())
		return got.SHA256, resp.Diagnostics
	}
	source := appImageWriteFile(t, "logo.svg", []byte(appImageSVG))
	sha, diags := plan(source)
	require.False(t, diags.HasError(), "%v", diags)
	assert.Equal(t, appImageSHA256Hex([]byte(appImageSVG)), sha.ValueString())

	sha, diags = plan(filepath.Join(t.TempDir(), "later.svg"))
	assert.False(t, diags.HasError())
	assert.Len(t, diags.Warnings(), 1)
	assert.True(t, sha.IsUnknown(), "a file that does not exist yet is read at apply")

	// Destroying an image Pocket ID cannot remove warns at plan time.
	state := applicationImageRaw(t, s, &applicationImageModel{ID: types.StringValue("favicon"), Kind: types.StringValue("favicon"), Source: types.StringValue("f.ico"), SHA256: types.StringValue("x")})
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: applicationImageRaw(t, s, nil)}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: tfsdk.Plan{Schema: s, Raw: applicationImageRaw(t, s, nil)}, State: tfsdk.State{Schema: s, Raw: state}}, &resp)
	require.Len(t, resp.Diagnostics.Warnings(), 1)
	assert.Contains(t, resp.Diagnostics.Warnings()[0].Detail(), "leaves the image in place")
}

func TestApplicationImageUploadRecordsServedImage(t *testing.T) {
	server := &appImageFakeServer{transform: func(b []byte) []byte { return append([]byte("stripped:"), b...) }}
	r := &applicationImageResource{client: server.client(t)}
	source := appImageWriteFile(t, "logo.svg", []byte(appImageSVG))
	plan := &applicationImageModel{ID: types.StringUnknown(), Kind: types.StringValue("logo_dark"), Source: types.StringValue(source), SHA256: types.StringValue(appImageSHA256Hex([]byte(appImageSVG)))}
	var diags diag.Diagnostics
	uploaded, served := r.upload(context.Background(), plan, &diags)
	require.False(t, diags.HasError(), "%v", diags)
	require.True(t, uploaded)
	assert.Equal(t, appImageSHA256Hex([]byte("stripped:"+appImageSVG)), served)
	assert.Equal(t, appImageSHA256Hex([]byte(appImageSVG)), plan.SHA256.ValueString())
	assert.Equal(t, "logo_dark", plan.ID.ValueString())
	require.Len(t, server.requests, 2)
	assert.Equal(t, "PUT /api/application-images/logo?light=false", server.requests[0])
	assert.True(t, strings.HasPrefix(server.requests[1], "GET /api/application-images/logo?default=false&light=false&nocache="), server.requests[1])

	// A file changed since the plan is not uploaded.
	server.requests = nil
	plan.SHA256 = types.StringValue("0000")
	uploaded, _ = r.upload(context.Background(), plan, &diags)
	assert.False(t, uploaded)
	assert.Contains(t, diags.Errors()[0].Detail(), "changed after the plan")
	assert.Empty(t, server.requests)
}

func TestApplicationImageRefresh(t *testing.T) {
	ctx := context.Background()
	stateFor := func(sha types.String) *applicationImageModel {
		return &applicationImageModel{ID: types.StringValue("background"), Kind: types.StringValue("background"), Source: types.StringValue("bg.webp"), SHA256: sha}
	}
	uploadedHash := appImageSHA256Hex([]byte("source"))

	t.Run("unchanged", func(t *testing.T) {
		server := &appImageFakeServer{served: []byte("served")}
		r := &applicationImageResource{client: server.client(t)}
		state := stateFor(types.StringValue(uploadedHash))
		var diags diag.Diagnostics
		gone, record := r.refresh(ctx, state, appImageSHA256Hex([]byte("served")), &diags)
		require.False(t, diags.HasError())
		assert.False(t, gone)
		assert.Empty(t, record)
		assert.Equal(t, uploadedHash, state.SHA256.ValueString())
	})
	t.Run("replaced outside Terraform", func(t *testing.T) {
		server := &appImageFakeServer{served: []byte("someone else's")}
		r := &applicationImageResource{client: server.client(t)}
		state := stateFor(types.StringValue(uploadedHash))
		var diags diag.Diagnostics
		gone, _ := r.refresh(ctx, state, appImageSHA256Hex([]byte("served")), &diags)
		assert.False(t, gone)
		assert.Equal(t, appImageSHA256Hex([]byte("someone else's")), state.SHA256.ValueString(), "the plan uploads source again")
	})
	t.Run("removed outside Terraform", func(t *testing.T) {
		server := &appImageFakeServer{}
		r := &applicationImageResource{client: server.client(t)}
		var diags diag.Diagnostics
		gone, _ := r.refresh(ctx, stateFor(types.StringValue(uploadedHash)), appImageSHA256Hex([]byte("served")), &diags)
		assert.False(t, diags.HasError())
		assert.True(t, gone)
	})
	t.Run("a 404 that is not Pocket ID's is an error", func(t *testing.T) {
		server := &appImageFakeServer{served: []byte("served"), getStatus: http.StatusNotFound}
		r := &applicationImageResource{client: server.client(t)}
		var diags diag.Diagnostics
		gone, _ := r.refresh(ctx, stateFor(types.StringValue(uploadedHash)), "", &diags)
		assert.False(t, gone)
		assert.True(t, diags.HasError())
	})
	t.Run("imported", func(t *testing.T) {
		server := &appImageFakeServer{served: []byte("served")}
		r := &applicationImageResource{client: server.client(t)}
		state := &applicationImageModel{ID: types.StringValue("background"), Kind: types.StringValue("background"), Source: types.StringNull(), SHA256: types.StringNull()}
		var diags diag.Diagnostics
		gone, record := r.refresh(ctx, state, "", &diags)
		assert.False(t, gone)
		assert.Equal(t, appImageSHA256Hex([]byte("served")), record)
		assert.Equal(t, appImageSHA256Hex([]byte("served")), state.SHA256.ValueString())
	})
}

func TestApplicationImageDelete(t *testing.T) {
	s := applicationImageTestSchema(t)
	del := func(t *testing.T, server *appImageFakeServer, kind string) diag.Diagnostics {
		r := &applicationImageResource{client: server.client(t)}
		state := applicationImageRaw(t, s, &applicationImageModel{ID: types.StringValue(kind), Kind: types.StringValue(kind), Source: types.StringValue("x.png"), SHA256: types.StringValue("x")})
		var resp resource.DeleteResponse
		r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: state}}, &resp)
		return resp.Diagnostics
	}
	server := &appImageFakeServer{served: []byte("x")}
	diags := del(t, server, "default_profile_picture")
	assert.False(t, diags.HasError())
	assert.Equal(t, []string{"DELETE /api/application-images/default-profile-picture"}, server.requests)

	server = &appImageFakeServer{}
	assert.False(t, del(t, server, "logo_light").HasError(), "an image that is already gone is deleted")

	server = &appImageFakeServer{served: []byte("x")}
	diags = del(t, server, "email_logo")
	assert.False(t, diags.HasError())
	require.Len(t, diags.Warnings(), 1)
	assert.True(t, strings.Contains(diags.Warnings()[0].Detail(), "stays in place"))
	assert.Empty(t, server.requests)
}

// A cache in front of Pocket ID may serve an image it saw before an upload.
// Every read uses a URL of its own, so the read-back after an upload and the
// next refresh see the new image: the baseline is right and no drift shows.
func TestApplicationImageReadsBypassCaches(t *testing.T) {
	ctx := context.Background()
	server := &appImageFakeServer{served: []byte("old image"), cache: map[string][]byte{}}
	r := &applicationImageResource{client: server.client(t)}

	// A refresh before the upload puts the old image into the cache.
	before := &applicationImageModel{ID: types.StringValue("logo_light"), Kind: types.StringValue("logo_light"), Source: types.StringNull(), SHA256: types.StringNull()}
	var diags diag.Diagnostics
	_, record := r.refresh(ctx, before, "", &diags)
	require.False(t, diags.HasError())
	require.Equal(t, appImageSHA256Hex([]byte("old image")), record)

	source := appImageWriteFile(t, "logo.svg", []byte(appImageSVG))
	plan := &applicationImageModel{ID: types.StringUnknown(), Kind: types.StringValue("logo_light"), Source: types.StringValue(source), SHA256: types.StringValue(appImageSHA256Hex([]byte(appImageSVG)))}
	uploaded, served := r.upload(ctx, plan, &diags)
	require.False(t, diags.HasError(), "%v", diags)
	require.True(t, uploaded)
	assert.Equal(t, appImageSHA256Hex([]byte(appImageSVG)), served, "the read-back sees the uploaded image, not the cached one")

	state := *plan
	gone, record := r.refresh(ctx, &state, served, &diags)
	require.False(t, diags.HasError())
	assert.False(t, gone)
	assert.Empty(t, record)
	assert.Equal(t, appImageSHA256Hex([]byte(appImageSVG)), state.SHA256.ValueString(), "no drift: nothing to upload again")

	gets := map[string]bool{}
	for _, request := range server.requests {
		if strings.HasPrefix(request, "GET ") {
			assert.False(t, gets[request], "a read reused a URL: %s", request)
			gets[request] = true
		}
	}
	assert.Len(t, gets, 3)
}

// The same bytes under another extension are uploaded again (Pocket ID
// serves the type of the uploaded file name); another path or letter case
// with the same content and type is not.
func TestApplicationImageNeedsUpload(t *testing.T) {
	hash := types.StringValue(appImageSHA256Hex([]byte("bytes")))
	model := func(source string, sha types.String) *applicationImageModel {
		return &applicationImageModel{Kind: types.StringValue("background"), Source: types.StringValue(source), SHA256: sha}
	}
	state := model("images/background.png", hash)
	assert.True(t, applicationImageNeedsUpload(model("images/background.jpg", hash), state), "extension changed")
	assert.False(t, applicationImageNeedsUpload(model("other/BACKGROUND.PNG", hash), state), "same content and type")
	assert.True(t, applicationImageNeedsUpload(model("images/background.png", types.StringValue("other")), state), "content changed")
	assert.True(t, applicationImageNeedsUpload(model("images/background.png", types.StringUnknown()), state), "content not known yet")
	imported := &applicationImageModel{Kind: types.StringValue("background"), Source: types.StringNull(), SHA256: hash}
	assert.True(t, applicationImageNeedsUpload(model("images/background.png", hash), imported), "imported: type not known")
}
