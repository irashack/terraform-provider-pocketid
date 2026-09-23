package resources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
// PUT .../user-groups replaces it, recording each payload sent.
func groupMembershipTestServer(t *testing.T, userID string, initialGroups []string, userExists *bool) (*client.Client, *[]client.UpdateUserGroupsRequest) {
	t.Helper()

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
			groups := make([]client.UserGroup, 0, len(current))
			for _, id := range current {
				groups = append(groups, client.UserGroup{ID: id})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.User{ID: userID, UserGroups: groups})
		case r.Method == http.MethodPut && r.URL.Path == "/api/users/"+userID+"/user-groups":
			var req client.UpdateUserGroupsRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			puts = append(puts, req)
			current = append([]string(nil), req.UserGroupIDs...)
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

func configureGroupMembership(t *testing.T, c *client.Client) resource.Resource {
	t.Helper()
	r := resources.NewGroupMembershipResource()
	configurable := r.(resource.ResourceWithConfigure)
	resp := &resource.ConfigureResponse{}
	configurable.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return r
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
