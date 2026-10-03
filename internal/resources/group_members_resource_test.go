package resources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/resources"
)

func gmUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// gmServer is a fake Pocket ID that holds one group and a set of existing
// users. PUT /api/user-groups/{id}/users keeps only the requested IDs that
// name an existing user, as the real server does.
type gmServer struct {
	t       *testing.T
	mu      sync.Mutex
	groupID string
	exists  bool // the group exists
	users   map[string]bool
	members []string
	log     []string // "METHOD path"
	puts    [][]string
	// putStatus, when not zero, answers every PUT with it and changes nothing.
	putStatus int
	// putBody, when not nil, replaces the PUT response body.
	putBody any
	// putAppliesThenFails applies every PUT and then answers with this status
	// instead of the group.
	putAppliesThenFails int
	// putInterrupted applies every PUT and then breaks the connection in the
	// middle of the response body.
	putInterrupted bool
	// putHeld answers every PUT with 502 without applying it and remembers
	// what it asked for; releasePut then applies it, the way a request that a
	// proxy gave up on is committed late.
	putHeld        bool
	pendingMembers []string
	// failGetsAfterPut answers every GET of the group with 403 once a PUT was
	// received.
	failGetsAfterPut bool
	// getStatus / getBody, when set, answer every GET of the group.
	getStatus int
	getBody   any
}

func newGMServer(t *testing.T, members ...string) (*gmServer, *client.Client) {
	t.Helper()
	s := &gmServer{t: t, groupID: gmUUID(1), exists: true, users: map[string]bool{}, members: members}
	for i := 100; i < 130; i++ {
		s.users[gmUUID(i)] = true
	}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return s, c
}

func (s *gmServer) groupJSON() map[string]any {
	users := []any{}
	for _, id := range s.members {
		users = append(users, map[string]any{"id": id})
	}
	return map[string]any{
		"id": s.groupID, "name": "g", "friendlyName": "G", "createdAt": "2026-01-01T00:00:00Z",
		"customClaims": []any{}, "users": users, "allowedOidcClients": []any{}, "ldapId": nil,
	}
}

func gmReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *gmServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, r.Method+" "+r.URL.Path)
	base := "/api/user-groups/" + s.groupID
	switch {
	case r.Method == http.MethodGet && r.URL.Path == base:
		if s.getStatus != 0 {
			gmReply(w, s.getStatus, s.getBody)
			return
		}
		if s.failGetsAfterPut && len(s.puts) > 0 {
			gmReply(w, 403, map[string]any{"error": "no"})
			return
		}
		if !s.exists {
			gmReply(w, 404, map[string]any{"error": "User group not found", "code": "not_found", "details": map[string]any{"resource": "User group"}})
			return
		}
		gmReply(w, 200, s.groupJSON())
	case r.Method == http.MethodPut && r.URL.Path == base+"/users":
		var body struct {
			UserIDs *[]string `json:"userIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.UserIDs == nil {
			gmReply(w, 400, map[string]any{"error": "bad", "code": "validation_failed"})
			return
		}
		s.puts = append(s.puts, append([]string(nil), (*body.UserIDs)...))
		if s.putStatus != 0 {
			gmReply(w, s.putStatus, map[string]any{"error": "no"})
			return
		}
		if !s.exists {
			gmReply(w, 404, map[string]any{"error": "User group not found", "code": "not_found", "details": map[string]any{"resource": "User group"}})
			return
		}
		var kept []string
		seen := map[string]bool{}
		for _, id := range *body.UserIDs {
			if s.users[id] && !seen[id] {
				kept = append(kept, id)
				seen[id] = true
			}
		}
		if s.putHeld {
			s.pendingMembers = kept
			gmReply(w, http.StatusBadGateway, map[string]any{"error": "bad gateway"})
			return
		}
		s.members = kept
		if s.putAppliesThenFails != 0 {
			gmReply(w, s.putAppliesThenFails, map[string]any{"error": "boom"})
			return
		}
		if s.putInterrupted {
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 4000\r\n\r\n{\"id\":\"")
				_ = buf.Flush()
				_ = conn.Close()
			}
			return
		}
		if s.putBody != nil {
			gmReply(w, 200, s.putBody)
			return
		}
		gmReply(w, 200, s.groupJSON())
	default:
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		gmReply(w, 404, map[string]any{"error": "API endpoint not found"})
	}
}

// releasePut applies the request putHeld kept back.
func (s *gmServer) releasePut() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members, s.putHeld = s.pendingMembers, false
}

// stopFailingGets lets reads succeed again after failGetsAfterPut.
func (s *gmServer) stopFailingGets() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failGetsAfterPut = false
}

func (s *gmServer) memberSet() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.members...)
	sort.Strings(out)
	return out
}

func (s *gmServer) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.puts)
}

func gmResource(t *testing.T, c *client.Client) (resource.Resource, schema.Schema) {
	t.Helper()
	r := resources.NewGroupMembersResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError())
	sch := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, sch)
	require.False(t, sch.Diagnostics.HasError())
	return r, sch.Schema
}

func gmObject(ctx context.Context, sch schema.Schema, id, groupID string, ids []string) tftypes.Value {
	return gmObjectUnresolved(ctx, sch, id, groupID, ids, nil)
}

// gmObjectUnresolved builds a resource value with unresolved_user_ids set to
// the given users; nil is null, an empty slice an empty set.
func gmObjectUnresolved(ctx context.Context, sch schema.Schema, id, groupID string, ids, unresolved []string) tftypes.Value {
	setOf := func(values []string) tftypes.Value {
		elements := make([]tftypes.Value, 0, len(values))
		for _, v := range values {
			elements = append(elements, tftypes.NewValue(tftypes.String, v))
		}
		return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elements)
	}
	idValue := tftypes.NewValue(tftypes.String, nil)
	if id != "" {
		idValue = tftypes.NewValue(tftypes.String, id)
	}
	unresolvedValue := tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, nil)
	if unresolved != nil {
		unresolvedValue = setOf(unresolved)
	}
	return tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
		"id":                  idValue,
		"group_id":            tftypes.NewValue(tftypes.String, groupID),
		"user_ids":            setOf(ids),
		"unresolved_user_ids": unresolvedValue,
	})
}

func gmCreate(t *testing.T, r resource.Resource, sch schema.Schema, groupID string, ids []string) *resource.CreateResponse {
	t.Helper()
	ctx := context.Background()
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: gmObject(ctx, sch, "", groupID, ids)}}, resp)
	return resp
}

func gmUpdate(t *testing.T, r resource.Resource, sch schema.Schema, groupID string, prior, want []string) *resource.UpdateResponse {
	t.Helper()
	ctx := context.Background()
	state := tfsdk.State{Schema: sch, Raw: gmObject(ctx, sch, groupID, groupID, prior)}
	resp := &resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: sch, Raw: gmObject(ctx, sch, groupID, groupID, want)},
		State: state,
	}, resp)
	return resp
}

func gmDelete(t *testing.T, r resource.Resource, sch schema.Schema, groupID string, managed []string) *resource.DeleteResponse {
	t.Helper()
	return gmDeleteUnresolved(t, r, sch, groupID, managed, nil)
}

func gmDeleteUnresolved(t *testing.T, r resource.Resource, sch schema.Schema, groupID string, managed, unresolved []string) *resource.DeleteResponse {
	t.Helper()
	ctx := context.Background()
	state := tfsdk.State{Schema: sch, Raw: gmObjectUnresolved(ctx, sch, groupID, groupID, managed, unresolved)}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(ctx, resource.DeleteRequest{State: state}, resp)
	return resp
}

func gmRead(t *testing.T, r resource.Resource, sch schema.Schema, groupID string, prior []string) *resource.ReadResponse {
	t.Helper()
	return gmReadUnresolved(t, r, sch, groupID, prior, nil)
}

func gmReadUnresolved(t *testing.T, r resource.Resource, sch schema.Schema, groupID string, prior, unresolved []string) *resource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	state := tfsdk.State{Schema: sch, Raw: gmObjectUnresolved(ctx, sch, groupID, groupID, prior, unresolved)}
	resp := &resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, resp)
	return resp
}

// gmStateUnresolved returns unresolved_user_ids of a state, and whether it is
// null.
func gmStateUnresolved(t *testing.T, state tfsdk.State) (ids []string, null bool) {
	t.Helper()
	var set types.Set
	require.False(t, state.GetAttribute(context.Background(), path.Root("unresolved_user_ids"), &set).HasError())
	if set.IsNull() {
		return nil, true
	}
	for _, e := range set.Elements() {
		ids = append(ids, strings.Trim(e.String(), `"`))
	}
	sort.Strings(ids)
	return ids, false
}

