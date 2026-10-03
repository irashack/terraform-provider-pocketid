package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/valuefree"
)

var (
	_ datasource.DataSource                     = &apiDataSource{}
	_ datasource.DataSourceWithConfigure        = &apiDataSource{}
	_ datasource.DataSourceWithConfigValidators = &apiDataSource{}
)

func init() { register(NewAPIDataSource) }

// NewAPIDataSource returns the pocketid_api data source.
func NewAPIDataSource() datasource.DataSource {
	return &apiDataSource{}
}

type apiDataSource struct {
	client *client.Client
}

type apiDataSourceModel struct {
	ID               types.String `tfsdk:"id"`
	Resource         types.String `tfsdk:"resource"`
	Name             types.String `tfsdk:"name"`
	CreatedAt        types.String `tfsdk:"created_at"`
	AllowCIMDClients types.Bool   `tfsdk:"allow_cimd_clients"`
	Permissions      types.Map    `tfsdk:"permissions"`
}

var apiDataSourcePermissionAttrTypes = map[string]attr.Type{
	"id":                       types.StringType,
	"name":                     types.StringType,
	"description":              types.StringType,
	"allowed_for_cimd_clients": types.BoolType,
}

// apiDataSourceAttributes are the attributes every API data source reports;
// id and resource are added by the caller, since only the single lookup takes
// them as input.
func apiDataSourceAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"name": schema.StringAttribute{
			Description: "The display name of the API.",
			Computed:    true,
		},
		"created_at": schema.StringAttribute{
			Description: "When the API was created, as reported by Pocket ID.",
			Computed:    true,
		},
		"allow_cimd_clients": schema.BoolAttribute{
			Description: "Whether clients registered through a Client ID Metadata Document may request user-delegated tokens " +
				"for the API, with the permissions that have `allowed_for_cimd_clients` set.",
			Computed: true,
		},
		"permissions": schema.MapNestedAttribute{
			Description: "The API's permissions, keyed by permission key (the scope a client requests).",
			Computed:    true,
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{
						Description: "The permission's ID.",
						Computed:    true,
					},
					"name": schema.StringAttribute{
						Description: "The permission's display name.",
						Computed:    true,
					},
					"description": schema.StringAttribute{
						Description: "The permission's description; null when it has none.",
						Computed:    true,
					},
					"allowed_for_cimd_clients": schema.BoolAttribute{
						Description: "Whether clients registered through a Client ID Metadata Document may request this permission.",
						Computed:    true,
					},
				},
			},
		},
	}
}

func (d *apiDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api"
}

func (d *apiDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	defer func() { resp.Schema = valuefree.DataSourceSchema(resp.Schema) }()
	attributes := apiDataSourceAttributes()
	attributes["id"] = schema.StringAttribute{
		Description: "The ID of the API. Exactly one of `id` and `resource` must be set.",
		Optional:    true,
		Computed:    true,
		Validators:  []validator.String{apiDataSourceIDValidator{}},
	}
	attributes["resource"] = schema.StringAttribute{
		Description: "The resource identifier of the API, exactly as Pocket ID stores it (without a trailing slash). " +
			"Exactly one of `id` and `resource` must be set. A value that contains the admin API key this provider authenticates with is refused.",
		Optional:   true,
		Computed:   true,
		Validators: []validator.String{apiDataSourceResourceValidator{}},
	}
	resp.Schema = schema.Schema{
		Description: "Looks up one Pocket ID API (protected resource) by ID or by resource identifier, with its permissions. Requires Pocket ID 2.14.0 or later.",
		Attributes:  attributes,
	}
}

func (d *apiDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("resource")),
	}
}

func (d *apiDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *apiDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg apiDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var found *client.API
	if !cfg.ID.IsNull() {
		id := cfg.ID.ValueString()
		api, err := d.client.GetAPI(ctx, id)
		if err != nil {
			if client.IsNotFound(err, client.ResourceAPI) {
				resp.Diagnostics.AddAttributeError(path.Root("id"), "API not found", fmt.Sprintf("Pocket ID has no API with ID %s.", id))
			} else {
				resp.Diagnostics.AddError("Unable to read API", err.Error())
			}
			return
		}
		if !client.SameUUID(api.ID, id) {
			resp.Diagnostics.AddError("Unexpected API response", fmt.Sprintf("Reading API %s returned a different or no API.", id))
			return
		}
		found = api
	} else {
		want := cfg.Resource.ValueString()
		// The configured identifier is printed when no API matches; one that
		// carries the API key is refused before anything is sent or shown.
		if d.client.ContainsAPIKey(want) {
			resp.Diagnostics.AddAttributeError(path.Root("resource"), "Value not supported",
				"The resource identifier contains the API key this provider authenticates with, which the provider never stores or prints. The value is not shown, and no request was made.")
			return
		}
		apis, err := d.client.ListAPIs(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list APIs", err.Error())
			return
		}
		for i := range apis {
			if apis[i].Resource == want {
				found = &apis[i]
				break
			}
		}
		if found == nil {
			resp.Diagnostics.AddAttributeError(path.Root("resource"), "API not found", fmt.Sprintf("Pocket ID has no API with resource %q.", want))
			return
		}
	}

	model, diags := apiDataSourceModelFromServer(found)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func apiDataSourcePermissions(api *client.API) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics
	permissions := map[string]attr.Value{}
	for _, p := range api.Permissions {
		if _, dup := permissions[p.Key]; dup {
			diags.AddError("Unexpected API response", fmt.Sprintf("Pocket ID reported permission key %q twice for API %s.", p.Key, api.ID))
			continue
		}
		object, d := types.ObjectValue(apiDataSourcePermissionAttrTypes, map[string]attr.Value{
			"id":                       types.StringValue(p.ID),
			"name":                     types.StringValue(p.Name),
			"description":              types.StringPointerValue(p.Description),
			"allowed_for_cimd_clients": types.BoolValue(p.AllowedForCIMDClients),
		})
		diags.Append(d...)
		permissions[p.Key] = object
	}
	result, d := types.MapValue(types.ObjectType{AttrTypes: apiDataSourcePermissionAttrTypes}, permissions)
	diags.Append(d...)
	return result, diags
}

func apiDataSourceModelFromServer(api *client.API) (apiDataSourceModel, diag.Diagnostics) {
	permissions, diags := apiDataSourcePermissions(api)
	return apiDataSourceModel{
		ID:               types.StringValue(api.ID),
		Resource:         types.StringValue(api.Resource),
		Name:             types.StringValue(api.Name),
		CreatedAt:        types.StringValue(api.CreatedAt),
		AllowCIMDClients: types.BoolValue(api.AllowCIMDClients),
		Permissions:      permissions,
	}, diags
}

// apiDataSourceIDValidator refuses an ID that is not a UUID, which no API
// can have.
type apiDataSourceIDValidator struct{}

func (apiDataSourceIDValidator) Description(context.Context) string {
	return "must be an API ID (a UUID)"
}

func (v apiDataSourceIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (apiDataSourceIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := client.ValidateUUID("API", req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid identifier", err.Error())
	}
}

// apiDataSourceResourceValidator refuses an identifier no API can have, such
// as one with a trailing slash, which Pocket ID removes before storing.
type apiDataSourceResourceValidator struct{}

func (apiDataSourceResourceValidator) Description(context.Context) string {
	return "must be an API resource identifier as Pocket ID stores it"
}

func (v apiDataSourceResourceValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (apiDataSourceResourceValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := client.APIResourceProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid API resource identifier", fmt.Sprintf("%s %s.", req.Path, problem))
	}
}
