package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &signupTokensDataSource{}
	_ datasource.DataSourceWithConfigure = &signupTokensDataSource{}
)

func init() { register(NewSignupTokensDataSource) }

// NewSignupTokensDataSource creates a new signup tokens data source.
func NewSignupTokensDataSource() datasource.DataSource {
	return &signupTokensDataSource{}
}

// signupTokensDataSource is the data source implementation.
type signupTokensDataSource struct {
	client *client.Client
}

type signupTokensDataSourceModel struct {
	Tokens []signupTokenItemModel `tfsdk:"tokens"`
}

type signupTokenItemModel struct {
	ID           types.String `tfsdk:"id"`
	ExpiresAt    types.String `tfsdk:"expires_at"`
	CreatedAt    types.String `tfsdk:"created_at"`
	UsageLimit   types.Int64  `tfsdk:"usage_limit"`
	UsageCount   types.Int64  `tfsdk:"usage_count"`
	UserGroupIDs types.Set    `tfsdk:"user_group_ids"`
}

// Metadata returns the data source type name.
func (d *signupTokensDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_signup_tokens"
}

// Schema defines the schema for the data source.
func (d *signupTokensDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the signup tokens that are currently valid in Pocket-ID, without their values.",
		MarkdownDescription: "Lists the signup tokens that are currently valid in Pocket-ID, oldest first: the ones created in the " +
			"Pocket-ID interface as well as by `pocketid_signup_token`. Pocket-ID removes a token when it expires, so expired " +
			"tokens are not listed; a token whose uses are used up is listed until it expires.\n\n" +
			"**Token values are deliberately not exposed.** Pocket-ID's list includes each token's value, but a value lets " +
			"anyone who has it register an account, and this data source would copy the value of every outstanding token, " +
			"including tokens created outside Terraform, into the state. It returns only each token's ID, times, limits, " +
			"use count and groups. The value of a token that Terraform created is available as `token` on " +
			"`pocketid_signup_token`.",
		Attributes: map[string]schema.Attribute{
			"tokens": schema.ListNestedAttribute{
				Description: "The valid signup tokens, oldest first.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The ID of the signup token.",
							Computed:    true,
						},
						"expires_at": schema.StringAttribute{
							Description: "When the token expires, in RFC3339 format.",
							Computed:    true,
						},
						"created_at": schema.StringAttribute{
							Description: "When the token was created, in RFC3339 format.",
							Computed:    true,
						},
						"usage_limit": schema.Int64Attribute{
							Description: "How many people can register with the token.",
							Computed:    true,
						},
						"usage_count": schema.Int64Attribute{
							Description: "How many people have registered with the token.",
							Computed:    true,
						},
						"user_group_ids": schema.SetAttribute{
							Description: "IDs of the groups people who register with the token join.",
							Computed:    true,
							ElementType: types.StringType,
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the data source.
func (d *signupTokensDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *signupTokensDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	tokens, err := d.client.ListSignupTokens(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Signup Tokens", err.Error())
		return
	}
	tflog.Debug(ctx, "Retrieved signup tokens", map[string]any{"count": len(tokens)})

	data := signupTokensDataSourceModel{Tokens: make([]signupTokenItemModel, len(tokens))}
	for i, token := range tokens {
		groups := make([]attr.Value, 0, len(token.UserGroups))
		for _, group := range token.UserGroups {
			groups = append(groups, types.StringValue(group.ID))
		}
		data.Tokens[i] = signupTokenItemModel{
			ID:           types.StringValue(token.ID),
			ExpiresAt:    types.StringValue(token.ExpiresAt),
			CreatedAt:    types.StringValue(token.CreatedAt),
			UsageLimit:   types.Int64Value(int64(token.UsageLimit)),
			UsageCount:   types.Int64Value(int64(token.UsageCount)),
			UserGroupIDs: types.SetValueMust(types.StringType, groups),
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
