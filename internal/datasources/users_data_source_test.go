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

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

// Test Users Data Source
func TestUsersDataSource_Metadata(t *testing.T) {
	ctx := context.Background()
	ds := datasources.NewUsersDataSource()

	req := datasource.MetadataRequest{
		ProviderTypeName: "pocketid",
	}
	resp := &datasource.MetadataResponse{}

	ds.Metadata(ctx, req, resp)

	assert.Equal(t, "pocketid_users", resp.TypeName)
}

func TestUsersDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	ds := datasources.NewUsersDataSource()

	req := datasource.SchemaRequest{}
	resp := &datasource.SchemaResponse{}

	ds.Schema(ctx, req, resp)

	assert.False(t, resp.Diagnostics.HasError())
	assert.NotNil(t, resp.Schema)

	// Verify users attribute
	usersAttr, ok := resp.Schema.Attributes["users"]
	assert.True(t, ok)

	listAttr, ok := usersAttr.(schema.ListNestedAttribute)
	assert.True(t, ok, "users should be a ListNestedAttribute")

	// Verify nested attributes
	expectedNestedAttributes := []string{
		"id", "username", "email", "first_name", "last_name",
		"display_name", "email_verified",
		"is_admin", "locale", "disabled", "groups",
	}

	for _, attr := range expectedNestedAttributes {
		_, ok := listAttr.NestedObject.Attributes[attr]
		assert.True(t, ok, "Nested object should have %s attribute", attr)
	}
}

func TestUsersDataSource_Configure(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name          string
		providerData  interface{}
		expectError   bool
		errorContains string
	}{
		{
			name:         "valid_client",
			providerData: &client.Client{},
			expectError:  false,
		},
		{
			name:         "nil_provider_data",
			providerData: nil,
			expectError:  false,
		},
		{
			name:          "invalid_provider_data_type",
			providerData:  map[string]string{"invalid": "type"},
			expectError:   true,
			errorContains: "Expected *client.Client",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ds := datasources.NewUsersDataSource()

			configurable, ok := ds.(datasource.DataSourceWithConfigure)
			require.True(t, ok)

			req := datasource.ConfigureRequest{
				ProviderData: tc.providerData,
			}
			resp := &datasource.ConfigureResponse{}

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

// paginatedUsersDataSourceServer serves GET /api/users honoring
// pagination[page] and pagination[limit], actually splitting users across
// pages instead of returning everything on the first response. This is what
// makes a "user on page 2" test meaningful: a server that ignores paging (as
// usersListServer in user_data_source_test.go does) would pass even without
// following pagination.
func paginatedUsersDataSourceServer(t *testing.T, users []client.User) *client.Client {
	t.Helper()

	const defaultLimit = 20

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/users", r.URL.Path)

		page := 1
		limit := defaultLimit
		if v := r.URL.Query().Get("pagination[page]"); v != "" {
			_, _ = fmt.Sscanf(v, "%d", &page)
		}
		if v := r.URL.Query().Get("pagination[limit]"); v != "" {
			_, _ = fmt.Sscanf(v, "%d", &limit)
		}
		if limit <= 0 {
			limit = defaultLimit
		}

		totalPages := (len(users) + limit - 1) / limit
		if totalPages == 0 {
			totalPages = 1
		}

		start := (page - 1) * limit
		end := start + limit
		if start > len(users) {
			start = len(users)
		}
		if end > len(users) {
			end = len(users)
		}

		resp := client.PaginatedResponse[client.User]{
			Data: users[start:end],
			Pagination: client.PaginationInfo{
				TotalItems:   len(users),
				CurrentPage:  page,
				ItemsPerPage: limit,
				TotalPages:   totalPages,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return c
}

func configureUsersDataSource(t *testing.T, c *client.Client) datasource.DataSource {
	t.Helper()
	ds := datasources.NewUsersDataSource()
	configurable := ds.(datasource.DataSourceWithConfigure)
	resp := &datasource.ConfigureResponse{}
	configurable.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return ds
}

func usersDataSourceSchema(t *testing.T, ds datasource.DataSource) schema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	ds.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

// usersDataSourceUserModel mirrors every attribute of the users_data_source
// nested object, so reading the "users" list attribute back into it succeeds.
type usersDataSourceUserModel struct {
	ID            string            `tfsdk:"id"`
	Username      string            `tfsdk:"username"`
	Email         string            `tfsdk:"email"`
	FirstName     string            `tfsdk:"first_name"`
	LastName      string            `tfsdk:"last_name"`
	DisplayName   string            `tfsdk:"display_name"`
	EmailVerified bool              `tfsdk:"email_verified"`
	IsAdmin       bool              `tfsdk:"is_admin"`
	Locale        *string           `tfsdk:"locale"`
	Disabled      bool              `tfsdk:"disabled"`
	LdapID        *string           `tfsdk:"ldap_id"`
	Groups        []string          `tfsdk:"groups"`
	CustomClaims  map[string]string `tfsdk:"custom_claims"`
}

// TestUsersDataSource_Read_ListsUsersAcrossAllPages is the pocketid_users
// side of the P2 pagination finding: it used to call ListUsers() once and
// silently return only the first page. This proves a user on page 2 (past
// the server's default page size, and past ListAllUsers' own 100-per-page
// request) is present in the result.
func TestUsersDataSource_Read_ListsUsersAcrossAllPages(t *testing.T) {
	ctx := context.Background()
	users := make([]client.User, 150)
	for i := range users {
		users[i] = client.User{
			ID:       fmt.Sprintf("user-%d", i),
			Username: fmt.Sprintf("user%d", i),
			Email:    fmt.Sprintf("user%d@example.com", i),
		}
	}
	c := paginatedUsersDataSourceServer(t, users)

	ds := configureUsersDataSource(t, c)
	sch := usersDataSourceSchema(t, ds)

	readResp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)},
	}
	ds.Read(ctx, datasource.ReadRequest{}, readResp)
	require.False(t, readResp.Diagnostics.HasError(), "%v", readResp.Diagnostics)

	var usersState []usersDataSourceUserModel
	require.False(t, readResp.State.GetAttribute(ctx, path.Root("users"), &usersState).HasError())

	assert.Len(t, usersState, 150)

	found := false
	for _, u := range usersState {
		if u.Username == "user149" {
			found = true
			assert.Equal(t, "user-149", u.ID)
			break
		}
	}
	assert.True(t, found, "user149 (page 2) must be present in the pocketid_users result")
}