func gmStateIDs(t *testing.T, state tfsdk.State) []string {
	t.Helper()
	var set types.Set
	require.False(t, state.GetAttribute(context.Background(), path.Root("user_ids"), &set).HasError())
	var out []string
	for _, e := range set.Elements() {
		out = append(out, strings.Trim(e.String(), `"`))
	}
	sort.Strings(out)
	return out
}

func TestGroupMembersResource_SchemaAndMetadata(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupMembersResource()
	meta := &resource.MetadataResponse{}
	r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pocketid"}, meta)
	assert.Equal(t, "pocketid_group_members", meta.TypeName)

	_, sch := gmResource(t, nil)
	groupID, ok := sch.Attributes["group_id"].(schema.StringAttribute)
	require.True(t, ok)
	assert.True(t, groupID.Required)
	assert.NotEmpty(t, groupID.PlanModifiers, "a different group is a different resource")
	assert.NotEmpty(t, groupID.Validators)
	userIDs, ok := sch.Attributes["user_ids"].(schema.SetAttribute)
	require.True(t, ok)
	assert.True(t, userIDs.Required, "an empty set must be written out; null is not accepted")
	assert.NotEmpty(t, userIDs.Validators)
	assert.Contains(t, sch.MarkdownDescription, "pocketid_group_membership")
	assert.Contains(t, sch.MarkdownDescription, "pocketid_user")
	assert.Contains(t, sch.MarkdownDescription, "back-channel logout")
	// The requests whose outcome is unknown, and what clears the record of them.
	for _, claim := range []string{"unresolved_user_ids", "may yet take effect", "plans for the resource are refused", "destroy stops with an error", "terraform state rm"} {
		assert.Contains(t, sch.MarkdownDescription, claim)
	}
	unresolved, ok := sch.Attributes["unresolved_user_ids"].(schema.SetAttribute)
	require.True(t, ok)
	assert.True(t, unresolved.Computed)
	assert.False(t, unresolved.Optional || unresolved.Required)
	// The window between the provider's read and its write cannot be closed, and
	// the description says so for all three writes.
	for _, claim := range []string{"after the read and before the write", "cannot be protected", "create and update refuse", "destroy keeps"} {
		assert.Contains(t, sch.MarkdownDescription, claim)
	}
}

func TestGroupMembersResource_Create(t *testing.T) {
	s, c := newGMServer(t)
	r, sch := gmResource(t, c)

	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(102), gmUUID(101)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{gmUUID(101), gmUUID(102)}, s.memberSet())
	assert.Equal(t, [][]string{{gmUUID(101), gmUUID(102)}}, s.puts, "the full set is sent, sorted")
	assert.Equal(t, []string{gmUUID(101), gmUUID(102)}, gmStateIDs(t, resp.State))
	var id string
	require.False(t, resp.State.GetAttribute(context.Background(), path.Root("id"), &id).HasError())
	assert.Equal(t, s.groupID, id)
}

// An empty set is written as [] and empties a group that has no members the
// plan did not show.
func TestGroupMembersResource_Create_EmptySet(t *testing.T) {
	s, c := newGMServer(t)
	r, sch := gmResource(t, c)
	resp := gmCreate(t, r, sch, s.groupID, nil)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Zero(t, s.putCount(), "the group is already empty: nothing to write")
}

// Creating the resource for a group that already has other members must not
// remove them: a plan for a new resource cannot show that. Nothing is written.
func TestGroupMembersResource_Create_RefusesToRemoveUnshownMembers(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(103))
	r, sch := gmResource(t, c)

	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(102)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, "Group has members the plan did not show", resp.Diagnostics.Errors()[0].Summary())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), gmUUID(103))
	assert.Zero(t, s.putCount())
	assert.Equal(t, []string{gmUUID(101), gmUUID(103)}, s.memberSet(), "the group is untouched")
	assert.True(t, resp.State.Raw.IsNull(), "no state for a resource that was not created")
}

func TestGroupMembersResource_Create_AlreadyExactlyRight(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(102))
	r, sch := gmResource(t, c)
	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(102), gmUUID(101)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Zero(t, s.putCount(), "an identical membership is not rewritten")
}

