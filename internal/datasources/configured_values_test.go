package datasources_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

// A lookup value that contains the API key ("test-token" for this fake) is
// refused before any request, and no diagnostic repeats it: the diagnostics
// that explain an empty or failed lookup would otherwise name it.
func TestLookupValuesCarryingTheKeyAreRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		ds     func() datasource.DataSource
		values map[string]tftypes.Value
	}{
		"user by email":    {datasources.NewUserDataSource, map[string]tftypes.Value{"email": b2Str("x-test-token@example.com")}},
		"user by username": {datasources.NewUserDataSource, map[string]tftypes.Value{"username": b2Str("test-token-user")}},
		"group by name":    {datasources.NewGroupDataSource, map[string]tftypes.Value{"name": b2Str("admins-test-token")}},
		"client by id":     {datasources.NewClientDataSource, map[string]tftypes.Value{"id": b2Str("app-test-token")}},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newB2Fake(t)
			resp := b2Read(t, b2Configure(t, tc.ds(), fake.client()), tc.values)
			require.True(t, resp.Diagnostics.HasError())
			for _, d := range resp.Diagnostics {
				assert.NotContains(t, d.Summary()+d.Detail(), "test-token")
			}
			assert.Empty(t, fake.log(), "nothing is sent")
		})
	}
}
