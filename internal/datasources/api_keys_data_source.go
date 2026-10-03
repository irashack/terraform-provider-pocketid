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
	_ datasource.DataSource              = &apiKeysDataSource{}
	_ datasource.DataSourceWithConfigure = &apiKeysDataSource{}
)

func init() { register(NewAPIKeysDataSource) }

// NewAPIKeysDataSource creates a new API keys data source.
func NewAPIKeysDataSource() datasource.DataSource {
	return &apiKeysDataSource{}
}

// apiKeysDataSource is the data source implementation.
type apiKeysDataSource struct {
	client *client.Client
}

type apiKeysDataSourceModel struct {
	Keys []apiKeyItemModel `tfsdk:"keys"`
}

type apiKeyItemModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	ExpiresAt   types.String `tfsdk:"expires_at"`
	LastUsedAt  types.String `tfsdk:"last_used_at"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// Metadata returns the data source type name.
func (d *apiKeysDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_keys"
}

// Schema defines the schema for the data source.
func (d *apiKeysDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the API keys of the Pocket-ID user who owns the API key this provider uses.",
		MarkdownDescription: "Lists the API keys of the Pocket-ID user who owns the API key this provider authenticates with " +
			"(Pocket-ID's list shows only the caller's own keys), with their name, description and creation, expiry and " +
			"last-used times. A key's value is never returned: Pocket-ID shows it once, when it is created.\n\n" +
			"Its purpose is to warn before the provider's own key expires, with a `check` block (see the example). A key " +
			"that has expired can no longer authenticate, so the provider could not read this list afterwards: the " +
			"warning has to come earlier. The provider cannot tell which listed key it is using, so match the key by its " +
			"name. If the provider uses Pocket-ID's static API key (`STATIC_API_KEY`), that is not a stored key and the " +
			"list is empty.\n\n" +
			"This data source only reads. Creating and renewing a key needs a signed-in session and cannot be done with an API " +
			"key, and revoking one could delete the key the provider is running on, so neither is offered.",
		Attributes: map[string]schema.Attribute{
			"keys": schema.ListNestedAttribute{
				Description: "The API keys, oldest first.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The ID of the API key.",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The name of the API key.",
							Computed:    true,
						},
						"description": schema.StringAttribute{
							Description: "The description of the API key. Null when it has none.",
							Computed:    true,
						},
						"expires_at": schema.StringAttribute{
							Description: "When the API key expires, in RFC3339 format. Usable with `timecmp`.",
							Computed:    true,
						},
						"last_used_at": schema.StringAttribute{
							Description: "When the API key was last used, in RFC3339 format. Null when it never was.",
							Computed:    true,
						},
						"created_at": schema.StringAttribute{
							Description: "When the API key was created, in RFC3339 format.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *apiKeysDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// apiKeyOptionalString is null for a missing or empty value.
func apiKeyOptionalString(value *string) types.String {
	if value == nil || *value == "" {
		return types.StringNull()
	}
	return types.StringValue(*value)
}

// Read refreshes the Terraform state with the latest data.
func (d *apiKeysDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	keys, err := d.client.ListAPIKeys(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read API Keys", err.Error())
		return
	}
	tflog.Debug(ctx, "Retrieved API keys", map[string]any{"count": len(keys)})

	data := apiKeysDataSourceModel{Keys: make([]apiKeyItemModel, len(keys))}
	for i, key := range keys {
		data.Keys[i] = apiKeyItemModel{
			ID:          types.StringValue(key.ID),
			Name:        types.StringValue(key.Name),
			Description: apiKeyOptionalString(key.Description),
			ExpiresAt:   types.StringValue(key.ExpiresAt),
			LastUsedAt:  apiKeyOptionalString(key.LastUsedAt),
			CreatedAt:   types.StringValue(key.CreatedAt),
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
