package datasources_test

import (
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

func TestVersionDataSource_Read(t *testing.T) {
	fake := newB2Fake(t)
	fake.handle("GET /api/version/current", func(w http.ResponseWriter, _ *http.Request) {
		b2JSON(w, http.StatusOK, map[string]any{"currentVersion": "v2.17.0"})
	})
	resp := b2Read(t, b2Configure(t, datasources.NewVersionDataSource(), fake.client()), nil)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var version string
	b2Attr(t, resp, "version", &version)
	assert.Equal(t, "2.17.0", version, "no leading v")
}

func TestVersionDataSource_Read_Failures(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"server without a version endpoint": func(w http.ResponseWriter, _ *http.Request) {
			b2JSON(w, 404, map[string]any{"error": "API endpoint not found"})
		},
		"not a version": func(w http.ResponseWriter, _ *http.Request) {
			b2JSON(w, 200, map[string]any{"currentVersion": "latest"})
		},
		"refused": func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, 403, map[string]any{"error": "no"}) },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newB2Fake(t)
			fake.handle("GET /api/version/current", handler)
			resp := b2Read(t, b2Configure(t, datasources.NewVersionDataSource(), fake.client()), nil)
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, []string{"Unable to Read the Server Version"}, b2Summaries(resp))
		})
	}
}

// A version whose pre-release or build part carries the API key the client
// sends ("test-token" for this fake) is refused: it never reaches the
// non-sensitive version attribute, and the diagnostic does not repeat it.
func TestVersionDataSource_Read_VersionCarryingTheKeyNeverReachesState(t *testing.T) {
	for name, reported := range map[string]string{
		"build metadata": "2.17.0+test-token",
		"pre-release":    "2.17.0-test-token.1",
	} {
		t.Run(name, func(t *testing.T) {
			fake := newB2Fake(t)
			fake.handle("GET /api/version/current", func(w http.ResponseWriter, _ *http.Request) {
				b2JSON(w, http.StatusOK, map[string]any{"currentVersion": reported})
			})
			resp := b2Read(t, b2Configure(t, datasources.NewVersionDataSource(), fake.client()), nil)
			require.True(t, resp.Diagnostics.HasError())
			assert.True(t, resp.State.Raw.IsNull(), "no version is recorded")
			for _, d := range resp.Diagnostics {
				assert.NotContains(t, d.Summary()+d.Detail(), "test-token")
			}
		})
	}
}

func TestCurrentUserDataSource_Read(t *testing.T) {
	fake := newB2Fake(t)
	fake.handle("GET /api/users/me", func(w http.ResponseWriter, _ *http.Request) {
		b2JSON(w, http.StatusOK, map[string]any{
			"id": b2UUID(9), "username": "admin", "email": "admin@example.com", "firstName": "Ad", "lastName": "Min",
			"displayName": "Ad Min", "isAdmin": true, "emailVerified": true, "disabled": false, "locale": "en",
			"userGroups":   []any{map[string]any{"id": b2UUID(1)}},
			"customClaims": []any{map[string]any{"key": "role", "value": "owner"}},
		})
	})
	resp := b2Read(t, b2Configure(t, datasources.NewCurrentUserDataSource(), fake.client()), nil)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	var id, username, locale string
	var admin bool
	var groups types.Set
	var claims types.Map
	b2Attr(t, resp, "id", &id)
	b2Attr(t, resp, "username", &username)
	b2Attr(t, resp, "locale", &locale)
	b2Attr(t, resp, "is_admin", &admin)
	b2Attr(t, resp, "groups", &groups)
	b2Attr(t, resp, "custom_claims", &claims)
	assert.Equal(t, b2UUID(9), id)
	assert.Equal(t, "admin", username)
	assert.Equal(t, "en", locale)
	assert.True(t, admin)
	assert.Equal(t, []string{b2UUID(1)}, b2SetStrings(t, groups))
	assert.Equal(t, map[string]string{"role": "owner"}, b2MapStrings(t, claims))
	assert.Equal(t, []string{"GET /api/users/me"}, fake.log())
}

// None of the attributes is an argument: the user is whoever owns the key.
func TestCurrentUserDataSource_Schema_HasNoArguments(t *testing.T) {
	for name, attribute := range b2Schema(t, datasources.NewCurrentUserDataSource()).Attributes {
		assert.True(t, attribute.IsComputed(), name)
		assert.False(t, attribute.IsRequired() || attribute.IsOptional(), name)
	}
}

func TestCurrentUserDataSource_Read_Failure(t *testing.T) {
	fake := newB2Fake(t)
	fake.handle("GET /api/users/me", func(w http.ResponseWriter, _ *http.Request) {
		b2JSON(w, 403, map[string]any{"error": "no", "code": "user_disabled"})
	})
	resp := b2Read(t, b2Configure(t, datasources.NewCurrentUserDataSource(), fake.client()), nil)
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{"Unable to Read the Current User"}, b2Summaries(resp))
}
