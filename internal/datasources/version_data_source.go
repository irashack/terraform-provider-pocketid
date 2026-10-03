package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &versionDataSource{}
	_ datasource.DataSourceWithConfigure = &versionDataSource{}
)

func init() { register(NewVersionDataSource) }

// NewVersionDataSource creates the data source that reports the Pocket ID
// server's version.
func NewVersionDataSource() datasource.DataSource {
	return &versionDataSource{}
}

type versionDataSource struct {
	client *client.Client
}

type versionDataSourceModel struct {
	Version types.String `tfsdk:"version"`
}

// Metadata returns the data source type name.
func (d *versionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_version"
}

// Schema defines the schema for the data source.
func (d *versionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reports the version of the Pocket-ID server the provider is talking to, so a configuration can check it, for example in a precondition, before relying on a feature of a newer version.",
		Attributes: map[string]schema.Attribute{
			"version": schema.StringAttribute{
				Description: "The server's version as semantic version text without a leading `v`, for example `2.17.0`.",
				Computed:    true,
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *versionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *versionDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	version, err := d.client.GetCurrentVersion(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read the Server Version", err.Error())
		return
	}
	if version == "" {
		resp.Diagnostics.AddError(
			"Unable to Read the Server Version",
			"The server has no version endpoint, which means it is older than Pocket ID 2.3.0. This provider supports Pocket ID 2.14.0 and later.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &versionDataSourceModel{Version: types.StringValue(version)})...)
}
