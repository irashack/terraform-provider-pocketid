package resources

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &userResource{}
	_ resource.ResourceWithConfigure   = &userResource{}
	_ resource.ResourceWithImportState = &userResource{}
)

func init() { register(NewUserResource) }

// NewUserResource is a helper function to simplify the provider implementation.
func NewUserResource() resource.Resource {
	return &userResource{}
}

// userResource is the resource implementation.
type userResource struct {
	client *client.Client
}

// userResourceModel maps the resource schema data.
type userResourceModel struct {
	ID            types.String `tfsdk:"id"`
	Username      types.String `tfsdk:"username"`
	Email         types.String `tfsdk:"email"`
	FirstName     types.String `tfsdk:"first_name"`
	LastName      types.String `tfsdk:"last_name"`
	DisplayName   types.String `tfsdk:"display_name"`
	EmailVerified types.Bool   `tfsdk:"email_verified"`
	IsAdmin       types.Bool   `tfsdk:"is_admin"`
	Locale        types.String `tfsdk:"locale"`
	Disabled      types.Bool   `tfsdk:"disabled"`
	Groups        types.Set    `tfsdk:"groups"`
	CustomClaims  types.Map    `tfsdk:"custom_claims"`
}

// Metadata returns the resource type name.
func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

// Schema defines the schema for the resource.
func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a user in Pocket-ID.",
		MarkdownDescription: `Manages a user in Pocket-ID.

~> **Important** Users must complete passkey registration through the Pocket-ID web interface. This resource only creates the user account; authentication setup must be done separately.`,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID of the user.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"username": schema.StringAttribute{
				Description: "The username for the user. Must be unique.",
				Required:    true,
			},
			"email": schema.StringAttribute{
				Description: "The email address of the user.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`),
						"Email must be a valid email address",
					),
				},
			},
			"first_name": schema.StringAttribute{
				Description: "The first name of the user. Omitted means none (an empty name in Pocket ID).",
				Optional:    true,
			},
			"last_name": schema.StringAttribute{
				Description: "The last name of the user. Omitted means none (an empty name in Pocket ID).",
				Optional:    true,
			},
			"display_name": schema.StringAttribute{
				Description: "The display name of the user. Computed from first and last name if not set.",
				Computed:    true,
				Optional:    true,
			},
			"email_verified": schema.BoolAttribute{
				Description: "Whether the user's email address is verified. Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"is_admin": schema.BoolAttribute{
				Description: "Whether the user has administrator privileges. Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"locale": schema.StringAttribute{
				Description: "The locale preference for the user (e.g., 'en', 'fr').",
				Optional:    true,
			},
			"disabled": schema.BoolAttribute{
				Description: "Whether the user account is disabled. Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"groups": schema.SetAttribute{
				Description: "IDs of the groups the user belongs to. Authoritative: the user is in exactly these groups, and in none when the attribute is omitted or empty. " +
					"On creation the groups are sent with the request, which keeps Pocket ID from adding the instance's signup default groups; when no groups are set, Pocket ID adds those defaults to the new user and the provider removes them right after, before the new account has a passkey or a session. " +
					"Pocket ID ignores an ID that names no group, so the provider checks the user's groups after each change and fails, naming the group, if one was not applied.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"custom_claims": schema.MapAttribute{
				Description:         "Custom claims to include in the user's OIDC tokens, as a map of claim name to value. Authoritative: the user has exactly these claims, and none when the attribute is omitted or empty. Pocket ID gives every new user the instance's signup default custom claims; the provider replaces them right after creation, before the new account has a passkey or a session. Reserved claim names (e.g. 'email', 'groups', 'sub') are rejected by Pocket-ID.",
				MarkdownDescription: "Custom claims to include in the user's OIDC tokens, as a map of claim name to value. Authoritative: the user has exactly these claims, and none when the attribute is omitted or `{}`. Pocket ID gives every new user the instance's signup default custom claims; the provider replaces them right after creation, before the new account has a passkey or a session. Reserved claim names (e.g. `email`, `groups`, `sub`) are rejected by Pocket-ID.",
				Optional:            true,
				ElementType:         types.StringType,
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// Create creates the resource and sets the initial Terraform state.
func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan userResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Convert groups and claims before anything is created, so a conversion
	// error cannot leave a new user behind.
	var groupIDs []string
	if !plan.Groups.IsNull() && !plan.Groups.IsUnknown() {
		resp.Diagnostics.Append(plan.Groups.ElementsAs(ctx, &groupIDs, false)...)
	}
	var claims []client.CustomClaim
	if !plan.CustomClaims.IsNull() && !plan.CustomClaims.IsUnknown() {
		converted, claimDiags := customClaimsToAPI(ctx, plan.CustomClaims)
		resp.Diagnostics.Append(claimDiags...)
		claims = converted
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// Build displayName from first and last names if not provided
	displayName := plan.DisplayName.ValueString()
	if displayName == "" {
		firstName := plan.FirstName.ValueString()
		lastName := plan.LastName.ValueString()
		if firstName != "" && lastName != "" {
			displayName = firstName + " " + lastName
		} else if firstName != "" {
			displayName = firstName
		} else if lastName != "" {
			displayName = lastName
		}
	}

	// Create the user
	createReq := &client.UserCreateRequest{
		Username:      plan.Username.ValueString(),
		Email:         plan.Email.ValueString(),
		FirstName:     plan.FirstName.ValueString(),
		LastName:      plan.LastName.ValueString(),
		DisplayName:   displayName,
		EmailVerified: plan.EmailVerified.ValueBool(),
		IsAdmin:       plan.IsAdmin.ValueBool(),
		Disabled:      plan.Disabled.ValueBool(),
		// Sent with the create, the planned groups also keep Pocket ID from
		// adding its signup default groups.
		UserGroupIDs: groupIDs,
	}

	// Handle locale if provided
	if !plan.Locale.IsNull() {
		locale := plan.Locale.ValueString()
		createReq.Locale = &locale
	}

	tflog.Debug(ctx, "Creating user", map[string]any{
		"username": createReq.Username,
		"email":    createReq.Email,
		"isAdmin":  createReq.IsAdmin,
	})

	userResp, err := r.client.CreateUser(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating user",
			"Could not create user, unexpected error: "+err.Error(),
		)
		return
	}

	tflog.Debug(ctx, "Created user", map[string]any{
		"id": userResp.ID,
	})

	// Set state values from API response
	plan.ID = types.StringValue(userResp.ID)
	setUserFieldsFromAPI(&plan, userResp)

	// Pocket ID gives a user created through the API the instance's signup
	// default groups (unless groups were sent with the create) and default
	// custom claims. The groups and claims attributes are authoritative, so
	// the user must end up with exactly the planned ones, none included.
	held := userResp.GroupIDs()
	if len(groupIDs) > 0 {
		if err := client.CheckUserGroups(userResp.ID, groupIDs, held); err != nil {
			r.failedCreate(ctx, &plan, "groups", err, resp)
			return
		}
	} else if len(held) > 0 {
		tflog.Debug(ctx, "Removing the signup default groups from the new user", map[string]any{
			"id": userResp.ID,
		})
		if _, err := r.setGroups(ctx, userResp.ID, nil); err != nil {
			r.failedCreate(ctx, &plan, "groups", err, resp)
			return
		}
	}
	plan.Groups = groupIDsToState(ctx, groupIDs, plan.Groups)

	// The create response does not show default claims, so the claims are
	// always replaced, with an empty list when none are planned.
	tflog.Debug(ctx, "Setting user custom claims", map[string]any{
		"id": userResp.ID,
	})
	updatedClaims, err := r.client.UpdateUserCustomClaims(ctx, userResp.ID, claims)
	if err == nil {
		err = checkCustomClaims(claims, updatedClaims)
	}
	if err != nil {
		r.failedCreate(ctx, &plan, "custom claims", err, resp)
		return
	}
	claimsMap, claimDiags := customClaimsToState(ctx, updatedClaims, plan.CustomClaims)
	if claimDiags.HasError() {
		r.failedCreate(ctx, &plan, "custom claims", errors.New("the server's custom claims could not be stored"), resp)
		return
	}
	plan.CustomClaims = claimsMap

	// Set the state
	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// setUserFieldsFromAPI copies the user's own fields from a Pocket ID response
// into model. An empty first or last name keeps model's representation (null
// stays null, "" stays ""), as does a missing locale.
func setUserFieldsFromAPI(model *userResourceModel, user *client.User) {
	model.Username = types.StringValue(user.Username)
	model.Email = types.StringValue(user.Email)
	model.FirstName = optionalStringToState(user.FirstName, model.FirstName)
	model.LastName = optionalStringToState(user.LastName, model.LastName)
	model.DisplayName = types.StringValue(user.DisplayName)
	model.EmailVerified = types.BoolValue(user.EmailVerified)
	model.IsAdmin = types.BoolValue(user.IsAdmin)
	model.Disabled = types.BoolValue(user.Disabled)
	if user.Locale != nil && *user.Locale != "" {
		model.Locale = types.StringValue(*user.Locale)
	} else {
		model.Locale = types.StringNull()
	}
}

// optionalStringToState returns value for an Optional string attribute whose
// server value is "" when unset. "" keeps the representation of like (the
// configured or prior value): null stays null, so an omitted attribute never
// becomes "".
func optionalStringToState(value string, like types.String) types.String {
	if value == "" && (like.IsNull() || like.IsUnknown()) {
		return types.StringNull()
	}
	return types.StringValue(value)
}

// Read refreshes the Terraform state with the latest data.
func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Get current state
	var state userResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading user", map[string]any{
		"id": state.ID.ValueString(),
	})

	// Get user from API
	userResp, err := r.client.GetUser(ctx, state.ID.ValueString())
	if err != nil {
		// Only Pocket ID's own "user not found" proves the user is gone; a
		// proxy's or a missing route's 404 stays an error.
		if client.IsNotFound(err, client.ResourceUser) {
			tflog.Warn(ctx, "User no longer exists, removing it from state", map[string]any{
				"id": state.ID.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading user",
			"Could not read user ID "+state.ID.ValueString()+": "+err.Error(),
		)
		return
	}

	// Update state from API response
	setUserFieldsFromAPI(&state, userResp)
	state.Groups = groupIDsToState(ctx, userResp.GroupIDs(), state.Groups)

	// Update custom claims
	claimsMap, claimDiags := customClaimsToState(ctx, userResp.CustomClaims, state.CustomClaims)
	resp.Diagnostics.Append(claimDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.CustomClaims = claimsMap

	// Set the state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Retrieve values from plan
	var plan userResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)

	// Retrieve current state
	var state userResourceModel
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Build displayName from first and last names if not provided
	displayName := plan.DisplayName.ValueString()
	if displayName == "" {
		firstName := plan.FirstName.ValueString()
		lastName := plan.LastName.ValueString()
		if firstName != "" && lastName != "" {
			displayName = firstName + " " + lastName
		} else if firstName != "" {
			displayName = firstName
		} else if lastName != "" {
			displayName = lastName
		}
	}

	// Update the user
	updateReq := &client.UserCreateRequest{
		Username:      plan.Username.ValueString(),
		Email:         plan.Email.ValueString(),
		FirstName:     plan.FirstName.ValueString(),
		LastName:      plan.LastName.ValueString(),
		DisplayName:   displayName,
		EmailVerified: plan.EmailVerified.ValueBool(),
		IsAdmin:       plan.IsAdmin.ValueBool(),
		Disabled:      plan.Disabled.ValueBool(),
	}

	// Handle locale if provided
	if !plan.Locale.IsNull() {
		locale := plan.Locale.ValueString()
		updateReq.Locale = &locale
	}

	tflog.Debug(ctx, "Updating user", map[string]any{
		"id":       plan.ID.ValueString(),
		"username": updateReq.Username,
		"email":    updateReq.Email,
	})

	userResp, err := r.client.UpdateUser(ctx, plan.ID.ValueString(), updateReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating user",
			"Could not update user, unexpected error: "+err.Error(),
		)
		return
	}

	// Update state values from API response
	setUserFieldsFromAPI(&plan, userResp)

	// Handle user groups
	var plannedGroupIDs []string
	if !plan.Groups.IsNull() && !plan.Groups.IsUnknown() {
		diags = plan.Groups.ElementsAs(ctx, &plannedGroupIDs, false)
		resp.Diagnostics.Append(diags...)
	}

	var currentGroupIDs []string
	if !state.Groups.IsNull() {
		diags = state.Groups.ElementsAs(ctx, &currentGroupIDs, false)
		resp.Diagnostics.Append(diags...)
	}

	if !resp.Diagnostics.HasError() {
		// Check if groups have changed
		groupsChanged := false
		if len(plannedGroupIDs) != len(currentGroupIDs) {
			groupsChanged = true
		} else {
			// Check if group IDs are different
			groupMap := make(map[string]bool)
			for _, id := range currentGroupIDs {
				groupMap[id] = true
			}
			for _, id := range plannedGroupIDs {
				if !groupMap[id] {
					groupsChanged = true
					break
				}
			}
		}

		if groupsChanged {
			tflog.Debug(ctx, "Updating user groups", map[string]any{
				"groups": plannedGroupIDs,
			})
			held, err := r.setGroups(ctx, plan.ID.ValueString(), plannedGroupIDs)
			if err != nil {
				plan.Groups = groupIDsToState(ctx, held, plan.Groups)
				var mismatch *client.UserGroupsMismatchError
				if errors.As(err, &mismatch) {
					// The write was made: record the groups the user is in now,
					// and the claims as they were, so the next plan shows what
					// is still to do.
					plan.CustomClaims = state.CustomClaims
					resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
				}
				resp.Diagnostics.AddError(
					"Error updating user groups",
					"Could not update user groups: "+err.Error(),
				)
				return
			}
		}
	}

	// Handle custom claims. The API performs a full replace, so any change to the
	// map (including clearing it) is applied by sending the full desired list.
	if !plan.CustomClaims.Equal(state.CustomClaims) {
		claims, claimDiags := customClaimsToAPI(ctx, plan.CustomClaims)
		resp.Diagnostics.Append(claimDiags...)
		if resp.Diagnostics.HasError() {
			return
		}

		tflog.Debug(ctx, "Updating user custom claims", map[string]any{
			"id": plan.ID.ValueString(),
		})
		updatedClaims, err := r.client.UpdateUserCustomClaims(ctx, plan.ID.ValueString(), claims)
		if err != nil {
			resp.Diagnostics.AddError(
				"Error updating user custom claims",
				"Could not update user custom claims: "+err.Error(),
			)
			return
		}

		claimsMap, claimDiags := customClaimsToState(ctx, updatedClaims, plan.CustomClaims)
		resp.Diagnostics.Append(claimDiags...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.CustomClaims = claimsMap
		if err := checkCustomClaims(claims, updatedClaims); err != nil {
			// The replacement was made: record what the user holds.
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			resp.Diagnostics.AddError("Error updating user custom claims", err.Error())
			return
		}
	}

	// Set the state
	diags = resp.State.Set(ctx, &plan)
	resp.Diagnostics.Append(diags...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Retrieve values from state
	var state userResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting user", map[string]any{
		"id": state.ID.ValueString(),
	})

	// Delete the user. A user Pocket ID confirms is already gone needs no
	// deletion; any other error, including a generic 404, stays an error.
	err := r.client.DeleteUser(ctx, state.ID.ValueString())
	if err != nil && client.IsNotFound(err, client.ResourceUser) {
		tflog.Warn(ctx, "User was already deleted", map[string]any{
			"id": state.ID.ValueString(),
		})
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting user",
			"Could not delete user, unexpected error: "+err.Error(),
		)
		return
	}

	tflog.Debug(ctx, "Deleted user", map[string]any{
		"id": state.ID.ValueString(),
	})
}

