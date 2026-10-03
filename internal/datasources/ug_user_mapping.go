package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// ugUserModelFromAPI maps a user as Pocket ID reports it to the attributes the
// user data sources share.
func ugUserModelFromAPI(ctx context.Context, user *client.User) (userModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	model := userModel{
		ID:            types.StringValue(user.ID),
		Username:      types.StringValue(user.Username),
		Email:         types.StringNull(),
		FirstName:     types.StringValue(user.FirstName),
		LastName:      types.StringValue(user.LastName),
		DisplayName:   types.StringValue(user.DisplayName),
		EmailVerified: types.BoolValue(user.EmailVerified),
		IsAdmin:       types.BoolValue(user.IsAdmin),
		Locale:        types.StringNull(),
		Disabled:      types.BoolValue(user.Disabled),
		LdapID:        types.StringNull(),
	}
	// A user without an email address (Pocket ID allows that unless it
	// requires one) has "" in the client's model: report null, not an empty
	// string.
	if user.Email != "" {
		model.Email = types.StringValue(user.Email)
	}
	if user.Locale != nil && *user.Locale != "" {
		model.Locale = types.StringValue(*user.Locale)
	}
	if user.LdapID != nil && *user.LdapID != "" {
		model.LdapID = types.StringValue(*user.LdapID)
	}

	// A user in no group has a null set, as the data sources always reported.
	if len(user.UserGroups) > 0 {
		groupIDs := make([]string, 0, len(user.UserGroups))
		for _, group := range user.UserGroups {
			groupIDs = append(groupIDs, group.ID)
		}
		var groupDiags diag.Diagnostics
		model.Groups, groupDiags = types.SetValueFrom(ctx, types.StringType, groupIDs)
		diags.Append(groupDiags...)
	} else {
		model.Groups = types.SetNull(types.StringType)
	}

	var claimDiags diag.Diagnostics
	model.CustomClaims, claimDiags = ugClaimsMapValue(ctx, user.CustomClaims)
	diags.Append(claimDiags...)
	return model, diags
}

// userAttributeDescriptions are the descriptions of the attributes every user
// data source reports, so that they read the same everywhere.
const ugUserClaimsDescription = "The user's custom claims, by claim key. Empty when the user has none."
