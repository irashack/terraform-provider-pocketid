package resources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Read keeps the configuration's spelling of "none": null stays null and an
// explicit empty value stays empty, for logout URLs, allowed groups and the
// launch URL; values from the server replace whatever state held.
func TestClientReadKeepsEmptyValues(t *testing.T) {
	emptyList := types.ListValueMust(types.StringType, []attr.Value{})
	emptySet := types.SetValueMust(types.StringType, []attr.Value{})
	for name, tc := range map[string]struct {
		logout types.List
		groups types.Set
		launch types.String
		server func(c *fakeClient)
		want   func(t *testing.T, m clientResourceModel)
	}{
		"null stays null": {types.ListNull(types.StringType), types.SetNull(types.StringType), types.StringNull(), nil, func(t *testing.T, m clientResourceModel) {
			assert.True(t, m.LogoutCallbackURLs.IsNull())
			assert.True(t, m.AllowedUserGroups.IsNull())
			assert.True(t, m.LaunchURL.IsNull())
		}},
		"empty stays empty": {emptyList, emptySet, types.StringValue(""), nil, func(t *testing.T, m clientResourceModel) {
			assert.Equal(t, emptyList, m.LogoutCallbackURLs)
			assert.Equal(t, emptySet, m.AllowedUserGroups)
			assert.Equal(t, types.StringValue(""), m.LaunchURL)
		}},
		"server values replace empty": {emptyList, emptySet, types.StringValue(""), func(c *fakeClient) {
			c.LogoutCallbacks = []string{"https://example.invalid/logout"}
			c.Allowed = []string{"bbbbbbbb-0000-4000-8000-000000000002", "bbbbbbbb-0000-4000-8000-000000000001"}
			c.LaunchURL = "https://example.invalid/"
		}, func(t *testing.T, m clientResourceModel) {
			assert.Equal(t, types.ListValueMust(types.StringType, []attr.Value{types.StringValue("https://example.invalid/logout")}), m.LogoutCallbackURLs)
			assert.True(t, stringSet("bbbbbbbb-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002").Equal(m.AllowedUserGroups), "%v", m.AllowedUserGroups)
			assert.Equal(t, "https://example.invalid/", m.LaunchURL.ValueString())
		}},
		"stale values cleared": {types.ListValueMust(types.StringType, []attr.Value{types.StringValue("https://old.invalid/")}), stringSet("g9"), types.StringValue("https://old.invalid/"), nil, func(t *testing.T, m clientResourceModel) {
			assert.True(t, m.LogoutCallbackURLs.IsNull())
			assert.True(t, m.AllowedUserGroups.IsNull())
			assert.True(t, m.LaunchURL.IsNull())
		}},
	} {
		t.Run(name, func(t *testing.T) {
			fake := managedFake(t, "2.16.0")
			if tc.server != nil {
				tc.server(fake.client)
			}
			prior := managedModel()
			prior.LogoutCallbackURLs, prior.AllowedUserGroups, prior.LaunchURL = tc.logout, tc.groups, tc.launch
			resp, after := runRead(t, &clientResource{client: fake.start()}, prior)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			tc.want(t, after)
		})
	}
}

// An update sends back the launch URL the server holds when launch_url is
// not configured, even one set outside Terraform since the last refresh, and
// records the plan; a configured value is sent, and "" removes the URL. A
// computed has_logo that was known in the plan is kept, never replaced by a
// different value from the response.
func TestClientUpdateLaunchURLAndLogo(t *testing.T) {
	const outside, configured = "https://outside.invalid/", "https://configured.invalid/"
	for name, tc := range map[string]struct {
		prior, config, planned types.String
		sent                   any // nil: absent
		recorded               types.String
	}{
		"unmanaged, set outside, unrefreshed": {types.StringNull(), types.StringNull(), types.StringNull(), outside, types.StringNull()},
		"unmanaged, stale state":              {types.StringValue("https://stale.invalid/"), types.StringNull(), types.StringValue("https://stale.invalid/"), outside, types.StringValue("https://stale.invalid/")},
		"configured":                          {types.StringNull(), types.StringValue(configured), types.StringValue(configured), configured, types.StringValue(configured)},
		"removed with empty string":           {types.StringValue(outside), types.StringValue(""), types.StringValue(""), nil, types.StringValue("")},
		"unknown before the first refresh":    {types.StringNull(), types.StringNull(), types.StringUnknown(), outside, types.StringValue(outside)},
	} {
		t.Run(name, func(t *testing.T) {
			fake := managedFake(t, "2.16.0")
			fake.client.LaunchURL = outside
			fake.client.HasLogo = true // uploaded since the last refresh
			r := &clientResource{client: fake.start()}
			prior := managedModel()
			prior.LaunchURL = tc.prior
			planned := prior
			planned.Name = types.StringValue("renamed")
			planned.LaunchURL = tc.planned
			config := configOf(planned)
			config.LaunchURL = tc.config

			resp, after := runUpdate(t, r, prior, planned, config)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			require.Len(t, fake.puts, 1)
			assert.Equal(t, "renamed", fake.puts[0]["name"])
			assert.Equal(t, tc.sent, fake.puts[0]["launchURL"])
			assert.Equal(t, tc.recorded, after.LaunchURL)
			assert.Equal(t, "renamed", after.Name.ValueString())
			assert.False(t, after.HasLogo.ValueBool(), "the planned has_logo is kept")
			assert.Equal(t, prior.ClientSecret, after.ClientSecret, "the secret is kept")
		})
	}
}
