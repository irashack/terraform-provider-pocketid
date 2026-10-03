package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

var (
	_ datasource.DataSource              = &apisDataSource{}
	_ datasource.DataSourceWithConfigure = &apisDataSource{}
)

func init() { register(NewAPIsDataSource) }

// NewAPIsDataSource returns the pocketid_apis data source.
func NewAPIsDataSource() datasource.DataSource {
	return &apisDataSource{}
}

type apisDataSource struct {
	client *client.Client
}

type apisDataSourceModel struct {
	APIs []apiDataSourceModel `tfsdk:"apis"`
}

func (d *apisDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_apis"
}

func (d *apisDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := apiDataSourceAttributes()
	attributes["id"] = schema.StringAttribute{
		Description: "The ID of the API.",
		Computed:    true,
	}
	attributes["resource"] = schema.StringAttribute{
		Description: "The resource identifier clients send to request tokens for the API.",
		Computed:    true,
	}
	resp.Schema = schema.Schema{
		Description: "Lists every Pocket ID API (protected resource) with its permissions, in creation order. Requires Pocket ID 2.14.0 or later.",
		Attributes: map[string]schema.Attribute{
			"apis": schema.ListNestedAttribute{
				Description: "Every API, oldest first. The list is complete: every page is read, and a list that changes while it is read is an error.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: attributes,
				},
			},
		},
	}
}

func (d *apisDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *apisDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	apis, err := d.client.ListAPIs(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list APIs", err.Error())
		return
	}
	model := apisDataSourceModel{APIs: make([]apiDataSourceModel, 0, len(apis))}
	for i := range apis {
		item, diags := apiDataSourceModelFromServer(&apis[i])
		resp.Diagnostics.Append(diags...)
		model.APIs = append(model.APIs, item)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
