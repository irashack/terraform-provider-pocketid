package resources_test

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
	"github.com/Trozz/terraform-provider-pocketid/internal/resources"
)

func TestNewGroupMembershipResource(t *testing.T) {
	r := resources.NewGroupMembershipResource()
	assert.NotNil(t, r)
}

func TestGroupMembershipResource_Metadata(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupMembershipResource()

	req := resource.MetadataRequest{ProviderTypeName: "pocketid"}
	resp := &resource.MetadataResponse{}
	r.Metadata(ctx, req, resp)

	assert.Equal(t, "pocketid_group_membership", resp.TypeName)
}

func TestGroupMembershipResource_SchemaValidation(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupMembershipResource()

	req := resource.SchemaRequest{}
	resp := &resource.SchemaResponse{}
	r.Schema(ctx, req, resp)

	assert.False(t, resp.Diagnostics.HasError())

	attrs := resp.Schema.Attributes

	groupIDAttr, ok := attrs["group_id"].(schema.StringAttribute)
	require.True(t, ok, "group_id should be StringAttribute")
	assert.True(t, groupIDAttr.Required, "group_id should be required")
	assert.NotEmpty(t, groupIDAttr.PlanModifiers, "group_id should require replacement")

	userIDAttr, ok := attrs["user_id"].(schema.StringAttribute)
	require.True(t, ok, "user_id should be StringAttribute")
	assert.True(t, userIDAttr.Required, "user_id should be required")
	assert.NotEmpty(t, userIDAttr.PlanModifiers, "user_id should require replacement")

	idAttr, ok := attrs["id"].(schema.StringAttribute)
	require.True(t, ok, "id should be StringAttribute")
	assert.True(t, idAttr.Computed, "id should be computed")
}

func TestGroupMembershipResource_Configure(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name          string
		providerData  interface{}
		expectError   bool
		errorContains string
	}{
		{name: "valid_client", providerData: &client.Client{}, expectError: false},
		{name: "nil_provider_data", providerData: nil, expectError: false},
		{name: "invalid_provider_data_type", providerData: "invalid", expectError: true, errorContains: "Expected *client.Client"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := resources.NewGroupMembershipResource()
			configurable, ok := r.(resource.ResourceWithConfigure)
			require.True(t, ok)

			req := resource.ConfigureRequest{ProviderData: tc.providerData}
			resp := &resource.ConfigureResponse{}
			configurable.Configure(ctx, req, resp)

			if tc.expectError {
				assert.True(t, resp.Diagnostics.HasError())
				assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), tc.errorContains)
			} else {
				assert.False(t, resp.Diagnostics.HasError())
			}
		})
	}
}

func TestGroupMembershipResource_Interfaces(t *testing.T) {
	r := resources.NewGroupMembershipResource()

	_, ok := r.(resource.ResourceWithConfigure)
	assert.True(t, ok)

	_, ok = r.(resource.ResourceWithImportState)
	assert.True(t, ok)
}

// groupMembershipTestServer mimics enough of the Pocket-ID API to exercise
// Create/Read/Delete: GetUser reflects the current in-memory group list, and
// PUT .../user-groups replaces it, recording each payload sent. Access to
// the shared state is mutex-guarded so concurrent requests (used by the
// concurrency tests below) don't race the fake server's own bookkeeping;
// each GET or PUT is still handled as an independent, unsynchronized
// round trip from the client's point of view, which is what lets an
// unserialized read-modify-write in the resource lose an update.
func groupMembershipTestServer(t *testing.T, userID string, initialGroups []string, userExists *bool) (*client.Client, *[]client.UpdateUserGroupsRequest) {
	t.Helper()

	var mu sync.Mutex
	current := append([]string(nil), initialGroups...)
	var puts []client.UpdateUserGroupsRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userExists != nil && !*userExists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error": "User not found"}`))
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/users/"+userID:
			mu.Lock()
			groups := make([]client.UserGroup, 0, len(current))
			for _, id := range current {
				groups = append(groups, client.UserGroup{ID: id})
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.User{ID: userID, UserGroups: groups})
		case r.Method == http.MethodPut && r.URL.Path == "/api/users/"+userID+"/user-groups":
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			puts = append(puts, req)
			current = append([]string(nil), req.UserGroupIDs...)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error": "API endpoint not found"}`))
		}
	}))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	return c, &puts
}

// missingEndpointServer always returns the exact 404 body Pocket-ID's API
// sends for a path it doesn't recognize, so client.HTTPError.MissingEndpoint
// is set - simulating a wrong base URL or a server too old to have an
// endpoint, as distinct from a confirmed-missing user or membership.
func missingEndpointServer(t *testing.T) *client.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": "API endpoint not found"}`))
	}))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	return c
}

