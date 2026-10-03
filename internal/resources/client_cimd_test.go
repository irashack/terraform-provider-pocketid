package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client registered from a Client ID Metadata Document has an https URL as
// its ID; import refuses it with a diagnostic that says why, and refuses any
// other ID Pocket ID cannot have before a request is sent.
func TestClientImportRefusesCIMDAndInvalidIDs(t *testing.T) {
	ctx := context.Background()
	s := clientSchema(t).Schema
	for id, want := range map[string]string{
		"https://app.example.invalid/oauth/client.json": "Client ID Metadata Document",
		"~aHR0cHM6Ly9hcHAuZXhhbXBsZQ":                   "Invalid import ID",
		"has space":                                     "Invalid import ID",
		"jellyfin":                                      "",
	} {
		t.Run(id, func(t *testing.T) {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
			(&clientResource{}).ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
			if want == "" {
				require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
				return
			}
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, resp.Diagnostics[0].Summary(), want)
		})
	}
}

// Should Pocket ID ever report a CIMD client under a plain ID, Read and
// Update refuse it: Update before any mutation.
func TestClientRefusesCIMDClient(t *testing.T) {
	fake := managedFake(t, "2.17.0")
	fake.client.ClientType = "cimd"
	r := &clientResource{client: fake.start()}
	prior := managedModel()
	resp, _ := runRead(t, r, prior)
	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics[0].Detail(), "Client ID Metadata Document")

	planned := prior
	planned.Name = types.StringValue("renamed")
	updateResp, after := runUpdate(t, r, prior, planned, configOf(planned))
	require.True(t, updateResp.Diagnostics.HasError())
	assert.Empty(t, fake.mutations())
	assert.Equal(t, prior, after)
}
