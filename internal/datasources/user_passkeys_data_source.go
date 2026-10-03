package datasources

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/valuefree"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &userPasskeysDataSource{}
	_ datasource.DataSourceWithConfigure = &userPasskeysDataSource{}
)

func init() { register(NewUserPasskeysDataSource) }

// NewUserPasskeysDataSource creates the data source that lists a user's
// passkeys.
func NewUserPasskeysDataSource() datasource.DataSource {
	return &userPasskeysDataSource{}
}

type userPasskeysDataSource struct {
	client *client.Client
}

type userPasskeysDataSourceModel struct {
	UserID   types.String         `tfsdk:"user_id"`
	Passkeys []userPasskeyElement `tfsdk:"passkeys"`
}

type userPasskeyElement struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	CreatedAt      types.String `tfsdk:"created_at"`
	BackupEligible types.Bool   `tfsdk:"backup_eligible"`
	BackupState    types.Bool   `tfsdk:"backup_state"`
	Transports     types.Set    `tfsdk:"transports"`
	AAGUID         types.String `tfsdk:"aaguid"`
}

var userPasskeysUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// zeroAAGUID is what Pocket ID stores for a passkey whose authenticator
// reported no model identifier.
const zeroAAGUID = "00000000-0000-0000-0000-000000000000"

// Metadata returns the data source type name.
func (d *userPasskeysDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_passkeys"
}

// Schema defines the schema for the data source.
func (d *userPasskeysDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	defer func() { resp.Schema = valuefree.DataSourceSchema(resp.Schema) }()
	resp.Schema = schema.Schema{
		Description: "Lists the passkeys a Pocket-ID user has registered, read-only. For example, a precondition can require an administrator to have at least two. " +
			"It reports each passkey's identifier, name, registration time and whether it is backed up or synced, and no credential material: not the credential ID, not the public key. " +
			"Pocket ID records no time of last use, so none is reported. There is no resource to create or delete passkeys: deleting one can lock a user out.",
		Attributes: map[string]schema.Attribute{
			"user_id": schema.StringAttribute{
				Description: "The ID of the user (a UUID).",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(userPasskeysUUIDPattern, "must be a UUID"),
				},
			},
			"passkeys": schema.ListNestedAttribute{
				Description: "The user's passkeys, oldest first. Empty when the user has none.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "Pocket ID's identifier of the passkey.",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The name the user gave the passkey.",
							Computed:    true,
						},
						"created_at": schema.StringAttribute{
							Description: "When the passkey was registered, in RFC3339 format.",
							Computed:    true,
						},
						"backup_eligible": schema.BoolAttribute{
							Description: "Whether the authenticator allows the passkey to be backed up or synced to other devices.",
							Computed:    true,
						},
						"backup_state": schema.BoolAttribute{
							Description: "Whether the passkey is currently backed up or synced (a synced passkey, such as one in a platform keychain or password manager).",
							Computed:    true,
						},
						"transports": schema.SetAttribute{
							Description: "The transports the authenticator reported, such as `internal`, `usb` or `hybrid`. Empty when it reported none.",
							Computed:    true,
							ElementType: types.StringType,
						},
						"aaguid": schema.StringAttribute{
							Description: "The identifier of the authenticator model. Null when the authenticator reported none, and on Pocket ID 2.14, which does not report it.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *userPasskeysDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *userPasskeysDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data userPasskeysDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !lookupValuesOK(d.client, &resp.Diagnostics, map[string]types.String{"user_id": data.UserID}) {
		return
	}
	passkeys, err := d.client.ListUserPasskeys(ctx, data.UserID.ValueString())
	switch {
	case err == nil:
	case client.IsUserNotFound(err):
		resp.Diagnostics.AddError("User Not Found", fmt.Sprintf("No user found with id '%s'", data.UserID.ValueString()))
		return
	default:
		resp.Diagnostics.AddError("Unable to Read Passkeys", err.Error())
		return
	}

	// The server sorts nothing; oldest first, then by ID, keeps the order
	// stable between reads.
	sort.SliceStable(passkeys, func(i, j int) bool {
		if passkeys[i].CreatedAt != passkeys[j].CreatedAt {
			return passkeys[i].CreatedAt < passkeys[j].CreatedAt
		}
		return passkeys[i].ID < passkeys[j].ID
	})

	data.Passkeys = make([]userPasskeyElement, 0, len(passkeys))
	for _, passkey := range passkeys {
		transports, diags := ugIDSetValue(ctx, passkey.Transports)
		resp.Diagnostics.Append(diags...)
		element := userPasskeyElement{
			ID:             types.StringValue(passkey.ID),
			Name:           types.StringValue(passkey.Name),
			CreatedAt:      types.StringValue(passkey.CreatedAt),
			BackupEligible: types.BoolValue(passkey.BackupEligible),
			BackupState:    types.BoolValue(passkey.BackupState),
			Transports:     transports,
			AAGUID:         types.StringNull(),
		}
		if passkey.AAGUID != "" && passkey.AAGUID != zeroAAGUID {
			element.AAGUID = types.StringValue(passkey.AAGUID)
		}
		data.Passkeys = append(data.Passkeys, element)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
