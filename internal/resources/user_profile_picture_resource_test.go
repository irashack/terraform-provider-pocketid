package resources_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/resources"
)

const ppUser = "00000000-0000-4000-8000-000000000001"

// ppServer is a fake Pocket ID that stores one user's picture the way the real
// one does: what it serves afterwards is not the uploaded file but a different
// image derived from it, and without a custom picture it serves a default.
type ppServer struct {
	t      *testing.T
	mu     sync.Mutex
	exists bool
	custom []byte // the stored picture, nil for none
	log    []string
	// uploads records each upload: field name, file name, part content type, size.
	uploads []ppUpload
	// uploadStatus, when not zero, answers every upload with it.
	uploadStatus int
	uploadBody   map[string]any
	// getStatus, when not zero, answers every GET of the picture with it.
	getStatus int
	// failGetAfterUpload makes the GET of the picture fail once an upload happened.
	failGetAfterUpload bool
	// deleteStatus, when not zero, answers every DELETE with it.
	deleteStatus int
}

type ppUpload struct {
	Field, FileName, ContentType string
	Size                         int
}

func newPPServer(t *testing.T) (*ppServer, *client.Client) {
	t.Helper()
	s := &ppServer{t: t, exists: true}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return s, c
}

func (s *ppServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, r.Method+" "+r.URL.Path)
	userGone := func() bool {
		if s.exists {
			return false
		}
		gmReply(w, 404, map[string]any{"error": "User not found", "code": "user_not_found"})
		return true
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/users/"+ppUser+"/profile-picture.png":
		if s.getStatus != 0 {
			gmReply(w, s.getStatus, map[string]any{"error": "no"})
			return
		}
		if s.failGetAfterUpload && len(s.uploads) > 0 {
			gmReply(w, 403, map[string]any{"error": "no"})
			return
		}
		if userGone() {
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(ppServed(s.custom))
	case r.Method == http.MethodPut && r.URL.Path == "/api/users/"+ppUser+"/profile-picture":
		if s.uploadStatus != 0 {
			gmReply(w, s.uploadStatus, s.uploadBody)
			return
		}
		if userGone() {
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			gmReply(w, 400, map[string]any{"error": "bad", "code": "validation_failed"})
			return
		}
		for field, headers := range r.MultipartForm.File {
			header := headers[0]
			f, _ := header.Open()
			content, _ := io.ReadAll(f)
			_ = f.Close()
			s.uploads = append(s.uploads, ppUpload{field, header.Filename, header.Header.Get("Content-Type"), len(content)})
			s.custom = content
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && r.URL.Path == "/api/users/"+ppUser+"/profile-picture":
		if s.deleteStatus != 0 {
			gmReply(w, s.deleteStatus, map[string]any{"error": "no"})
			return
		}
		if userGone() {
			return
		}
		s.custom = nil
		w.WriteHeader(http.StatusNoContent)
	default:
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		gmReply(w, 404, map[string]any{"error": "API endpoint not found"})
	}
}

// ppServed is what the server serves for a stored picture: never the uploaded
// bytes themselves.
func ppServed(custom []byte) []byte {
	if custom == nil {
		return []byte("default-initials-picture")
	}
	digest := sha256.Sum256(custom)
	return append([]byte("scaled-300x300:"), digest[:]...)
}

func ppDigest(b []byte) string {
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func (s *ppServer) uploadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

func (s *ppServer) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, l := range s.log {
		if len(l) > len(method) && l[:len(method)] == method {
			n++
		}
	}
	return n
}

func ppPNG(t *testing.T, w, h int, mark uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.Pix[0] = mark
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func ppFile(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "picture.png")
	require.NoError(t, os.WriteFile(p, content, 0o600))
	return p
}

func ppResource(t *testing.T, c *client.Client) (resource.Resource, schema.Schema) {
	t.Helper()
	r := resources.NewUserProfilePictureResource()
	cfg := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, cfg)
	require.False(t, cfg.Diagnostics.HasError())
	sch := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, sch)
	require.False(t, sch.Diagnostics.HasError())
	return r, sch.Schema
}

