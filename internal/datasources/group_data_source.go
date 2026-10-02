package datasources

import (
	"context"
	"errors"
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
	_ datasource.DataSource              = &groupDataSource{}
	_ datasource.DataSourceWithConfigure = &groupDataSource{}
)

func init() { register(NewGroupDataSource) }

// NewGroupDataSource creates a new group data source.
func NewGroupDataSource() datasource.DataSource {
	return &groupDataSource{}
}

// groupDataSource is the data source implementation.
type groupDataSource struct {
	client *client.Client
}

// groupDataSourceModel describes the data source data model.
type groupDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	FriendlyName types.String `tfsdk:"friendly_name"`
	LdapID       types.String `tfsdk:"ldap_id"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

// Metadata returns the data source type name.
func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

// Schema defines the schema for the data source.
func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Retrieves information about a Pocket-ID group, by ID or by exact name.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID of the group (a UUID). Either id or name must be provided; when both are given they must name the same group.",
				Optional:    true,
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The unique name identifier of the group, matched exactly. Either id or name must be provided; when both are given they must name the same group.",
				Optional:    true,
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
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *groupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data groupDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Validate that either ID or name is provided
	if data.ID.IsNull() && data.Name.IsNull() {
		resp.Diagnostics.AddError(
			"Missing required argument",
			"Either 'id' or 'name' must be provided",
		)
		return
	}

	foundGroup := d.lookup(ctx, data, &resp.Diagnostics)
	if foundGroup == nil {
		return
	}

	tflog.Debug(ctx, "Found group", map[string]interface{}{
		"id":            foundGroup.ID,
		"name":          foundGroup.Name,
		"friendly_name": foundGroup.FriendlyName,
	})

	// Map response body to model
	data.ID = types.StringValue(foundGroup.ID)
	data.Name = types.StringValue(foundGroup.Name)
	data.FriendlyName = types.StringValue(foundGroup.FriendlyName)
	if foundGroup.LdapID != nil && *foundGroup.LdapID != "" {
		data.LdapID = types.StringValue(*foundGroup.LdapID)
	} else {
		data.LdapID = types.StringNull()
	}
	if foundGroup.CreatedAt != "" {
		data.CreatedAt = types.StringValue(foundGroup.CreatedAt)
	} else {
		data.CreatedAt = types.StringNull()
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// lookup finds the group the configuration names, or adds an error and
// returns nil.
//
// An ID is resolved with the single-object endpoint, never by scanning a list:
// a list is paginated, and a group beyond its first page would read as "not
// found". A name is resolved with the server's search, which can only return a
// superset of the exact match, followed by an exact comparison over every page.
// When both are set they must name the same group.
func (d *groupDataSource) lookup(ctx context.Context, data groupDataSourceModel, diags *diag.Diagnostics) *client.UserGroup {
	hasID, hasName := !data.ID.IsNull(), !data.Name.IsNull()

	var found *client.UserGroup
	if hasID {
		group, err := d.client.GetUserGroup(ctx, data.ID.ValueString())
		switch {
		case err == nil:
			found = group
		case errors.Is(err, client.ErrInvalidIdentifier):
			diags.AddError("Invalid group ID", err.Error())
			return nil
		case client.IsNotFound(err, client.ResourceUserGroup):
			diags.AddError("Group Not Found", fmt.Sprintf("No group found with id '%s'", data.ID.ValueString()))
			return nil
		default:
			diags.AddError("Unable to Read Group", err.Error())
			return nil
		}
		if hasName && found.Name != data.Name.ValueString() {
			diags.AddError(
				"Conflicting arguments",
				fmt.Sprintf("The group with id '%s' is not named '%s'. Give an id or a name, or make them name the same group.", data.ID.ValueString(), data.Name.ValueString()),
			)
			return nil
		}
		return found
	}

	groups, err := d.client.SearchUserGroups(ctx, data.Name.ValueString())
	if err != nil {
		diags.AddError("Unable to Read Groups", err.Error())
		return nil
	}
	for i := range groups {
		if groups[i].Name == data.Name.ValueString() {
			return &groups[i]
		}
	}
	diags.AddError("Group Not Found", fmt.Sprintf("No group found with name '%s'", data.Name.ValueString()))
	return nil
}
