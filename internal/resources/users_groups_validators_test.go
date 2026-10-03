package resources

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func usersGroupsStringErrors(t *testing.T, validators []validator.String, value string) bool {
	t.Helper()
	failed := false
	for _, v := range validators {
		resp := validator.StringResponse{}
		v.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("x"), ConfigValue: types.StringValue(value)}, &resp)
		failed = failed || resp.Diagnostics.HasError()
	}
	return failed
}

func usersGroupsMapErrors(t *testing.T, validators []validator.Map, value map[string]string) bool {
	t.Helper()
	elems := map[string]attr.Value{}
	for k, v := range value {
		elems[k] = types.StringValue(v)
	}
	failed := false
	for _, v := range validators {
		resp := validator.MapResponse{}
		v.ValidateMap(context.Background(), validator.MapRequest{Path: path.Root("x"), ConfigValue: types.MapValueMust(types.StringType, elems)}, &resp)
		failed = failed || resp.Diagnostics.HasError()
	}
	return failed
}

func usersGroupsAttrStringValidators(t *testing.T, s schema.Schema, name string) []validator.String {
	t.Helper()
	a, ok := s.Attributes[name].(schema.StringAttribute)
	require.True(t, ok, name)
	return a.Validators
}

// The schemas apply Pocket ID's own rules (2.14.0 to 2.17.0) at plan time.
func TestUsersGroupsSchemaValidators(t *testing.T) {
	ctx := context.Background()
	var user, group, token resource.SchemaResponse
	(&userResource{}).Schema(ctx, resource.SchemaRequest{}, &user)
	(&groupResource{}).Schema(ctx, resource.SchemaRequest{}, &group)
	(&OneTimeAccessTokenResource{}).Schema(ctx, resource.SchemaRequest{}, &token)

	username := usersGroupsAttrStringValidators(t, user.Schema, "username")
	for _, ok := range []string{"a", "john.doe", "a@b", "x_y-z.9", strings.Repeat("a", 50)} {
		assert.False(t, usersGroupsStringErrors(t, username, ok), ok)
	}
	for _, bad := range []string{"", "-bad", "bad.", "has space", "ünï", strings.Repeat("a", 51)} {
		assert.True(t, usersGroupsStringErrors(t, username, bad), bad)
	}

	// Lengths count characters, as the server does: 50 two-byte characters
	// are accepted, 51 are not.
	for _, name := range []string{"first_name", "last_name"} {
		v := usersGroupsAttrStringValidators(t, user.Schema, name)
		assert.False(t, usersGroupsStringErrors(t, v, strings.Repeat("é", 50)), name)
		assert.True(t, usersGroupsStringErrors(t, v, strings.Repeat("é", 51)), name)
		assert.False(t, usersGroupsStringErrors(t, v, ""), name)
	}
	display := usersGroupsAttrStringValidators(t, user.Schema, "display_name")
	assert.False(t, usersGroupsStringErrors(t, display, strings.Repeat("é", 100)))
	assert.True(t, usersGroupsStringErrors(t, display, strings.Repeat("é", 101)))

	groupName := usersGroupsAttrStringValidators(t, group.Schema, "name")
	assert.True(t, usersGroupsStringErrors(t, groupName, "a"))
	assert.False(t, usersGroupsStringErrors(t, groupName, "ab"))
	assert.False(t, usersGroupsStringErrors(t, groupName, strings.Repeat("é", 255)))
	assert.True(t, usersGroupsStringErrors(t, groupName, strings.Repeat("a", 256)))
	friendly := usersGroupsAttrStringValidators(t, group.Schema, "friendly_name")
	assert.True(t, usersGroupsStringErrors(t, friendly, "a"))
	assert.False(t, usersGroupsStringErrors(t, friendly, strings.Repeat("é", 50)))
	assert.True(t, usersGroupsStringErrors(t, friendly, strings.Repeat("a", 51)))

	for _, s := range []schema.Schema{user.Schema, group.Schema} {
		claims := s.Attributes["custom_claims"].(schema.MapAttribute).Validators
		assert.False(t, usersGroupsMapErrors(t, claims, map[string]string{"department": "x", "Email": "y"}), "keys are compared case-sensitively")
		for _, reserved := range reservedClaimKeys {
			assert.True(t, usersGroupsMapErrors(t, claims, map[string]string{reserved: "x"}), reserved)
		}
		assert.True(t, usersGroupsMapErrors(t, claims, map[string]string{"": "x"}), "empty key")
		assert.True(t, usersGroupsMapErrors(t, claims, map[string]string{"k": ""}), "empty value")
	}

	ttl := usersGroupsAttrStringValidators(t, token.Schema, "ttl")
	for _, ok := range []string{"2s", "15m", "1h", "744h"} {
		assert.False(t, usersGroupsStringErrors(t, ttl, ok), ok)
	}
	for _, bad := range []string{"", "1s", "0s", "-1h", "745h", "not-a-duration"} {
		assert.True(t, usersGroupsStringErrors(t, ttl, bad), bad)
	}
}