// ppObject builds a resource value. An empty digest string is null, "?" is unknown.
func ppObject(ctx context.Context, sch schema.Schema, id, source, sha, stored string) tftypes.Value {
	str := func(v string) tftypes.Value {
		switch v {
		case "":
			return tftypes.NewValue(tftypes.String, nil)
		case "?":
			return tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
		}
		return tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
		"id": str(id), "user_id": str(ppUser), "source": str(source), "sha256": str(sha), "stored_sha256": str(stored),
	})
}

func ppAttr(t *testing.T, state tfsdk.State, name string) (value string, null bool) {
	t.Helper()
	var s *string
	require.False(t, state.GetAttribute(context.Background(), path.Root(name), &s).HasError())
	if s == nil {
		return "", true
	}
	return *s, false
}

func ppCreate(t *testing.T, r resource.Resource, sch schema.Schema, source, plannedSHA string) *resource.CreateResponse {
	t.Helper()
	ctx := context.Background()
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
	planned := plannedSHA
	if planned == "" {
		planned = "?"
	}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: ppObject(ctx, sch, "?", source, planned, "?")}}, resp)
	return resp
}

func ppModifyPlan(t *testing.T, r resource.Resource, sch schema.Schema, state *tftypes.Value, source string) *resource.ModifyPlanResponse {
	t.Helper()
	ctx := context.Background()
	plan := tfsdk.Plan{Schema: sch, Raw: ppObject(ctx, sch, "?", source, "?", "?")}
	req := resource.ModifyPlanRequest{Plan: plan, Config: tfsdk.Config{Schema: sch, Raw: plan.Raw}, State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
	if state != nil {
		req.State = tfsdk.State{Schema: sch, Raw: *state}
		req.Plan.Raw = ppObject(ctx, sch, ppUser, source, "?", "?")
	}
	resp := &resource.ModifyPlanResponse{Plan: req.Plan}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(ctx, req, resp)
	return resp
}

func TestUserProfilePictureResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := resources.NewUserProfilePictureResource()
	meta := &resource.MetadataResponse{}
	r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pocketid"}, meta)
	assert.Equal(t, "pocketid_user_profile_picture", meta.TypeName)

	_, sch := ppResource(t, nil)
	assert.True(t, sch.Attributes["user_id"].IsRequired())
	assert.True(t, sch.Attributes["source"].IsRequired())
	assert.True(t, sch.Attributes["sha256"].IsComputed())
	assert.False(t, sch.Attributes["sha256"].IsOptional())
	_, isImportable := r.(resource.ResourceWithImportState)
	assert.False(t, isImportable, "the source file cannot be recovered from the server")
	for _, claim := range []string{"300x300", "16 million pixels", "10 MiB", "stored_sha256", "cannot be detected", "no import",
		"terraform state rm", "no conditional delete", "that race cannot be closed"} {
		assert.Contains(t, sch.MarkdownDescription, claim)
	}
}

// Create uploads the file as the "file" part, and records the digest of the
// file and of the picture the server then serves, which is not the same thing.
func TestUserProfilePictureResource_Create(t *testing.T) {
	s, c := newPPServer(t)
	r, sch := ppResource(t, c)
	content := ppPNG(t, 60, 40, 1)

	resp := ppCreate(t, r, sch, ppFile(t, content), ppDigest(content))
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	require.Len(t, s.uploads, 1)
	assert.Equal(t, "file", s.uploads[0].Field)
	assert.Equal(t, "image/png", s.uploads[0].ContentType)
	assert.Equal(t, len(content), s.uploads[0].Size)

	sha, _ := ppAttr(t, resp.State, "sha256")
	stored, null := ppAttr(t, resp.State, "stored_sha256")
	id, _ := ppAttr(t, resp.State, "id")
	assert.Equal(t, ppDigest(content), sha)
	assert.False(t, null)
	assert.Equal(t, ppDigest(ppServed(content)), stored)
	assert.NotEqual(t, sha, stored, "the server serves a different image than the one uploaded")
	assert.Equal(t, ppUser, id)
}

