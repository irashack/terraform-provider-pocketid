package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const fixedUserID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"

// A configured id that differs from the existing user's is a plan-time error;
// the same id, no id, or a new user is not.
func TestUserIDUnchangeable(t *testing.T) {
	ctx := context.Background()
	existing := tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})}
	none := tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, nil)}
	other := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	for _, tc := range []struct {
		name          string
		state         tfsdk.State
		stateValue    types.String
		config        types.String
		wantError     bool
		wantPlanValue types.String
	}{
		{"changed", existing, types.StringValue(fixedUserID), types.StringValue(other), true, types.StringValue(other)},
		{"same", existing, types.StringValue(fixedUserID), types.StringValue(fixedUserID), false, types.StringValue(fixedUserID)},
		{"unset", existing, types.StringValue(fixedUserID), types.StringNull(), false, types.StringValue(fixedUserID)},
		{"unknown", existing, types.StringValue(fixedUserID), types.StringUnknown(), false, types.StringUnknown()},
		{"create", none, types.StringNull(), types.StringValue(other), false, types.StringValue(other)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planned := tc.config
			if tc.config.IsNull() {
				planned = tc.stateValue // as UseStateForUnknown leaves it
			}
			req := planmodifier.StringRequest{Path: path.Root("id"), State: tc.state, StateValue: tc.stateValue, ConfigValue: tc.config, PlanValue: planned}
			resp := planmodifier.StringResponse{PlanValue: planned}
			userIDUnchangeable{}.PlanModifyString(ctx, req, &resp)
			require.Equal(t, tc.wantError, resp.Diagnostics.HasError())
			require.True(t, resp.PlanValue.Equal(tc.wantPlanValue))
		})
	}
}

// fixedIDServer answers version, user GET, create, groups and claims for a
// user created with fixedUserID.
type fixedIDServer struct {
	mu           sync.Mutex
	version      string
	existing     bool
	createStatus int
	createdID    string
	readStatus   int
	// afterPostExisting and afterPostReadStatus replace existing and
	// readStatus once the create has been received (a create that
	// committed although its response failed, or a read that fails).
	afterPostExisting          bool
	afterPostReadStatus        int
	sentID                     any
	posts, gets, deletes, puts int
}

func (s *fixedIDServer) start(t *testing.T) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		user := func(id string) string {
			return `{"id":"` + id + `","username":"fixture","email":"fixture@example.invalid","userGroups":[],"customClaims":[]}`
		}
		switch {
		case r.URL.Path == "/api/version/current":
			_, _ = fmt.Fprintf(w, `{"currentVersion":%q}`, s.version)
		case r.Method == "GET" && r.URL.Path == "/api/users/"+fixedUserID:
			s.gets++
			switch {
			case s.readStatus != 0:
				w.WriteHeader(s.readStatus)
			case s.existing:
				_, _ = fmt.Fprint(w, user(fixedUserID))
			default:
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"error":"User not found","code":"user_not_found"}`)
			}
		case r.Method == "POST" && r.URL.Path == "/api/users":
			s.posts++
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			s.sentID = body["id"]
			s.existing, s.readStatus = s.afterPostExisting, s.afterPostReadStatus
			if s.createStatus != 0 {
				w.WriteHeader(s.createStatus)
				return
			}
			id := s.createdID
			if id == "" {
				id = fixedUserID
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, user(id))
		case r.Method == "PUT":
			s.puts++
			_, _ = fmt.Fprint(w, `[]`)
		case r.Method == "DELETE":
			s.deletes++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c
}

func runFixedIDCreate(t *testing.T, s *fixedIDServer, id types.String) resource.CreateResponse {
	t.Helper()
	ctx := context.Background()
	r := &userResource{client: s.start(t)}
	sr := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	model := defaultsPlanModel(types.SetNull(types.StringType), types.MapNull(types.StringType))
	model.ID = id
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	return resp
}

func TestUserCreateWithFixedID(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		s := &fixedIDServer{version: "2.12.0"}
		resp := runFixedIDCreate(t, s, types.StringValue(fixedUserID))
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		require.Equal(t, fixedUserID, s.sentID)
		var state userResourceModel
		require.False(t, resp.State.Get(context.Background(), &state).HasError())
		require.Equal(t, fixedUserID, state.ID.ValueString())
	})
	t.Run("generated_id_not_sent", func(t *testing.T) {
		s := &fixedIDServer{version: "2.11.0"}
		resp := runFixedIDCreate(t, s, types.StringUnknown())
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		require.Nil(t, s.sentID, "no id key is sent")
	})
	t.Run("server_too_old", func(t *testing.T) {
		s := &fixedIDServer{version: "2.11.0"}
		resp := runFixedIDCreate(t, s, types.StringValue(fixedUserID))
		require.True(t, resp.Diagnostics.HasError())
		require.Zero(t, s.posts, "refused before any mutation")
	})
	t.Run("already_exists", func(t *testing.T) {
		s := &fixedIDServer{version: "2.17.0", existing: true}
		resp := runFixedIDCreate(t, s, types.StringValue(fixedUserID))
		require.True(t, resp.Diagnostics.HasError())
		require.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "import it")
		require.Zero(t, s.posts)
		require.True(t, resp.State.Raw.IsNull())
	})
	t.Run("absence_unconfirmed", func(t *testing.T) {
		s := &fixedIDServer{version: "2.17.0", readStatus: http.StatusForbidden}
		resp := runFixedIDCreate(t, s, types.StringValue(fixedUserID))
		require.True(t, resp.Diagnostics.HasError())
		require.Zero(t, s.posts)
	})
	t.Run("server_ignored_id_rolled_back", func(t *testing.T) {
		s := &fixedIDServer{version: "2.17.0", createdID: "ffffffff-ffff-4fff-8fff-ffffffffffff"}
		resp := runFixedIDCreate(t, s, types.StringValue(fixedUserID))
		require.True(t, resp.Diagnostics.HasError())
		require.Equal(t, 1, s.deletes, "the user created under another ID is deleted")
		require.True(t, resp.State.Raw.IsNull())
	})
}
