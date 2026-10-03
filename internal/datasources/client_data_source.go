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
	_ datasource.DataSource              = &clientDataSource{}
	_ datasource.DataSourceWithConfigure = &clientDataSource{}
)

func init() { register(NewClientDataSource) }

// NewClientDataSource is a helper function to simplify the provider implementation.
func NewClientDataSource() datasource.DataSource {
	return &clientDataSource{}
}

// clientDataSource is the data source implementation.
type clientDataSource struct {
	client *client.Client
}

// Metadata returns the data source type name.
func (d *clientDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_client"
}

// Schema defines the schema for the data source.
func (d *clientDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description:         "Fetches an OIDC client from Pocket-ID.",
		MarkdownDescription: "Fetches an OIDC client from Pocket-ID by its ID.",
		Attributes: clientAttributes(schema.StringAttribute{
			Description:         "The ID of the OIDC client to fetch.",
			MarkdownDescription: "The ID of the OIDC client to fetch.",
			Required:            true,
		}),
	}
}

// Configure adds the provider configured client to the data source.
func (d *clientDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *clientDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Get current configuration
	var config clientModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading OIDC client data source", map[string]any{
		"id": config.ID.ValueString(),
	})

	clientResp, err := d.client.GetClient(ctx, config.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading OIDC client",
			"Could not read OIDC client ID "+config.ID.ValueString()+": "+err.Error(),
		)
		return
	}

	state := clientModelFromAPI(ctx, *clientResp)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