// A file that changed between plan and apply is not uploaded.
func TestUserProfilePictureResource_Create_FileChangedSincePlan(t *testing.T) {
	s, c := newPPServer(t)
	r, sch := ppResource(t, c)
	planned := ppPNG(t, 60, 40, 1)
	resp := ppCreate(t, r, sch, ppFile(t, ppPNG(t, 60, 40, 2)), ppDigest(planned))
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, "Source file changed since the plan", resp.Diagnostics.Errors()[0].Summary())
	assert.Zero(t, s.uploadCount())
	assert.True(t, resp.State.Raw.IsNull())
}

func TestUserProfilePictureResource_Create_FileProblemsSendNothing(t *testing.T) {
	s, c := newPPServer(t)
	r, sch := ppResource(t, c)

	missing := ppCreate(t, r, sch, filepath.Join(t.TempDir(), "absent.png"), "")
	require.True(t, missing.Diagnostics.HasError())
	assert.Equal(t, "Source file does not exist", missing.Diagnostics.Errors()[0].Summary())

	notImage := ppCreate(t, r, sch, ppFile(t, []byte("plain text")), "")
	require.True(t, notImage.Diagnostics.HasError())
	assert.Equal(t, "Unusable profile picture file", notImage.Diagnostics.Errors()[0].Summary())
	assert.Empty(t, s.log, "no request was made")
}

func TestUserProfilePictureResource_Create_ServerRefusals(t *testing.T) {
	content := ppPNG(t, 60, 40, 1)

	t.Run("definite rejection", func(t *testing.T) {
		s, c := newPPServer(t)
		s.uploadStatus, s.uploadBody = 400, map[string]any{"error": "bad image", "code": "invalid_image"}
		r, sch := ppResource(t, c)
		resp := ppCreate(t, r, sch, ppFile(t, content), "")
		require.True(t, resp.Diagnostics.HasError())
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "Nothing was changed")
		assert.True(t, resp.State.Raw.IsNull())
	})

	t.Run("user missing", func(t *testing.T) {
		s, c := newPPServer(t)
		s.exists = false
		r, sch := ppResource(t, c)
		resp := ppCreate(t, r, sch, ppFile(t, content), "")
		require.True(t, resp.Diagnostics.HasError())
		assert.Equal(t, "User not found", resp.Diagnostics.Errors()[0].Summary())
	})

	// An uncertain failure is reported as uncertain and the upload is not repeated.
	t.Run("server error", func(t *testing.T) {
		s, c := newPPServer(t)
		s.uploadStatus, s.uploadBody = 500, map[string]any{"error": "boom"}
		r, sch := ppResource(t, c)
		resp := ppCreate(t, r, sch, ppFile(t, content), "")
		require.True(t, resp.Diagnostics.HasError())
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "may or may not")
		assert.Equal(t, 1, s.count("PUT"))
	})
}

// The picture was uploaded but cannot be read back: the resource is recorded
// (it exists) with no baseline, and a warning says the baseline is missing.
func TestUserProfilePictureResource_Create_ReadBackFailureStillRecordsTheUpload(t *testing.T) {
	s, c := newPPServer(t)
	s.failGetAfterUpload = true
	r, sch := ppResource(t, c)
	content := ppPNG(t, 60, 40, 1)

	resp := ppCreate(t, r, sch, ppFile(t, content), ppDigest(content))
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Len(t, resp.Diagnostics.Warnings(), 1)
	sha, _ := ppAttr(t, resp.State, "sha256")
	_, storedNull := ppAttr(t, resp.State, "stored_sha256")
	assert.Equal(t, ppDigest(content), sha)
	assert.True(t, storedNull)
}

