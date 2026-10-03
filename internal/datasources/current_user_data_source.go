package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &currentUserDataSource{}
	_ datasource.DataSourceWithConfigure = &currentUserDataSource{}
)

func init() { register(NewCurrentUserDataSource) }

// NewCurrentUserDataSource creates the data source that reports the user the
// provider's API key belongs to.
func NewCurrentUserDataSource() datasource.DataSource {
	return &currentUserDataSource{}
}

type currentUserDataSource struct {
	client *client.Client
}

// Metadata returns the data source type name.
func (d *currentUserDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_current_user"
}

// Schema defines the schema for the data source: every attribute of
// pocketid_user, none of them an argument.
func (d *currentUserDataSource) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	var user datasource.SchemaResponse
	(&userDataSource{}).Schema(ctx, datasource.SchemaRequest{}, &user)

	attributes := make(map[string]schema.Attribute, len(user.Schema.Attributes))
	for name, attribute := range user.Schema.Attributes {
		attributes[name] = attribute
	}
	attributes["id"] = schema.StringAttribute{Description: "The ID of the user the API key belongs to.", Computed: true}
	attributes["username"] = schema.StringAttribute{Description: "The username of the user the API key belongs to.", Computed: true}
	attributes["email"] = schema.StringAttribute{Description: "The email address of the user the API key belongs to, or null if the user has none.", Computed: true}

	resp.Schema = schema.Schema{
		Description: "Reads the Pocket-ID user the provider's API key belongs to: the account whose admin rights the provider acts with. For example, a precondition can check that this user is still an administrator, or that it has passkeys (see `pocketid_user_passkeys`).",
		Attributes:  attributes,
	}
}

// Configure adds the provider configured client to the data source.
func (d *currentUserDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	d.client = c
}

// Read refreshes the Terraform state with the latest data.
func (d *currentUserDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	user, err := d.client.GetCurrentUser(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read the Current User", err.Error())
		return
	}
	model, diags := ugUserModelFromAPI(ctx, user)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userDataSourceModel(model))...)
}