// Pocket ID drops IDs that name no user and still answers 200. That is an
// error naming the ID. But the valid users of the same request were added, so
// the resource is kept, with the members the group actually holds: destroying
// it removes them, where a failed create with no state would leave them
// unmanaged for good.
func TestGroupMembersResource_Create_UnknownUserIsAnErrorButTheAddedMembersAreKept(t *testing.T) {
	s, c := newGMServer(t)
	r, sch := gmResource(t, c)

	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(999)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, "Group members differ from the request", resp.Diagnostics.Errors()[0].Summary())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), gmUUID(999))
	assert.NotContains(t, resp.Diagnostics.Errors()[0].Detail(), gmUUID(101))

	require.False(t, resp.State.Raw.IsNull(), "the group's membership changed, so the resource keeps its identity")
	assert.Equal(t, []string{gmUUID(101)}, gmStateIDs(t, resp.State), "the state holds what the group actually holds, not the plan")
	var id string
	require.False(t, resp.State.GetAttribute(context.Background(), path.Root("id"), &id).HasError())
	assert.Equal(t, s.groupID, id)
	assert.Equal(t, []string{gmUUID(101)}, s.memberSet())

	// Removing the failed configuration removes the member it added.
	deleted := &resource.DeleteResponse{State: resp.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: resp.State}, deleted)
	require.False(t, deleted.Diagnostics.HasError(), "%v", deleted.Diagnostics)
	assert.Empty(t, s.memberSet())
}

// A request whose valid part changed nothing leaves nothing to keep.
func TestGroupMembersResource_Create_NothingChangedMeansNoState(t *testing.T) {
	s, c := newGMServer(t)
	r, sch := gmResource(t, c)

	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(999)})
	require.True(t, resp.Diagnostics.HasError())
	assert.True(t, resp.State.Raw.IsNull(), "the group still holds what it held")
	assert.Empty(t, s.memberSet())
}

// The same on update: the state follows what the group holds after a partial
// result, not the stale prior members and not the plan.
func TestGroupMembersResource_Update_PartialResultKeepsTheActualMembers(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(102))
	r, sch := gmResource(t, c)

	resp := gmUpdate(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(102)}, []string{gmUUID(102), gmUUID(103), gmUUID(999)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), gmUUID(999))
	assert.Equal(t, []string{gmUUID(102), gmUUID(103)}, s.memberSet())
	assert.Equal(t, []string{gmUUID(102), gmUUID(103)}, gmStateIDs(t, resp.State))
}

// An update that was refused changes nothing and leaves the prior state alone.
func TestGroupMembersResource_Update_RefusedLeavesTheStateAlone(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101))
	s.putStatus = http.StatusForbidden
	r, sch := gmResource(t, c)

	resp := gmUpdate(t, r, sch, s.groupID, []string{gmUUID(101)}, []string{gmUUID(102)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{gmUUID(101)}, gmStateIDs(t, resp.State))
	assert.Equal(t, 1, s.putCount())
}

// The request failed after the change was made (a server error, a broken
// connection, an interrupted response body): the group is read, and what it
// holds is the truth.
func TestGroupMembersResource_FailureAfterTheChangeIsReconciledByReading(t *testing.T) {
	cases := map[string]func(s *gmServer){
		"server error after the change": func(s *gmServer) { s.putAppliesThenFails = http.StatusInternalServerError },
		"interrupted response body":     func(s *gmServer) { s.putInterrupted = true },
	}
	for name, configure := range cases {
		t.Run(name, func(t *testing.T) {
			s, c := newGMServer(t)
			configure(s)
			r, sch := gmResource(t, c)

			resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(102)})
			require.False(t, resp.Diagnostics.HasError(), "the group holds the requested members: %v", resp.Diagnostics)
			assert.Len(t, resp.Diagnostics.Warnings(), 1, "the failed request is reported")
			assert.Equal(t, []string{gmUUID(101), gmUUID(102)}, gmStateIDs(t, resp.State))
			assert.Equal(t, 1, s.putCount(), "never sent twice")
		})
	}
}