// At plan time the file's digest becomes the planned sha256, so a changed file
// shows as a change.
func TestUserProfilePictureResource_ModifyPlan(t *testing.T) {
	ctx := context.Background()
	_, c := newPPServer(t)
	r, sch := ppResource(t, c)
	content := ppPNG(t, 60, 40, 1)
	source := ppFile(t, content)

	planned := func(resp *resource.ModifyPlanResponse) (sha, stored string, shaUnknown, storedUnknown bool) {
		raw := map[string]tftypes.Value{}
		require.NoError(t, resp.Plan.Raw.As(&raw))
		shaUnknown, storedUnknown = !raw["sha256"].IsKnown(), !raw["stored_sha256"].IsKnown()
		if raw["sha256"].IsKnown() && !raw["sha256"].IsNull() {
			_ = raw["sha256"].As(&sha)
		}
		if raw["stored_sha256"].IsKnown() && !raw["stored_sha256"].IsNull() {
			_ = raw["stored_sha256"].As(&stored)
		}
		return
	}

	t.Run("new resource", func(t *testing.T) {
		resp := ppModifyPlan(t, r, sch, nil, source)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		sha, _, _, storedUnknown := planned(resp)
		assert.Equal(t, ppDigest(content), sha)
		assert.True(t, storedUnknown)
	})

	t.Run("unchanged file keeps the stored digest", func(t *testing.T) {
		state := ppObject(ctx, sch, ppUser, source, ppDigest(content), "stored-digest")
		resp := ppModifyPlan(t, r, sch, &state, source)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		sha, stored, _, storedUnknown := planned(resp)
		assert.Equal(t, ppDigest(content), sha)
		assert.Equal(t, "stored-digest", stored)
		assert.False(t, storedUnknown)
	})

	t.Run("changed file plans a new digest", func(t *testing.T) {
		state := ppObject(ctx, sch, ppUser, source, "old-digest", "stored-digest")
		resp := ppModifyPlan(t, r, sch, &state, source)
		sha, _, _, storedUnknown := planned(resp)
		assert.Equal(t, ppDigest(content), sha)
		assert.True(t, storedUnknown, "the upload will produce a new stored digest")
	})

	t.Run("drift cleared the digest in state", func(t *testing.T) {
		state := ppObject(ctx, sch, ppUser, source, "", "stored-digest")
		resp := ppModifyPlan(t, r, sch, &state, source)
		sha, _, _, storedUnknown := planned(resp)
		assert.Equal(t, ppDigest(content), sha, "a known digest against null in state is a visible change")
		assert.True(t, storedUnknown)
	})

	t.Run("file does not exist yet", func(t *testing.T) {
		resp := ppModifyPlan(t, r, sch, nil, filepath.Join(t.TempDir(), "later.png"))
		require.False(t, resp.Diagnostics.HasError())
		assert.Len(t, resp.Diagnostics.Warnings(), 1)
		_, _, shaUnknown, _ := planned(resp)
		assert.True(t, shaUnknown)
	})

	t.Run("unusable file is refused at plan time", func(t *testing.T) {
		resp := ppModifyPlan(t, r, sch, nil, ppFile(t, []byte("not an image")))
		require.True(t, resp.Diagnostics.HasError())
		assert.Equal(t, "Unusable profile picture file", resp.Diagnostics.Errors()[0].Summary())
	})

	t.Run("too many pixels is refused at plan time", func(t *testing.T) {
		resp := ppModifyPlan(t, r, sch, nil, ppFile(t, ppPNG(t, 4001, 4000, 0)))
		require.True(t, resp.Diagnostics.HasError())
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "16000000")
	})
}

func ppRead(t *testing.T, r resource.Resource, sch schema.Schema, sha, stored string) *resource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	state := tfsdk.State{Schema: sch, Raw: ppObject(ctx, sch, ppUser, "/some/file.png", sha, stored)}
	resp := &resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, resp)
	return resp
}

