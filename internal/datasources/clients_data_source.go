package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &clientsDataSource{}
	_ datasource.DataSourceWithConfigure = &clientsDataSource{}
)

func init() { register(NewClientsDataSource) }

// NewClientsDataSource is a helper function to simplify the provider implementation.
func NewClientsDataSource() datasource.DataSource {
	return &clientsDataSource{}
}

// clientsDataSource is the data source implementation.
type clientsDataSource struct {
	client *client.Client
}

// clientsDataSourceModel maps the data source schema data.
type clientsDataSourceModel struct {
	Clients []clientModel `tfsdk:"clients"`
}

// Metadata returns the data source type name.
func (d *clientsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_clients"
}

// Schema defines the schema for the data source.
func (d *clientsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description:         "Fetches all OIDC clients from Pocket-ID.",
		MarkdownDescription: "Fetches all OIDC clients from Pocket-ID.",
		Attributes: map[string]schema.Attribute{
			"clients": schema.ListNestedAttribute{
				Description: "List of all OIDC clients.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: clientAttributes(schema.StringAttribute{
						Description: "The ID of the OIDC client.",
						Computed:    true,
					}),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *clientsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *clientsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	tflog.Debug(ctx, "Reading OIDC clients data source")

	// Get clients from API
	clientsResp, err := d.client.ListClients(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading OIDC clients",
			"Could not read OIDC clients: "+err.Error(),
		)
		return
	}

	// Map response to model
	state := clientsDataSourceModel{
		Clients: make([]clientModel, 0, len(clientsResp)),
	}

	for _, clientResp := range clientsResp {
		state.Clients = append(state.Clients, clientModelFromAPI(ctx, clientResp))
	}

	tflog.Debug(ctx, "Found OIDC clients", map[string]any{
		"count": len(state.Clients),
	})

	// Set state
	diags := resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}