func configureGroupMembership(t *testing.T, c *client.Client) resource.Resource {
	t.Helper()
	r, err := newConfiguredGroupMembership(c)
	require.NoError(t, err)
	return r
}

// newConfiguredGroupMembership configures a fresh resource instance without
// calling any *testing.T method, so it is safe to use from a goroutine other
// than the one running the test (t.FailNow-family calls, which require.*
// makes on failure, must only happen on the test's own goroutine).
func newConfiguredGroupMembership(c *client.Client) (resource.Resource, error) {
	r := resources.NewGroupMembershipResource()
	configurable := r.(resource.ResourceWithConfigure)
	resp := &resource.ConfigureResponse{}
	configurable.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	if resp.Diagnostics.HasError() {
		return nil, fmt.Errorf("configure: %v", resp.Diagnostics)
	}
	return r, nil
}

func groupMembershipSchema(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

func TestGroupMembershipResource_CreatePreservesOtherMembers(t *testing.T) {
	ctx := context.Background()
	userExists := true
	c, puts := groupMembershipTestServer(t, "user-1", []string{"group-existing"}, &userExists)
	r := configureGroupMembership(t, c)
	sch := groupMembershipSchema(t, r)

	plan := tfsdk.Plan{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":       tftypes.NewValue(tftypes.String, nil),
			"group_id": tftypes.NewValue(tftypes.String, "group-new"),
			"user_id":  tftypes.NewValue(tftypes.String, "user-1"),
		}),
	}

	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)
	require.False(t, createResp.Diagnostics.HasError(), "%v", createResp.Diagnostics)

	require.Len(t, *puts, 1)
	assert.ElementsMatch(t, []string{"group-existing", "group-new"}, (*puts)[0].UserGroupIDs)

	var id, groupID, userID string
	require.False(t, createResp.State.GetAttribute(ctx, path.Root("id"), &id).HasError())
	require.False(t, createResp.State.GetAttribute(ctx, path.Root("group_id"), &groupID).HasError())
	require.False(t, createResp.State.GetAttribute(ctx, path.Root("user_id"), &userID).HasError())
	assert.Equal(t, "group-new/user-1", id)
	assert.Equal(t, "group-new", groupID)
	assert.Equal(t, "user-1", userID)
}

func TestGroupMembershipResource_DeletePreservesOtherMembers(t *testing.T) {
	ctx := context.Background()
	userExists := true
	c, puts := groupMembershipTestServer(t, "user-1", []string{"group-keep", "group-remove"}, &userExists)
	r := configureGroupMembership(t, c)
	sch := groupMembershipSchema(t, r)

	state := tfsdk.State{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":       tftypes.NewValue(tftypes.String, "group-remove/user-1"),
			"group_id": tftypes.NewValue(tftypes.String, "group-remove"),
			"user_id":  tftypes.NewValue(tftypes.String, "user-1"),
		}),
	}

	deleteResp := &resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: state}, deleteResp)
	require.False(t, deleteResp.Diagnostics.HasError(), "%v", deleteResp.Diagnostics)

	require.Len(t, *puts, 1)
	assert.Equal(t, []string{"group-keep"}, (*puts)[0].UserGroupIDs)
}

func TestGroupMembershipResource_DeleteUserAlreadyGone(t *testing.T) {
	ctx := context.Background()
	userExists := false
	c, puts := groupMembershipTestServer(t, "user-1", nil, &userExists)
	r := configureGroupMembership(t, c)
	sch := groupMembershipSchema(t, r)

	state := tfsdk.State{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":       tftypes.NewValue(tftypes.String, "group-remove/user-1"),
			"group_id": tftypes.NewValue(tftypes.String, "group-remove"),
			"user_id":  tftypes.NewValue(tftypes.String, "user-1"),
		}),
	}

	deleteResp := &resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: state}, deleteResp)
	// A missing user is not an error: there is nothing left to remove.
	assert.False(t, deleteResp.Diagnostics.HasError(), "%v", deleteResp.Diagnostics)
	assert.Empty(t, *puts)
}

func TestGroupMembershipResource_ReadRemovesFromStateWhenMembershipGone(t *testing.T) {
	ctx := context.Background()
	userExists := true
	c, _ := groupMembershipTestServer(t, "user-1", []string{"group-other"}, &userExists)
	r := configureGroupMembership(t, c)
	sch := groupMembershipSchema(t, r)

	state := tfsdk.State{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":       tftypes.NewValue(tftypes.String, "group-gone/user-1"),
			"group_id": tftypes.NewValue(tftypes.String, "group-gone"),
			"user_id":  tftypes.NewValue(tftypes.String, "user-1"),
		}),
	}

	readResp := &resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, readResp)
	require.False(t, readResp.Diagnostics.HasError(), "%v", readResp.Diagnostics)
	assert.True(t, readResp.State.Raw.IsNull(), "state should be removed when the membership no longer exists")
}

