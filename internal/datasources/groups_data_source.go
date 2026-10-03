package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &groupsDataSource{}
	_ datasource.DataSourceWithConfigure = &groupsDataSource{}
)

func init() { register(NewGroupsDataSource) }

// NewGroupsDataSource creates a new groups data source.
func NewGroupsDataSource() datasource.DataSource {
	return &groupsDataSource{}
}

// groupsDataSource is the data source implementation.
type groupsDataSource struct {
	client *client.Client
}

// groupsDataSourceModel describes the data source data model.
type groupsDataSourceModel struct {
	Groups []groupModel `tfsdk:"groups"`
}

// groupModel describes the group data model.
type groupModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	FriendlyName     types.String `tfsdk:"friendly_name"`
	LdapID           types.String `tfsdk:"ldap_id"`
	CreatedAt        types.String `tfsdk:"created_at"`
	CustomClaims     types.Map    `tfsdk:"custom_claims"`
	MemberIDs        types.Set    `tfsdk:"member_ids"`
	AllowedClientIDs types.Set    `tfsdk:"allowed_client_ids"`
	UserCount        types.Int64  `tfsdk:"user_count"`
}

// Metadata returns the data source type name.
func (d *groupsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_groups"
}

// Schema defines the schema for the data source.
func (d *groupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Retrieves information about all Pocket-ID groups. Each group's members are built from one pass over the user list and its allowed clients from one pass over the client list, so the cost grows with the number of users and clients, not with one request per group (except against Pocket ID 2.14, whose client list does not carry groups: there the allowed clients cost one request per group). " +
			"The groups, the users and the clients are read in separate passes, which together are not an atomic snapshot: a change made while the data source is being read can show in one collection and not in another. " +
			"`user_count` is counted from the same member set as `member_ids`, so the two always agree with each other, though not necessarily with the server at any single moment.",

		Attributes: map[string]schema.Attribute{
			"groups": schema.ListNestedAttribute{
				Description: "List of all groups.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The ID of the group.",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The unique name identifier of the group.",
							Computed:    true,
						},
						"friendly_name": schema.StringAttribute{
							Description: "The friendly display name of the group.",
							Computed:    true,
						},
						"ldap_id": schema.StringAttribute{
							Description: "The LDAP identifier of the group, if it is synced from LDAP. Null for groups not managed by LDAP.",
							Computed:    true,
						},
						"created_at": schema.StringAttribute{
							Description: "The creation time of the group in RFC3339 format.",
							Computed:    true,
						},
						"custom_claims": schema.MapAttribute{
							Description: "The group's custom claims, by claim key. Empty when the group has none.",
							Computed:    true,
							ElementType: types.StringType,
						},
						"member_ids": schema.SetAttribute{
							Description: "The IDs of the users in the group, from the pass over the user list. Empty when the group has no members.",
							Computed:    true,
							ElementType: types.StringType,
						},
						"allowed_client_ids": schema.SetAttribute{
							Description: "The IDs of the OIDC clients that list this group as allowed. Empty when there are none. A client that is not group-restricted admits every user, whatever this lists.",
							Computed:    true,
							ElementType: types.StringType,
						},
						"user_count": schema.Int64Attribute{
							Description: "The number of users in the group: the size of `member_ids`, counted from the same deduplicated set (not the count the group list reports, which comes from a different request).",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *groupsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *groupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data groupsDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Get all groups
	groupsResp, err := d.client.ListUserGroups(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Read Groups",
			err.Error(),
		)
		return
	}

	tflog.Debug(ctx, "Retrieved groups", map[string]interface{}{
		"count": len(groupsResp),
	})

	members, err := d.client.GroupMemberIDs(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Group Members", err.Error())
		return
	}
	allowedClients, ok, err := d.client.AllowedClientIDsByGroup(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Group Clients", err.Error())
		return
	}

	// Map response body to model. A group's members are counted from the set
	// that is reported, never from the group list's own userCount: that comes
	// from an earlier request, and the two could describe different memberships.
	data.Groups = make([]groupModel, len(groupsResp))
	for i, group := range groupsResp {
		gm := groupModel{
			ID:           types.StringValue(group.ID),
			Name:         types.StringValue(group.Name),
			FriendlyName: types.StringValue(group.FriendlyName),
			LdapID:       types.StringNull(),
			CreatedAt:    types.StringNull(),
		}
		if group.LdapID != nil && *group.LdapID != "" {
			gm.LdapID = types.StringValue(*group.LdapID)
		}
		if group.CreatedAt != "" {
			gm.CreatedAt = types.StringValue(group.CreatedAt)
		}

		clientIDs := allowedClients[group.ID]
		if !ok {
			// Pocket ID 2.14: the client list has no groups.
			detail, err := d.client.GetUserGroupDetail(ctx, group.ID)
			if err != nil {
				resp.Diagnostics.AddError("Unable to Read Group", fmt.Sprintf("Could not read group %s: %s", group.ID, err))
				return
			}
			clientIDs = detail.AllowedClientIDs
		}

		var diags diag.Diagnostics
		gm.CustomClaims, diags = ugClaimsMapValue(ctx, group.CustomClaims)
		resp.Diagnostics.Append(diags...)
		memberIDs := ugUniqueIDs(members[group.ID])
		gm.MemberIDs, diags = ugIDSetValue(ctx, memberIDs)
		resp.Diagnostics.Append(diags...)
		gm.UserCount = types.Int64Value(int64(len(memberIDs)))
		gm.AllowedClientIDs, diags = ugIDSetValue(ctx, clientIDs)
		resp.Diagnostics.Append(diags...)
		data.Groups[i] = gm
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
