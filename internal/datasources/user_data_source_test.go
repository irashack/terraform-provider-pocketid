package datasources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
	"github.com/Trozz/terraform-provider-pocketid/internal/datasources"
)

// TestUserDataSource_Schema_EmailLookup verifies that email, like username, is
// an Optional+Computed lookup key rather than a read-only attribute.
func TestUserDataSource_Schema_EmailLookup(t *testing.T) {
	ctx := context.Background()
	ds := datasources.NewUserDataSource()

	resp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())

	emailAttr, ok := resp.Schema.Attributes["email"].(schema.StringAttribute)
	require.True(t, ok, "email should be a StringAttribute")
	assert.True(t, emailAttr.Optional, "email should be usable as a lookup key")
	assert.True(t, emailAttr.Computed, "email should also be populated on lookup by id/username")
}

func configureUserDataSource(t *testing.T, c *client.Client) datasource.DataSource {
	t.Helper()
	ds := datasources.NewUserDataSource()
	configurable := ds.(datasource.DataSourceWithConfigure)
	resp := &datasource.ConfigureResponse{}
	configurable.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return ds
}

func userDataSourceSchema(t *testing.T, ds datasource.DataSource) schema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	ds.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

// usersListServer serves GET /api/users with a fixed list, for exercising the
// username/email lookup paths that list-and-filter client-side.
func usersListServer(t *testing.T, users []client.User) *client.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/users" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.PaginatedResponse[client.User]{Data: users})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return c
}

func userDataSourceConfig(ctx context.Context, t *testing.T, sch schema.Schema, id, username, email string) tfsdk.Config {
	t.Helper()
	return tfsdk.Config{
		Schema: sch,
		Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":             stringOrNull(id),
			"username":       stringOrNull(username),
			"email":          stringOrNull(email),
			"first_name":     tftypes.NewValue(tftypes.String, nil),
			"last_name":      tftypes.NewValue(tftypes.String, nil),
			"display_name":   tftypes.NewValue(tftypes.String, nil),
			"email_verified": tftypes.NewValue(tftypes.Bool, nil),
			"is_admin":       tftypes.NewValue(tftypes.Bool, nil),
			"locale":         tftypes.NewValue(tftypes.String, nil),
			"disabled":       tftypes.NewValue(tftypes.Bool, nil),
			"ldap_id":        tftypes.NewValue(tftypes.String, nil),
			"groups":         tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, nil),
		}),
	}
}

func stringOrNull(v string) tftypes.Value {
	if v == "" {
		return tftypes.NewValue(tftypes.String, nil)
	}
	return tftypes.NewValue(tftypes.String, v)
}

func TestUserDataSource_Read_ByEmail(t *testing.T) {
	ctx := context.Background()
	c := usersListServer(t, []client.User{
		{ID: "user-1", Username: "alice", Email: "alice@example.com", DisplayName: "Alice"},
		{ID: "user-2", Username: "bob", Email: "bob@example.com", DisplayName: "Bob"},
	})
	ds := configureUserDataSource(t, c)
	sch := userDataSourceSchema(t, ds)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
	}
	ds.Read(ctx, datasource.ReadRequest{Config: userDataSourceConfig(ctx, t, sch, "", "", "bob@example.com")}, readResp)
	require.False(t, readResp.Diagnostics.HasError(), "%v", readResp.Diagnostics)

	var id, username string
	require.False(t, readResp.State.GetAttribute(ctx, path.Root("id"), &id).HasError())
	require.False(t, readResp.State.GetAttribute(ctx, path.Root("username"), &username).HasError())
	assert.Equal(t, "user-2", id)
	assert.Equal(t, "bob", username)
}

