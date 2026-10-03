package resources

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// stringListFromServer converts a list Pocket ID returned. An empty list
// becomes an empty value when prior holds one (the configuration says []) and
// null otherwise, so both spellings of "none" stay stable.
func stringListFromServer(values []string, prior types.List) types.List {
	if len(values) == 0 {
		if !prior.IsNull() && !prior.IsUnknown() && len(prior.Elements()) == 0 {
			return types.ListValueMust(types.StringType, []attr.Value{})
		}
		return types.ListNull(types.StringType)
	}
	elements := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elements = append(elements, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elements)
}

// groupSetFromServer converts a client's allowed groups. An empty set becomes
// an empty value when prior holds one and null otherwise.
func groupSetFromServer(groups []client.UserGroup, prior types.Set) types.Set {
	if len(groups) == 0 {
		if !prior.IsNull() && !prior.IsUnknown() && len(prior.Elements()) == 0 {
			return types.SetValueMust(types.StringType, []attr.Value{})
		}
		return types.SetNull(types.StringType)
	}
	elements := make([]attr.Value, 0, len(groups))
	for _, g := range groups {
		elements = append(elements, types.StringValue(g.ID))
	}
	return types.SetValueMust(types.StringType, elements)
}

// launchURLFromServer converts the client's launch URL. Pocket ID reports a
// removed URL as null; state keeps "" when that is what it holds (the
// configuration removes the URL with ""), and null otherwise.
func launchURLFromServer(value string, prior types.String) types.String {
	if value == "" && !prior.IsNull() && !prior.IsUnknown() && prior.ValueString() == "" {
		return types.StringValue("")
	}
	return optionalString(value)
}

// launchURLForUpdate decides the launch URL an update sends. The update
// replaces the client in full, so an omitted launch_url (unmanaged) sends back
// the value the server holds right now, which may be newer than state. A
// configured value is sent as planned; "" removes the URL.
func launchURLForUpdate(configured, planned types.String, current string) *string {
	if configured.IsNull() {
		if current == "" {
			return nil
		}
		return &current
	}
	return stringPointer(planned)
}
