package datasources_test

import (
	"net/http"
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

// An answer that carries the API key ("test-token" for this fake) in a text
// field never reaches the data source's state or a diagnostic.
func TestAnswersCarryingTheKeyNeverReachState(t *testing.T) {
	fake := newB2Fake(t)
	fake.handle("GET /api/users/"+b2UUID(1), func(w http.ResponseWriter, _ *http.Request) {
		b2JSON(w, http.StatusOK, map[string]any{
			"id": b2UUID(1), "username": "someone", "email": "a@example.com",
			"displayName": "Shown test-token here", "userGroups": []any{}, "customClaims": []any{},
		})
	})
	resp := b2Read(t, b2Configure(t, datasources.NewUserDataSource(), fake.client()), map[string]tftypes.Value{"id": b2Str(b2UUID(1))})
	require.True(t, resp.Diagnostics.HasError())
	assert.True(t, resp.State.Raw.IsNull(), "nothing is recorded")
	for _, d := range resp.Diagnostics {
		assert.NotContains(t, d.Summary()+d.Detail(), "test-token")
	}
}