func TestGroupMembershipResource_ReadRemovesFromStateWhenUserGone(t *testing.T) {
	ctx := context.Background()
	userExists := false
	c, _ := groupMembershipTestServer(t, "user-1", nil, &userExists)
	r := configureGroupMembership(t, c)
	sch := groupMembershipSchema(t, r)

	state := tfsdk.State{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":       tftypes.NewValue(tftypes.String, "group-1/user-1"),
			"group_id": tftypes.NewValue(tftypes.String, "group-1"),
			"user_id":  tftypes.NewValue(tftypes.String, "user-1"),
		}),
	}

	readResp := &resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, readResp)
	require.False(t, readResp.Diagnostics.HasError(), "%v", readResp.Diagnostics)
	assert.True(t, readResp.State.Raw.IsNull(), "state should be removed when the user no longer exists")
}

func TestGroupMembershipResource_Update_NotSupported(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupMembershipResource()
	resp := &resource.UpdateResponse{}
	r.Update(ctx, resource.UpdateRequest{}, resp)
	assert.True(t, resp.Diagnostics.HasError())
}

func TestGroupMembershipResource_ImportState(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupMembershipResource()
	sch := groupMembershipSchema(t, r)

	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
	}
	r.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: "group-1/user-1"}, importResp)
	require.False(t, importResp.Diagnostics.HasError(), "%v", importResp.Diagnostics)

	var groupID, userID, id string
	require.False(t, importResp.State.GetAttribute(ctx, path.Root("group_id"), &groupID).HasError())
	require.False(t, importResp.State.GetAttribute(ctx, path.Root("user_id"), &userID).HasError())
	require.False(t, importResp.State.GetAttribute(ctx, path.Root("id"), &id).HasError())
	assert.Equal(t, "group-1", groupID)
	assert.Equal(t, "user-1", userID)
	assert.Equal(t, "group-1/user-1", id)
}

func TestGroupMembershipResource_ImportState_InvalidFormat(t *testing.T) {
	ctx := context.Background()
	r := resources.NewGroupMembershipResource()
	sch := groupMembershipSchema(t, r)

	for _, id := range []string{"no-separator", "/missing-group", "missing-user/"} {
		t.Run(id, func(t *testing.T) {
			importResp := &resource.ImportStateResponse{
				State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
			}
			r.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: id}, importResp)
			assert.True(t, importResp.Diagnostics.HasError())
		})
	}
}

// groupMembershipCreatePlan builds a Create plan for a fresh (group_id,
// user_id) pair.
func groupMembershipCreatePlan(ctx context.Context, sch schema.Schema, groupID, userID string) tfsdk.Plan {
	return tfsdk.Plan{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":       tftypes.NewValue(tftypes.String, nil),
			"group_id": tftypes.NewValue(tftypes.String, groupID),
			"user_id":  tftypes.NewValue(tftypes.String, userID),
		}),
	}
}

// groupMembershipDeleteState builds the prior state Delete reads from for an
// existing (group_id, user_id) pair.
func groupMembershipDeleteState(ctx context.Context, sch schema.Schema, groupID, userID string) tfsdk.State {
	return tfsdk.State{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":       tftypes.NewValue(tftypes.String, groupID+"/"+userID),
			"group_id": tftypes.NewValue(tftypes.String, groupID),
			"user_id":  tftypes.NewValue(tftypes.String, userID),
		}),
	}
}