// Read compares what the server serves with what it served after the upload.
// The recorded digest stays the one the last upload produced: it is what Delete
// holds the served picture against.
func TestUserProfilePictureResource_Read_DetectsAChangedPicture(t *testing.T) {
	s, c := newPPServer(t)
	r, sch := ppResource(t, c)
	content := ppPNG(t, 60, 40, 1)
	s.custom = content
	stored := ppDigest(ppServed(content))

	same := ppRead(t, r, sch, "file-digest", stored)
	require.False(t, same.Diagnostics.HasError(), "%v", same.Diagnostics)
	sha, _ := ppAttr(t, same.State, "sha256")
	assert.Equal(t, "file-digest", sha, "no drift: the state is unchanged")

	// Replaced outside Terraform.
	s.custom = ppPNG(t, 60, 40, 9)
	replaced := ppRead(t, r, sch, "file-digest", stored)
	require.False(t, replaced.Diagnostics.HasError(), "%v", replaced.Diagnostics)
	_, null := ppAttr(t, replaced.State, "sha256")
	assert.True(t, null, "the file digest is cleared so the next plan uploads the file again")
	keptStored, _ := ppAttr(t, replaced.State, "stored_sha256")
	assert.Equal(t, stored, keptStored, "a picture found at refresh is not the one that was uploaded")

	// Removed outside Terraform: the default is served.
	s.custom = nil
	removed := ppRead(t, r, sch, "file-digest", stored)
	_, null = ppAttr(t, removed.State, "sha256")
	assert.True(t, null)
	keptStored, _ = ppAttr(t, removed.State, "stored_sha256")
	assert.Equal(t, stored, keptStored)
}

// With no record of what was uploaded (the read-back failed), nothing the
// server serves can be taken for it: Read adopts no baseline and asks for
// another upload, which records one.
func TestUserProfilePictureResource_Read_DoesNotAdoptAMissingBaseline(t *testing.T) {
	s, c := newPPServer(t)
	r, sch := ppResource(t, c)
	s.custom = ppPNG(t, 60, 40, 1)

	resp := ppRead(t, r, sch, "file-digest", "")
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	_, shaNull := ppAttr(t, resp.State, "sha256")
	_, storedNull := ppAttr(t, resp.State, "stored_sha256")
	assert.True(t, shaNull, "the file digest is cleared so the next plan uploads the file and records the picture")
	assert.True(t, storedNull, "whatever is served now may be someone else's picture")
}

func TestUserProfilePictureResource_Read_UserGone(t *testing.T) {
	s, c := newPPServer(t)
	s.exists = false
	r, sch := ppResource(t, c)
	resp := ppRead(t, r, sch, "file-digest", "stored")
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.True(t, resp.State.Raw.IsNull())
}

func TestUserProfilePictureResource_Read_OtherFailuresKeepState(t *testing.T) {
	s, c := newPPServer(t)
	s.getStatus = 404 // a bare 404 does not prove the user is gone
	r, sch := ppResource(t, c)
	resp := ppRead(t, r, sch, "file-digest", "stored")
	require.True(t, resp.Diagnostics.HasError())
	assert.False(t, resp.State.Raw.IsNull())
}

func ppUpdate(t *testing.T, r resource.Resource, sch schema.Schema, priorSource, newSource, priorSHA, plannedSHA, stored string) *resource.UpdateResponse {
	t.Helper()
	ctx := context.Background()
	state := tfsdk.State{Schema: sch, Raw: ppObject(ctx, sch, ppUser, priorSource, priorSHA, stored)}
	resp := &resource.UpdateResponse{State: state}
	storedPlan := stored
	if plannedSHA != priorSHA || priorSHA == "" {
		storedPlan = "?"
	}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: sch, Raw: ppObject(ctx, sch, ppUser, newSource, plannedSHA, storedPlan)},
		State: state,
	}, resp)
	return resp
}