// The change may have been made but the group cannot be read afterwards either:
// the resource keeps its identity, with the members read before the request and
// the users the request named as candidates to clean up. Destroying it without
// a refresh in between must remove the user the request granted, which the
// members read before the request do not mention.
func TestGroupMembersResource_UnknownOutcomeKeepsTheIdentity(t *testing.T) {
	s, c := newGMServer(t)
	s.putAppliesThenFails = http.StatusInternalServerError
	s.failGetsAfterPut = true
	r, sch := gmResource(t, c)

	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, "Group members may have changed", resp.Diagnostics.Errors()[0].Summary())
	require.False(t, resp.State.Raw.IsNull(), "an unknown outcome keeps the resource so that it can be reconciled")
	assert.Empty(t, gmStateIDs(t, resp.State), "the members last read, before the request")
	candidates, null := gmStateUnresolved(t, resp.State)
	assert.False(t, null)
	assert.Equal(t, []string{gmUUID(101)}, candidates, "the requested users are recorded separately from the members")
	assert.Equal(t, 1, s.putCount())
	assert.Equal(t, []string{gmUUID(101)}, s.memberSet(), "the request did take effect")

	// While the group still cannot be read, destroy stops with an error and the
	// resource stays: it must not be forgotten on the strength of the empty
	// snapshot from before the write.
	failed := gmDeleteUnresolved(t, r, sch, s.groupID, nil, candidates)
	require.True(t, failed.Diagnostics.HasError())
	assert.False(t, failed.State.Raw.IsNull(), "the resource stays in state")
	assert.Equal(t, []string{gmUUID(101)}, s.memberSet())
	assert.Equal(t, 1, s.putCount(), "nothing was written")

	// Once it can be read, destroy, with no refresh in between, removes the user.
	s.stopFailingGets()
	deleted := gmDeleteUnresolved(t, r, sch, s.groupID, gmStateIDs(t, resp.State), candidates)
	require.False(t, deleted.Diagnostics.HasError(), "%v", deleted.Diagnostics)
	assert.Empty(t, s.memberSet(), "the user the unknown request granted is removed")
}

// A request that fails without proving it was refused, and after which the group
// is read and shows its old members, is still unsettled: a request that timed
// out upstream can be applied afterwards. The resource keeps its identity and
// records the requested users, and a destroy that comes after the late commit,
// with no refresh in between, removes them.
func TestGroupMembersResource_UnchangedGroupAfterAnUncertainFailureKeepsTheIdentity(t *testing.T) {
	cases := map[string]func(s *gmServer){
		"server error":         func(s *gmServer) { s.putStatus = http.StatusInternalServerError },
		"timeout status":       func(s *gmServer) { s.putStatus = http.StatusRequestTimeout },
		"held, committed late": func(s *gmServer) { s.putHeld = true },
	}
	for name, configure := range cases {
		t.Run(name, func(t *testing.T) {
			s, c := newGMServer(t)
			configure(s)
			r, sch := gmResource(t, c)

			resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(102)})
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, "Group members may have changed", resp.Diagnostics.Errors()[0].Summary())
			require.False(t, resp.State.Raw.IsNull(), "the re-read shows nothing, which does not settle it")
			assert.Empty(t, gmStateIDs(t, resp.State), "what the group held when it was read")
			candidates, null := gmStateUnresolved(t, resp.State)
			require.False(t, null)
			assert.Equal(t, []string{gmUUID(101), gmUUID(102)}, candidates)
			assert.Equal(t, 1, s.putCount(), "never sent twice")

			// The request commits late.
			s.mu.Lock()
			held := s.putHeld
			s.mu.Unlock()
			if held {
				s.releasePut()
			} else {
				// The server recovers, and the request it seemed to refuse turns
				// out to have been applied.
				s.mu.Lock()
				s.putStatus = 0
				s.members = []string{gmUUID(101), gmUUID(102)}
				s.mu.Unlock()
			}

			deleted := gmDeleteUnresolved(t, r, sch, s.groupID, gmStateIDs(t, resp.State), candidates)
			require.False(t, deleted.Diagnostics.HasError(), "%v", deleted.Diagnostics)
			assert.Empty(t, s.memberSet(), "the users the request granted late are removed")
		})
	}
}

// A request the server refused outright (a 4xx other than a timeout) proves it
// changed nothing: no state, no candidates.
func TestGroupMembersResource_DefiniteRejectionIsNotRecorded(t *testing.T) {
	s, c := newGMServer(t)
	s.putStatus = http.StatusForbidden
	r, sch := gmResource(t, c)

	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.True(t, resp.Diagnostics.HasError())
	assert.True(t, resp.State.Raw.IsNull())
	assert.Equal(t, 1, s.putCount())
}

