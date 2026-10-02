package datasources_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

func b2GroupJSON(n int, name string) map[string]any {
	return map[string]any{
		"id": b2UUID(n), "name": name, "friendlyName": "Friendly " + name,
		"createdAt": "2026-01-02T03:04:05Z", "ldapId": nil,
		"customClaims": []any{}, "users": []any{}, "allowedOidcClients": []any{},
	}
}

// A group is read by ID from the single-object endpoint. The fake serves no
// list, so any attempt to find the group by scanning one fails the test: a
// list is paginated, and a group past its first page would read as missing.
func TestGroupDataSource_Read_ByIDUsesSingleObjectEndpoint(t *testing.T) {
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups/"+b2UUID(240), func(w http.ResponseWriter, _ *http.Request) {
		b2JSON(w, http.StatusOK, b2GroupJSON(240, "team-240"))
	})
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"id": b2Str(b2UUID(240))})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	var name, friendly, createdAt string
	b2Attr(t, resp, "name", &name)
	b2Attr(t, resp, "friendly_name", &friendly)
	b2Attr(t, resp, "created_at", &createdAt)
	assert.Equal(t, "team-240", name)
	assert.Equal(t, "Friendly team-240", friendly)
	assert.Equal(t, "2026-01-02T03:04:05Z", createdAt)
	assert.Equal(t, []string{"GET /api/user-groups/" + b2UUID(240)}, fake.log())
}

func TestGroupDataSource_Read_ByIDMissingGroup(t *testing.T) {
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups/"+b2UUID(7), func(w http.ResponseWriter, _ *http.Request) {
		b2NotFound(w, "User group")
	})
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"id": b2Str(b2UUID(7))})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{"Group Not Found"}, b2Summaries(resp))
}

// Only Pocket ID's own "this group does not exist" answer is reported as a
// missing group. A bare 404 (a wrong base URL, a proxy's page) and a server
// error are failures to read, which a user must not mistake for absence.
func TestGroupDataSource_Read_ByIDOtherFailuresAreNotAbsence(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"bare 404": func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
		"router 404": func(w http.ResponseWriter, _ *http.Request) {
			b2JSON(w, 404, map[string]any{"error": "API endpoint not found"})
		},
		"other not-found": func(w http.ResponseWriter, _ *http.Request) { b2NotFound(w, "OIDC client") },
		"forbidden":       func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, 403, map[string]any{"error": "no"}) },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newB2Fake(t)
			fake.handle("GET /api/user-groups/"+b2UUID(7), handler)
			ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

			resp := b2Read(t, ds, map[string]tftypes.Value{"id": b2Str(b2UUID(7))})
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, []string{"Unable to Read Group"}, b2Summaries(resp))
		})
	}
}

// An ID that is not a UUID is refused before any request is sent. (The
// documentation example used to show a made-up "grp_..." ID.)
func TestGroupDataSource_Read_ByIDMustBeUUID(t *testing.T) {
	fake := newB2Fake(t)
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"id": b2Str("grp_1234567890")})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{"Invalid group ID"}, b2Summaries(resp))
	assert.Empty(t, fake.log())
}

// b2GroupSearchHandler serves GET /api/user-groups for a fixed set of groups
// the way Pocket ID does: the search term selects groups whose name contains
// it (case-insensitive), and the result is paginated.
func b2GroupSearchHandler(groups []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		term := strings.ToLower(r.URL.Query().Get("search"))
		var matched []any
		for _, g := range groups {
			if strings.Contains(strings.ToLower(g["name"].(string)), term) {
				matched = append(matched, g)
			}
		}
		b2Paginate(w, r, matched)
	}
}

// The group is on the third page of the full list, and the name is a prefix of
// several others: the lookup must send the search, read every page of its
// result and compare names exactly.
func TestGroupDataSource_Read_ByNameFindsExactMatchAmongSimilarOnes(t *testing.T) {
	var groups []map[string]any
	for i := 1; i <= 230; i++ {
		groups = append(groups, b2GroupJSON(i, fmt.Sprintf("team-%d", i)))
	}
	groups = append(groups, b2GroupJSON(231, "admins-extra"), b2GroupJSON(232, "Admins"), b2GroupJSON(233, "admins"))
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups", b2GroupSearchHandler(groups))
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"name": b2Str("admins")})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var id string
	b2Attr(t, resp, "id", &id)
	assert.Equal(t, b2UUID(233), id, "the group named exactly admins, not Admins or admins-extra")
	for _, request := range fake.log() {
		assert.Contains(t, request, "search=admins")
	}

	// Without the exact comparison and with several pages the last group would
	// be missed: team-230 only exists on the third page of an unfiltered list.
	resp = b2Read(t, ds, map[string]tftypes.Value{"name": b2Str("team-230")})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	b2Attr(t, resp, "id", &id)
	assert.Equal(t, b2UUID(230), id)
}

func TestGroupDataSource_Read_ByNameWithNoExactMatch(t *testing.T) {
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups", b2GroupSearchHandler([]map[string]any{b2GroupJSON(1, "admins-extra")}))
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"name": b2Str("admins")})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{"Group Not Found"}, b2Summaries(resp))
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "No group found with name 'admins'")
}

// PostgreSQL reads a backslash in a LIKE pattern as an escape, so a search for
// such a name could miss the group it names. The lookup lists every group
// instead and still finds it.
func TestGroupDataSource_Read_ByNameWithBackslashListsEveryGroup(t *testing.T) {
	groups := []map[string]any{b2GroupJSON(1, "plain"), b2GroupJSON(2, `corp\admins`)}
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups", func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.URL.Query().Get("search"))
		b2Paginate(w, r, []any{groups[0], groups[1]})
	})
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"name": b2Str(`corp\admins`)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var id string
	b2Attr(t, resp, "id", &id)
	assert.Equal(t, b2UUID(2), id)
}

// With both arguments, the group found by ID must carry the name. Before, the
// first group matching either was returned.
func TestGroupDataSource_Read_IDAndNameMustNameTheSameGroup(t *testing.T) {
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups/"+b2UUID(1), func(w http.ResponseWriter, _ *http.Request) {
		b2JSON(w, http.StatusOK, b2GroupJSON(1, "alpha"))
	})
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"id": b2Str(b2UUID(1)), "name": b2Str("beta")})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{"Conflicting arguments"}, b2Summaries(resp))

	resp = b2Read(t, ds, map[string]tftypes.Value{"id": b2Str(b2UUID(1)), "name": b2Str("alpha")})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
}

// pocketid_groups lists every group, however many pages that takes.
func TestGroupsDataSource_Read_ListsEveryPage(t *testing.T) {
	var groups []any
	for i := 1; i <= 250; i++ {
		g := b2GroupJSON(i, fmt.Sprintf("team-%d", i))
		delete(g, "users")
		delete(g, "allowedOidcClients")
		g["userCount"] = 0
		groups = append(groups, g)
	}
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups", func(w http.ResponseWriter, r *http.Request) { b2Paginate(w, r, groups) })
	ds := b2Configure(t, datasources.NewGroupsDataSource(), fake.client())

	resp := b2Read(t, ds, nil)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var list types.List
	b2Attr(t, resp, "groups", &list)
	assert.Len(t, list.Elements(), 250)
}