func TestUserProfilePictureResource_Update(t *testing.T) {
	newContent := ppPNG(t, 60, 40, 5)
	oldContent := ppPNG(t, 60, 40, 1)

	t.Run("changed content uploads", func(t *testing.T) {
		s, c := newPPServer(t)
		r, sch := ppResource(t, c)
		resp := ppUpdate(t, r, sch, "/old.png", ppFile(t, newContent), ppDigest(oldContent), ppDigest(newContent), "old-stored")
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assert.Equal(t, 1, s.uploadCount())
		stored, _ := ppAttr(t, resp.State, "stored_sha256")
		assert.Equal(t, ppDigest(ppServed(newContent)), stored)
	})

	t.Run("the same content at another path uploads nothing", func(t *testing.T) {
		s, c := newPPServer(t)
		r, sch := ppResource(t, c)
		resp := ppUpdate(t, r, sch, "/old.png", ppFile(t, oldContent), ppDigest(oldContent), ppDigest(oldContent), "old-stored")
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assert.Zero(t, s.uploadCount())
		assert.Zero(t, s.count("GET"))
		source, _ := ppAttr(t, resp.State, "source")
		stored, _ := ppAttr(t, resp.State, "stored_sha256")
		assert.NotEqual(t, "/old.png", source, "the new path is recorded")
		assert.Equal(t, "old-stored", stored)
	})

	t.Run("a picture found changed is uploaded again", func(t *testing.T) {
		s, c := newPPServer(t)
		r, sch := ppResource(t, c)
		// Read cleared sha256 (null) after finding the picture replaced.
		resp := ppUpdate(t, r, sch, "/old.png", ppFile(t, oldContent), "", ppDigest(oldContent), "served-now")
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assert.Equal(t, 1, s.uploadCount())
	})

	t.Run("a file that changed since the plan uploads nothing", func(t *testing.T) {
		s, c := newPPServer(t)
		r, sch := ppResource(t, c)
		resp := ppUpdate(t, r, sch, "/old.png", ppFile(t, newContent), ppDigest(oldContent), ppDigest(ppPNG(t, 60, 40, 7)), "old-stored")
		require.True(t, resp.Diagnostics.HasError())
		assert.Zero(t, s.uploadCount())
	})
}

func ppDelete(t *testing.T, r resource.Resource, sch schema.Schema, stored string) *resource.DeleteResponse {
	t.Helper()
	return ppDeleteState(t, r, sch, ppObject(context.Background(), sch, ppUser, "/x.png", "sha", stored))
}

func ppDeleteState(t *testing.T, r resource.Resource, sch schema.Schema, raw tftypes.Value) *resource.DeleteResponse {
	t.Helper()
	state := tfsdk.State{Schema: sch, Raw: raw}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)
	return resp
}