// Update records the same: the members the group holds, the requested users as
// candidates, and the plan's user_ids is not what the state keeps.
func TestGroupMembersResource_Update_UncertainOutcomeRecordsCandidates(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101))
	s.putHeld = true
	r, sch := gmResource(t, c)

	resp := gmUpdate(t, r, sch, s.groupID, []string{gmUUID(101)}, []string{gmUUID(101), gmUUID(102)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{gmUUID(101)}, gmStateIDs(t, resp.State), "what the group holds, not the plan")
	candidates, null := gmStateUnresolved(t, resp.State)
	require.False(t, null)
	assert.Equal(t, []string{gmUUID(101), gmUUID(102)}, candidates)

	s.releasePut()
	deleted := gmDeleteUnresolved(t, r, sch, s.groupID, gmStateIDs(t, resp.State), candidates)
	require.False(t, deleted.Diagnostics.HasError(), "%v", deleted.Diagnostics)
	assert.Empty(t, s.memberSet())
}

// A change applied to an unresolved resource is refused: the members to protect
// are not in the plan.
func TestGroupMembersResource_Update_RefusesWhileUnresolved(t *testing.T) {
	s, c := newGMServer(t)
	r, sch := gmResource(t, c)
	ctx := context.Background()
	state := tfsdk.State{Schema: sch, Raw: gmObjectUnresolved(ctx, sch, s.groupID, s.groupID, nil, []string{gmUUID(101)})}
	resp := &resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: sch, Raw: gmObject(ctx, sch, s.groupID, s.groupID, []string{gmUUID(102)})},
		State: state,
	}, resp)
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, "Group members have an unresolved change", resp.Diagnostics.Errors()[0].Summary())
	assert.Empty(t, s.log, "no request was made")
}

// Reading a resource with candidates reads the group and resolves them: the
// members the group holds are recorded, the candidates cleared.
func TestGroupMembersResource_Read_ResolvesCandidates(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(103))
	r, sch := gmResource(t, c)

	resp := gmReadUnresolved(t, r, sch, s.groupID, nil, []string{gmUUID(101), gmUUID(102)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{gmUUID(101), gmUUID(103)}, gmStateIDs(t, resp.State), "what the group holds now")
	_, null := gmStateUnresolved(t, resp.State)
	assert.True(t, null, "the candidates are cleared")

	// Destroying from the state the read left removes the candidate that did
	// become a member, and the member the read recorded.
	deleted := &resource.DeleteResponse{State: resp.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: resp.State}, deleted)
	require.False(t, deleted.Diagnostics.HasError(), "%v", deleted.Diagnostics)
	assert.Empty(t, s.memberSet())
}

// A read that fails leaves the candidates in place; a group that no longer
// exists ends the resource.
func TestGroupMembersResource_Read_UnresolvedKeepsCandidatesUntilTheGroupIsRead(t *testing.T) {
	s, c := newGMServer(t)
	s.getStatus, s.getBody = http.StatusServiceUnavailable, map[string]any{"error": "down"}
	r, sch := gmResource(t, c)

	failed := gmReadUnresolved(t, r, sch, s.groupID, nil, []string{gmUUID(101)})
	require.True(t, failed.Diagnostics.HasError())
	candidates, null := gmStateUnresolved(t, failed.State)
	assert.False(t, null, "the candidates stay")
	assert.Equal(t, []string{gmUUID(101)}, candidates)

	s.mu.Lock()
	s.getStatus = 0
	s.exists = false
	s.mu.Unlock()
	gone := gmReadUnresolved(t, r, sch, s.groupID, nil, []string{gmUUID(101)})
	require.False(t, gone.Diagnostics.HasError(), "%v", gone.Diagnostics)
	assert.True(t, gone.State.Raw.IsNull(), "a group that does not exist has no members to clean up")
}

// A success response that does not list the users is read back with a GET.
func TestGroupMembersResource_Create_UnreadableAnswerIsReadBack(t *testing.T) {
	s, c := newGMServer(t)
	s.putBody = map[string]any{"id": s.groupID}
	r, sch := gmResource(t, c)

	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{gmUUID(101)}, gmStateIDs(t, resp.State))
	assert.Equal(t, 1, s.putCount(), "never sent twice")
}