// setGroups replaces the user's groups with exactly groupIDs and verifies the
// result (see client.SetUserGroups). It holds the same per-user lock as
// pocketid_group_membership, so the two never interleave their
// read-modify-write cycles for one user within an apply.
func (r *userResource) setGroups(ctx context.Context, userID string, groupIDs []string) ([]string, error) {
	lock := lockForUser(userID)
	lock.Lock()
	defer lock.Unlock()
	return r.client.SetUserGroups(ctx, userID, groupIDs)
}

// groupIDsToState converts group IDs to the groups attribute. No groups keep
// the representation of like (null or an empty set).
func groupIDsToState(ctx context.Context, ids []string, like types.Set) types.Set {
	if len(ids) == 0 {
		if like.IsNull() || like.IsUnknown() {
			return types.SetNull(types.StringType)
		}
		return types.SetValueMust(types.StringType, []attr.Value{})
	}
	set, _ := types.SetValueFrom(ctx, types.StringType, ids)
	return set
}

// failedCreate handles a step that failed after Pocket ID created the user
// (plan.ID is set): the new user is deleted, and only a confirmed deletion
// lets Create end without state. Otherwise the user's ID stays in state, so
// Terraform keeps track of it (marked for replacement), and the diagnostic
// says what is known.
func (r *userResource) failedCreate(ctx context.Context, plan *userResourceModel, step string, cause error, resp *resource.CreateResponse) {
	outcome := rollBackAccountObject(ctx, createdAccountObject{
		kind: "user", id: plan.ID.ValueString(), missing: client.ResourceUser,
		remove: r.client.DeleteUser,
		read: func(ctx context.Context, id string) error {
			_, err := r.client.GetUser(ctx, id)
			return err
		},
	}, step, cause)
	if !outcome.gone {
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
	resp.Diagnostics.AddError(outcome.summary, outcome.detail)
}

// ImportState imports an existing resource into Terraform.
func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Retrieve import ID and set it as the resource ID
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