func TestUserProfilePictureResource_Delete(t *testing.T) {
	uploaded := ppPNG(t, 60, 40, 1)
	uploadedServed := ppDigest(ppServed(uploaded))

	t.Run("restores the default when the picture is the one uploaded", func(t *testing.T) {
		s, c := newPPServer(t)
		s.custom = uploaded
		r, sch := ppResource(t, c)
		resp := ppDelete(t, r, sch, uploadedServed)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assert.Nil(t, s.custom)
		assert.Equal(t, 1, s.count("DELETE"))
	})

	// Someone replaced the picture after the plan: it is not ours to remove.
	t.Run("a replacement picture is refused and kept", func(t *testing.T) {
		s, c := newPPServer(t)
		s.custom = ppPNG(t, 60, 40, 9)
		r, sch := ppResource(t, c)
		resp := ppDelete(t, r, sch, uploadedServed)
		require.True(t, resp.Diagnostics.HasError())
		assert.Equal(t, "The stored picture is not the one this resource uploaded", resp.Diagnostics.Errors()[0].Summary())
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "terraform state rm")
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "apply it again")
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "Nothing was removed")
		assert.Zero(t, s.count("DELETE"), "no delete request was sent")
		assert.NotNil(t, s.custom)
		assert.False(t, resp.State.Raw.IsNull(), "the resource stays in the state")
	})

	t.Run("a picture removed outside Terraform is refused too", func(t *testing.T) {
		s, c := newPPServer(t)
		s.custom = nil // the default is served
		r, sch := ppResource(t, c)
		resp := ppDelete(t, r, sch, uploadedServed)
		require.True(t, resp.Diagnostics.HasError())
		assert.Zero(t, s.count("DELETE"))
	})

	t.Run("no record of the upload is refused and explained", func(t *testing.T) {
		s, c := newPPServer(t)
		s.custom = uploaded
		r, sch := ppResource(t, c)
		resp := ppDelete(t, r, sch, "")
		require.True(t, resp.Diagnostics.HasError())
		assert.Equal(t, "Cannot tell whether the stored picture is the one this resource uploaded", resp.Diagnostics.Errors()[0].Summary())
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "no record")
		assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "terraform state rm")
		assert.Zero(t, s.count("DELETE"))
		assert.NotNil(t, s.custom)
	})

	// A picture that a refresh found replaced is not removed by the destroy
	// that follows the refresh: the state Read leaves still holds the upload's
	// digest.
	t.Run("a replacement found at refresh survives the destroy", func(t *testing.T) {
		s, c := newPPServer(t)
		s.custom = uploaded
		r, sch := ppResource(t, c)
		s.custom = ppPNG(t, 60, 40, 9)
		refreshed := ppRead(t, r, sch, "file-digest", uploadedServed)
		require.False(t, refreshed.Diagnostics.HasError(), "%v", refreshed.Diagnostics)
		resp := ppDeleteState(t, r, sch, refreshed.State.Raw)
		require.True(t, resp.Diagnostics.HasError())
		assert.Zero(t, s.count("DELETE"))
		assert.NotNil(t, s.custom)
	})

	t.Run("the check reads the picture again, so a later replacement is seen", func(t *testing.T) {
		s, c := newPPServer(t)
		s.custom = uploaded
		r, sch := ppResource(t, c)
		refreshed := ppRead(t, r, sch, "file-digest", uploadedServed) // no drift at refresh
		require.False(t, refreshed.Diagnostics.HasError(), "%v", refreshed.Diagnostics)
		s.custom = ppPNG(t, 60, 40, 9) // replaced between the refresh and the destroy
		resp := ppDeleteState(t, r, sch, refreshed.State.Raw)
		require.True(t, resp.Diagnostics.HasError())
		assert.Zero(t, s.count("DELETE"))
	})

	t.Run("user already gone", func(t *testing.T) {
		s, c := newPPServer(t)
		s.exists = false
		r, sch := ppResource(t, c)
		assert.False(t, ppDelete(t, r, sch, uploadedServed).Diagnostics.HasError())
		assert.False(t, ppDelete(t, r, sch, "").Diagnostics.HasError(), "with the user gone there is nothing to protect")
	})

	t.Run("a failed check is an error and removes nothing", func(t *testing.T) {
		s, c := newPPServer(t)
		s.custom = uploaded
		s.getStatus = 404 // a bare 404 does not prove anything
		r, sch := ppResource(t, c)
		resp := ppDelete(t, r, sch, uploadedServed)
		require.True(t, resp.Diagnostics.HasError())
		assert.Zero(t, s.count("DELETE"))
	})

	t.Run("other failures are errors", func(t *testing.T) {
		s, c := newPPServer(t)
		s.custom = uploaded
		s.deleteStatus = 404 // a bare 404 does not prove anything
		r, sch := ppResource(t, c)
		assert.True(t, ppDelete(t, r, sch, uploadedServed).Diagnostics.HasError())
	})
}