// A failed write is never repeated.
func TestGroupMembersResource_Create_ServerErrorIsNotRetried(t *testing.T) {
	s, c := newGMServer(t)
	s.putStatus = http.StatusInternalServerError
	r, sch := gmResource(t, c)

	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, 1, s.putCount())
}

func TestGroupMembersResource_Update(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(102))
	r, sch := gmResource(t, c)

	resp := gmUpdate(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(102)}, []string{gmUUID(102), gmUUID(103)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{gmUUID(102), gmUUID(103)}, s.memberSet())
	assert.Equal(t, []string{gmUUID(102), gmUUID(103)}, gmStateIDs(t, resp.State))
}

// Someone who joined after the plan was made is not in the state and not in
// the plan: replacing the membership would remove them unseen.
func TestGroupMembersResource_Update_RefusesToRemoveMembersJoinedSincePlan(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(102), gmUUID(110))
	r, sch := gmResource(t, c)

	resp := gmUpdate(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(102)}, []string{gmUUID(101)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, "Group has members the plan did not show", resp.Diagnostics.Errors()[0].Summary())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), gmUUID(110))
	assert.Zero(t, s.putCount())
	assert.Equal(t, []string{gmUUID(101), gmUUID(102), gmUUID(110)}, s.memberSet())
}

func TestGroupMembersResource_Read_ReportsActualMembers(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(105))
	r, sch := gmResource(t, c)

	resp := gmRead(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(102)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{gmUUID(101), gmUUID(105)}, gmStateIDs(t, resp.State), "drift shows as a difference")
}

func TestGroupMembersResource_Read_GroupGoneLeavesState(t *testing.T) {
	s, c := newGMServer(t)
	s.exists = false
	r, sch := gmResource(t, c)

	resp := gmRead(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.True(t, resp.State.Raw.IsNull())
}

// Only Pocket ID's own "no such group" removes the resource from state.
func TestGroupMembersResource_Read_OtherFailuresKeepState(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   any
	}{
		"router 404":     {404, map[string]any{"error": "API endpoint not found"}},
		"another object": {404, map[string]any{"error": "x", "code": "not_found", "details": map[string]any{"resource": "OIDC client"}}},
		"forbidden":      {403, map[string]any{"error": "no"}},
	} {
		t.Run(name, func(t *testing.T) {
			s, c := newGMServer(t)
			s.getStatus, s.getBody = tc.status, tc.body
			r, sch := gmResource(t, c)

			resp := gmRead(t, r, sch, s.groupID, []string{gmUUID(101)})
			require.True(t, resp.Diagnostics.HasError())
			assert.False(t, resp.State.Raw.IsNull(), "the resource stays in state")
		})
	}
}

// Destroying removes the managed users and keeps whoever else joined.
func TestGroupMembersResource_Delete_RemovesOnlyManagedMembers(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(102), gmUUID(110))
	r, sch := gmResource(t, c)

	resp := gmDelete(t, r, sch, s.groupID, []string{gmUUID(101), gmUUID(102)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{gmUUID(110)}, s.memberSet())
}

