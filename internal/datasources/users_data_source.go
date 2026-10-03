package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &usersDataSource{}
	_ datasource.DataSourceWithConfigure = &usersDataSource{}
)

func init() { register(NewUsersDataSource) }

// NewUsersDataSource is a helper function to simplify the provider implementation.
func NewUsersDataSource() datasource.DataSource {
	return &usersDataSource{}
}

// usersDataSource is the data source implementation.
type usersDataSource struct {
	client *client.Client
}

// usersDataSourceModel maps the data source schema data.
type usersDataSourceModel struct {
	Users []userModel `tfsdk:"users"`
}

// userModel represents a single user in the list
type userModel struct {
	ID            types.String `tfsdk:"id"`
	Username      types.String `tfsdk:"username"`
	Email         types.String `tfsdk:"email"`
	FirstName     types.String `tfsdk:"first_name"`
	LastName      types.String `tfsdk:"last_name"`
	DisplayName   types.String `tfsdk:"display_name"`
	EmailVerified types.Bool   `tfsdk:"email_verified"`
	IsAdmin       types.Bool   `tfsdk:"is_admin"`
	Locale        types.String `tfsdk:"locale"`
	Disabled      types.Bool   `tfsdk:"disabled"`
	LdapID        types.String `tfsdk:"ldap_id"`
	Groups        types.Set    `tfsdk:"groups"`
	CustomClaims  types.Map    `tfsdk:"custom_claims"`
}

// Metadata returns the data source type name.
func (d *usersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_users"
}

// Schema defines the schema for the data source.
func (d *usersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description:         "Fetches all users from Pocket-ID.",
		MarkdownDescription: "Fetches all users from Pocket-ID.",
		Attributes: map[string]schema.Attribute{
			"users": schema.ListNestedAttribute{
				Description: "List of all users.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The ID of the user.",
							Computed:    true,
						},
						"username": schema.StringAttribute{
							Description: "The username of the user.",
							Computed:    true,
						},
						"email": schema.StringAttribute{
							Description: "The email address of the user. Null for a user without one.",
							Computed:    true,
						},
						"first_name": schema.StringAttribute{
							Description: "The first name of the user.",
							Computed:    true,
						},
						"last_name": schema.StringAttribute{
							Description: "The last name of the user.",
							Computed:    true,
						},
						"display_name": schema.StringAttribute{
							Description: "The display name of the user.",
							Computed:    true,
						},
						"email_verified": schema.BoolAttribute{
							Description: "Whether the user's email address is verified.",
							Computed:    true,
						},
						"is_admin": schema.BoolAttribute{
							Description: "Whether the user has administrator privileges.",
							Computed:    true,
						},
						"locale": schema.StringAttribute{
							Description: "The locale preference for the user.",
							Computed:    true,
						},
						"disabled": schema.BoolAttribute{
							Description: "Whether the user account is disabled.",
							Computed:    true,
						},
						"ldap_id": schema.StringAttribute{
							Description: "The LDAP identifier of the user, if it is synced from LDAP. Null for users not managed by LDAP.",
							Computed:    true,
						},
						"groups": schema.SetAttribute{
							Description: "List of group IDs the user belongs to.",
							Computed:    true,
							ElementType: types.StringType,
						},
						"custom_claims": schema.MapAttribute{
							Description: ugUserClaimsDescription,
							Computed:    true,
							ElementType: types.StringType,
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *usersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

// Read refreshes the Terraform state with the latest data.
func (d *usersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	tflog.Debug(ctx, "Reading users data source")

	// Get every user from the API. ListUsers alone would silently return only
	// the first page (server default 20 items); this lists the whole set.
	users, err := d.client.ListAllUsers(ctx, "")
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading users",
			"Could not read users: "+err.Error(),
		)
		return
	}

	// Map response to model
	state := usersDataSourceModel{
		Users: make([]userModel, 0, len(users)),
	}

	// Convert each user
	for i := range users {
		userState, diags := ugUserModelFromAPI(ctx, &users[i])
		resp.Diagnostics.Append(diags...)
		state.Users = append(state.Users, userState)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Found users", map[string]any{
		"count": len(state.Users),
	})

	// Set state
	diags := resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}