// TestUserDataSource_Read_ByEmail_UserOnPageTwo is the P2 regression test:
// ListUsers() alone only sees the first page (server default 20 items), so
// a user beyond it was silently unfindable. This uses a fake server that
// genuinely paginates (unlike usersListServer above) to prove the lookup
// now follows every page via ListAllUsers.
func TestUserDataSource_Read_ByEmail_UserOnPageTwo(t *testing.T) {
	ctx := context.Background()
	users := make([]client.User, 150)
	for i := range users {
		users[i] = client.User{
			ID:       fmt.Sprintf("user-%d", i),
			Username: fmt.Sprintf("user%d", i),
			Email:    fmt.Sprintf("user%d@example.com", i),
		}
	}
	// user149 is index 149, past both the server's default page size (20)
	// and ListAllUsers' own request size (100): it can only be found by
	// following pagination through at least a second page.
	c := paginatedUsersDataSourceServer(t, users)
	ds := configureUserDataSource(t, c)
	sch := userDataSourceSchema(t, ds)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
	}
	ds.Read(ctx, datasource.ReadRequest{Config: userDataSourceConfig(ctx, t, sch, "", "", "user149@example.com")}, readResp)
	require.False(t, readResp.Diagnostics.HasError(), "%v", readResp.Diagnostics)

	var id, username string
	require.False(t, readResp.State.GetAttribute(ctx, path.Root("id"), &id).HasError())
	require.False(t, readResp.State.GetAttribute(ctx, path.Root("username"), &username).HasError())
	assert.Equal(t, "user-149", id)
	assert.Equal(t, "user149", username)
}

func TestUserDataSource_Read_ByEmail_NotFound(t *testing.T) {
	ctx := context.Background()
	c := usersListServer(t, []client.User{
		{ID: "user-1", Username: "alice", Email: "alice@example.com"},
	})
	ds := configureUserDataSource(t, c)
	sch := userDataSourceSchema(t, ds)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
	}
	ds.Read(ctx, datasource.ReadRequest{Config: userDataSourceConfig(ctx, t, sch, "", "", "missing@example.com")}, readResp)
	assert.True(t, readResp.Diagnostics.HasError())
}

func TestUserDataSource_Read_NoLookupKey(t *testing.T) {
	ctx := context.Background()
	c := usersListServer(t, nil)
	ds := configureUserDataSource(t, c)
	sch := userDataSourceSchema(t, ds)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
	}
	ds.Read(ctx, datasource.ReadRequest{Config: userDataSourceConfig(ctx, t, sch, "", "", "")}, readResp)
	require.True(t, readResp.Diagnostics.HasError())
	assert.Contains(t, readResp.Diagnostics.Errors()[0].Summary(), "Missing Required Argument")
}

func TestUserDataSource_Read_ConflictingLookupKeys(t *testing.T) {
	ctx := context.Background()
	c := usersListServer(t, nil)
	ds := configureUserDataSource(t, c)
	sch := userDataSourceSchema(t, ds)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
	}
	ds.Read(ctx, datasource.ReadRequest{Config: userDataSourceConfig(ctx, t, sch, "", "alice", "alice@example.com")}, readResp)
	require.True(t, readResp.Diagnostics.HasError())
	assert.Contains(t, readResp.Diagnostics.Errors()[0].Summary(), "Conflicting Arguments")
}

// TestUserDataSource_ConfigValidators is the P3 fix: id/username/email
// exactly-one-of enforcement should happen at plan/validate time via
// ConfigValidators, not only inside Read.
func TestUserDataSource_ConfigValidators(t *testing.T) {
	ctx := context.Background()
	ds := datasources.NewUserDataSource()
	withValidators, ok := ds.(datasource.DataSourceWithConfigValidators)
	require.True(t, ok, "pocketid_user data source should implement DataSourceWithConfigValidators")

	sch := userDataSourceSchema(t, ds)
	validators := withValidators.ConfigValidators(ctx)
	require.NotEmpty(t, validators)

	testCases := []struct {
		name                string
		id, username, email string
		expectError         bool
	}{
		{name: "none", expectError: true},
		{name: "id_only", id: "user-1", expectError: false},
		{name: "username_only", username: "alice", expectError: false},
		{name: "email_only", email: "alice@example.com", expectError: false},
		{name: "id_and_username", id: "user-1", username: "alice", expectError: true},
		{name: "id_and_email", id: "user-1", email: "alice@example.com", expectError: true},
		{name: "all_three", id: "user-1", username: "alice", email: "alice@example.com", expectError: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := userDataSourceConfig(ctx, t, sch, tc.id, tc.username, tc.email)
			req := datasource.ValidateConfigRequest{Config: cfg}
			resp := &datasource.ValidateConfigResponse{}
			for _, v := range validators {
				v.ValidateDataSource(ctx, req, resp)
			}
			assert.Equal(t, tc.expectError, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		})
	}
}