func TestGroupMembersResource_Delete_NothingToRemove(t *testing.T) {
	s, c := newGMServer(t, gmUUID(110))
	r, sch := gmResource(t, c)

	resp := gmDelete(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Zero(t, s.putCount())
}

func TestGroupMembersResource_Delete_GroupAlreadyGone(t *testing.T) {
	s, c := newGMServer(t)
	s.exists = false
	r, sch := gmResource(t, c)

	resp := gmDelete(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
}

func TestGroupMembersResource_Delete_OtherFailureIsAnError(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101))
	s.getStatus, s.getBody = 404, map[string]any{"error": "API endpoint not found"}
	r, sch := gmResource(t, c)

	resp := gmDelete(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.True(t, resp.Diagnostics.HasError(), "a bare 404 does not prove the group is gone")
}

func TestGroupMembersResource_ImportState(t *testing.T) {
	ctx := context.Background()
	r, sch := gmResource(t, nil)
	imp := r.(resource.ResourceWithImportState)

	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
	imp.ImportState(ctx, resource.ImportStateRequest{ID: gmUUID(1)}, resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var id, groupID string
	require.False(t, resp.State.GetAttribute(ctx, path.Root("id"), &id).HasError())
	require.False(t, resp.State.GetAttribute(ctx, path.Root("group_id"), &groupID).HasError())
	assert.Equal(t, gmUUID(1), id)
	assert.Equal(t, gmUUID(1), groupID)

	bad := &resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
	imp.ImportState(ctx, resource.ImportStateRequest{ID: "not-a-uuid"}, bad)
	assert.True(t, bad.Diagnostics.HasError())
}

// Imported, the resource reads the group's members into user_ids.
func TestGroupMembersResource_ImportThenRead(t *testing.T) {
	s, c := newGMServer(t, gmUUID(101), gmUUID(102))
	r, sch := gmResource(t, c)
	ctx := context.Background()

	imported := &resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
	r.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: s.groupID}, imported)
	require.False(t, imported.Diagnostics.HasError())
	read := &resource.ReadResponse{State: imported.State}
	r.Read(ctx, resource.ReadRequest{State: imported.State}, read)
	require.False(t, read.Diagnostics.HasError(), "%v", read.Diagnostics)
	assert.Equal(t, []string{gmUUID(101), gmUUID(102)}, gmStateIDs(t, read.State))
}

// Every write holds the provider-wide lock for user-group relations, from its
// first read to its last check, so that it cannot interleave with another
// resource's read-modify-write of the same relations (a pocketid_user or
// pocketid_group_membership read of a user's groups, say, followed by a write
// of its stale list). While another writer holds the lock, no request is made.
func TestGroupMembersResource_WritesWaitForTheSharedMembershipLock(t *testing.T) {
	operations := map[string]func(r resource.Resource, sch schema.Schema, groupID string){
		"create": func(r resource.Resource, sch schema.Schema, groupID string) {
			gmCreate(t, r, sch, groupID, []string{gmUUID(101)})
		},
		"update": func(r resource.Resource, sch schema.Schema, groupID string) {
			gmUpdate(t, r, sch, groupID, nil, []string{gmUUID(101)})
		},
		"delete": func(r resource.Resource, sch schema.Schema, groupID string) {
			gmDelete(t, r, sch, groupID, []string{gmUUID(101)})
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			s, c := newGMServer(t, gmUUID(101))
			r, sch := gmResource(t, c)

			unlock := resources.LockMembershipWritesForTest()
			var once sync.Once
			release := func() { once.Do(unlock) }
			t.Cleanup(release) // a failed assertion must not leave the lock held for the next test
			done := make(chan struct{})
			go func() {
				defer close(done)
				operation(r, sch, s.groupID)
			}()

			select {
			case <-done:
				t.Fatal("the write finished while another writer held the lock")
			case <-time.After(150 * time.Millisecond):
			}
			s.mu.Lock()
			during := len(s.log)
			s.mu.Unlock()
			assert.Zero(t, during, "no request is made while the lock is held")

			release()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the write did not proceed once the lock was released")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			assert.NotEmpty(t, s.log)
		})
	}
}

// A group answer without its users is not an empty group: Create must not read
// it as one, pass its safety check and replace real members.
func TestGroupMembersResource_IncompleteGroupAnswerIsNeverReadAsEmpty(t *testing.T) {
	for name, body := range map[string]any{
		"only an id": map[string]any{"id": gmUUID(1)},
		"null":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			s, c := newGMServer(t, gmUUID(103))
			s.getStatus, s.getBody = 200, body
			r, sch := gmResource(t, c)

			create := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101)})
			require.True(t, create.Diagnostics.HasError())
			update := gmUpdate(t, r, sch, s.groupID, []string{gmUUID(103)}, []string{gmUUID(101)})
			require.True(t, update.Diagnostics.HasError())
			del := gmDelete(t, r, sch, s.groupID, []string{gmUUID(103)})
			require.True(t, del.Diagnostics.HasError())
			read := gmRead(t, r, sch, s.groupID, []string{gmUUID(103)})
			require.True(t, read.Diagnostics.HasError())
			assert.False(t, read.State.Raw.IsNull(), "a bad answer is not confirmation that the group is gone")

			assert.Zero(t, s.putCount(), "nothing is written on the strength of a snapshot that cannot be trusted")
			assert.Equal(t, []string{gmUUID(103)}, s.memberSet())
		})
	}
}