// TestGroupMembershipResource_Create_ConcurrentAddsSameUser reproduces the
// P1 finding: adding one user to several groups in a single apply dispatches
// Create concurrently (Terraform's default parallelism is 10). Without
// serializing the read-modify-write against the user's group list, later
// PUTs built from a stale snapshot silently drop earlier additions. Every
// one of the concurrently created memberships must survive.
func TestGroupMembershipResource_Create_ConcurrentAddsSameUser(t *testing.T) {
	ctx := context.Background()
	const n = 8
	userExists := true
	c, _ := groupMembershipTestServer(t, "user-1", nil, &userExists)
	sch := groupMembershipSchema(t, resources.NewGroupMembershipResource())

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			groupID := fmt.Sprintf("group-%d", i)
			r, err := newConfiguredGroupMembership(c)
			if err != nil {
				errs[i] = err
				return
			}
			createResp := &resource.CreateResponse{
				State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
			}
			r.Create(ctx, resource.CreateRequest{Plan: groupMembershipCreatePlan(ctx, sch, groupID, "user-1")}, createResp)
			if createResp.Diagnostics.HasError() {
				errs[i] = fmt.Errorf("create %s: %v", groupID, createResp.Diagnostics)
			}
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}

	final, err := c.GetUser("user-1")
	require.NoError(t, err)
	var gotGroups []string
	for _, g := range final.UserGroups {
		gotGroups = append(gotGroups, g.ID)
	}
	wantGroups := make([]string, n)
	for i := 0; i < n; i++ {
		wantGroups[i] = fmt.Sprintf("group-%d", i)
	}
	assert.ElementsMatch(t, wantGroups, gotGroups, "every concurrently created membership must survive")
}

// TestGroupMembershipResource_ConcurrentMixedAddsAndRemoves runs a mix of
// concurrent Create and Delete for the same user - the same race as above,
// in both directions at once.
func TestGroupMembershipResource_ConcurrentMixedAddsAndRemoves(t *testing.T) {
	ctx := context.Background()
	userExists := true
	initial := []string{"group-0", "group-1", "group-2", "group-3", "group-4"}
	c, _ := groupMembershipTestServer(t, "user-1", initial, &userExists)
	sch := groupMembershipSchema(t, resources.NewGroupMembershipResource())

	var wg sync.WaitGroup
	errs := make([]error, 10)

	// Concurrently remove group-0..4 and add group-5..9.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			groupID := fmt.Sprintf("group-%d", i)
			r, err := newConfiguredGroupMembership(c)
			if err != nil {
				errs[i] = err
				return
			}
			deleteResp := &resource.DeleteResponse{}
			r.Delete(ctx, resource.DeleteRequest{State: groupMembershipDeleteState(ctx, sch, groupID, "user-1")}, deleteResp)
			if deleteResp.Diagnostics.HasError() {
				errs[i] = fmt.Errorf("delete %s: %v", groupID, deleteResp.Diagnostics)
			}
		}(i)
	}
	for i := 5; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			groupID := fmt.Sprintf("group-%d", i)
			r, err := newConfiguredGroupMembership(c)
			if err != nil {
				errs[i] = err
				return
			}
			createResp := &resource.CreateResponse{
				State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
			}
			r.Create(ctx, resource.CreateRequest{Plan: groupMembershipCreatePlan(ctx, sch, groupID, "user-1")}, createResp)
			if createResp.Diagnostics.HasError() {
				errs[i] = fmt.Errorf("create %s: %v", groupID, createResp.Diagnostics)
			}
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}

	final, err := c.GetUser("user-1")
	require.NoError(t, err)
	var gotGroups []string
	for _, g := range final.UserGroups {
		gotGroups = append(gotGroups, g.ID)
	}
	assert.ElementsMatch(t, []string{"group-5", "group-6", "group-7", "group-8", "group-9"}, gotGroups)
}

// TestGroupMembershipResource_Read_ErrorsOnMissingEndpoint proves that Read
// reports an error, rather than dropping the resource from state, when the
// 404 means the API path itself doesn't exist (wrong base URL, or a server
// too old to have it) rather than a confirmed-missing user.
func TestGroupMembershipResource_Read_ErrorsOnMissingEndpoint(t *testing.T) {
	ctx := context.Background()
	c := missingEndpointServer(t)
	r := configureGroupMembership(t, c)
	sch := groupMembershipSchema(t, r)

	state := groupMembershipDeleteState(ctx, sch, "group-1", "user-1")
	readResp := &resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, readResp)

	assert.True(t, readResp.Diagnostics.HasError(), "a missing-endpoint 404 must surface as an error")
	assert.False(t, readResp.State.Raw.IsNull(), "state must not be silently dropped for a missing-endpoint 404")
}

// TestGroupMembershipResource_Delete_ErrorsOnMissingEndpoint is the Delete
// analogue of the Read test above.
func TestGroupMembershipResource_Delete_ErrorsOnMissingEndpoint(t *testing.T) {
	ctx := context.Background()
	c := missingEndpointServer(t)
	r := configureGroupMembership(t, c)
	sch := groupMembershipSchema(t, r)

	state := groupMembershipDeleteState(ctx, sch, "group-1", "user-1")
	deleteResp := &resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: state}, deleteResp)

	assert.True(t, deleteResp.Diagnostics.HasError(), "a missing-endpoint 404 must surface as an error, not silent success")
}
