package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/valuefree"
)

var (
	_ datasource.DataSource              = &logoPresetsDataSource{}
	_ datasource.DataSourceWithConfigure = &logoPresetsDataSource{}
)

func init() { register(NewLogoPresetsDataSource) }

// NewLogoPresetsDataSource creates the pocketid_logo_presets data source.
func NewLogoPresetsDataSource() datasource.DataSource {
	return &logoPresetsDataSource{}
}

// logoPresetsDataSource searches Pocket ID's icon library (Pocket ID 2.18.0
// and later).
type logoPresetsDataSource struct {
	client *client.Client
}

type logoPresetsDataSourceModel struct {
	Search  types.String           `tfsdk:"search"`
	Presets []logoPresetsItemModel `tfsdk:"presets"`
}

type logoPresetsItemModel struct {
	Name        types.String `tfsdk:"name"`
	Reference   types.String `tfsdk:"reference"`
	LogoURL     types.String `tfsdk:"logo_url"`
	DarkLogoURL types.String `tfsdk:"dark_logo_url"`
}

func (d *logoPresetsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_logo_presets"
}

func (d *logoPresetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	defer func() { resp.Schema = valuefree.DataSourceSchema(resp.Schema) }()
	resp.Schema = schema.Schema{
		Description: "Searches Pocket ID's icon library for OIDC client logos (Pocket ID 2.18.0 or later).",
		MarkdownDescription: "Searches Pocket ID's icon library for OIDC client logos, the same search the admin interface offers " +
			"when picking a client's logo (Pocket ID 2.18.0 or later). The library is set by Pocket ID's `ICON_LIBRARY_URL`, by " +
			"default the selfh.st icons (https://selfh.st/icons); with `ICON_LIBRARY_URL=disabled` the read fails.\n\n" +
			"Use it to find the `reference` for `preset` on `pocketid_client_logo`. Pocket ID ranks the matches (an exact name " +
			"or reference first, then names starting with the search, names containing it, and tags) and returns at most 30.",
		Attributes: map[string]schema.Attribute{
			"search": schema.StringAttribute{
				Description: "Text matched against icon names, references and tags, ignoring case, spaces and punctuation " +
					"(\"home assistant\" finds `home-assistant`). Omit it to list the first icons alphabetically.",
				Optional: true,
			},
			"presets": schema.ListNestedAttribute{
				Description: "The matching icons, best match first.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Description: "The icon's display name, such as `Home Assistant`.",
							Computed:    true,
						},
						"reference": schema.StringAttribute{
							Description: "The icon's reference, such as `home-assistant`: the value for `preset` on `pocketid_client_logo`.",
							Computed:    true,
						},
						"logo_url": schema.StringAttribute{
							Description: "URL of the icon's image (SVG when the library has one, otherwise PNG): the light logo.",
							Computed:    true,
						},
						"dark_logo_url": schema.StringAttribute{
							Description: "URL of the icon's white variant for dark backgrounds, used as the dark logo; null when the icon has none.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *logoPresetsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	d.client = c
}

func (d *logoPresetsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data logoPresetsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	search := data.Search.ValueString()
	if d.client.ContainsAPIKey(search) {
		resp.Diagnostics.AddAttributeError(path.Root("search"), "Search not sent",
			"The search contains the API key this provider authenticates with, so it was not sent.")
		return
	}
	presets, err := d.client.SearchLogoPresets(ctx, search)
	switch {
	case client.IsLogoPresetsDisabled(err):
		resp.Diagnostics.AddError("Icon library turned off", "Pocket ID's icon library is turned off (ICON_LIBRARY_URL=disabled).")
		return
	case client.IsLogoPresetsUnavailable(err):
		resp.Diagnostics.AddError("Icon library unavailable", "Pocket ID could not load its icon library's index ("+err.Error()+"); try again later.")
		return
	case client.IsMissingEndpoint(err):
		resp.Diagnostics.AddError("No icon library", "This Pocket ID has no icon library; it was added in Pocket ID 2.18.0.")
		return
	case err != nil:
		resp.Diagnostics.AddError("Unable to Search Logo Presets", err.Error())
		return
	}
	tflog.Debug(ctx, "Retrieved logo presets", map[string]any{"count": len(presets)})

	data.Presets = make([]logoPresetsItemModel, len(presets))
	for i, preset := range presets {
		data.Presets[i] = logoPresetsItemModel{
			Name:        types.StringValue(preset.Name),
			Reference:   types.StringValue(preset.Reference),
			LogoURL:     types.StringValue(preset.LogoURL),
			DarkLogoURL: types.StringPointerValue(preset.DarkLogoURL),
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
