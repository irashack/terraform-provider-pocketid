package datasources_test

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
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

// b2GroupDetailHandler serves GET /api/user-groups/{id} for a fixed set of
// groups.
func b2GroupDetailHandler(groups []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/user-groups/")
		for _, g := range groups {
			if g["id"] == id {
				b2JSON(w, http.StatusOK, g)
				return
			}
		}
		b2NotFound(w, "User group")
	}
}

// b2SQLLike reports whether name matches the SQL pattern "%term%" the way
// Pocket ID's search does (name LIKE ?): "%" in term matches any run of
// characters, "_" any one character, and ASCII letters match in either case (a
// SQLite default). There is no escape character, which is why a backslash in a
// name is not searched for. Everything else matches itself.
func b2SQLLike(term, name string) bool {
	var pattern strings.Builder
	pattern.WriteString("(?is)^.*")
	for _, r := range term {
		switch r {
		case '%':
			pattern.WriteString(".*")
		case '_':
			pattern.WriteString(".")
		default:
			pattern.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	pattern.WriteString(".*$")
	return regexp.MustCompile(pattern.String()).MatchString(name)
}

// b2GroupSearchHandler serves GET /api/user-groups for a fixed set of groups
// the way Pocket ID does: the search term selects groups whose name matches
// "%term%" as a SQL LIKE pattern (so "%" and "_" in the term are wildcards, and
// ASCII case is ignored), in creation order, and the result is paginated. The
// request's query strings are recorded in queries when it is not nil.
func b2GroupSearchHandler(groups []map[string]any) http.HandlerFunc {
	return b2RecordingGroupSearchHandler(groups, nil)
}

func b2RecordingGroupSearchHandler(groups []map[string]any, queries *[]url.Values) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		if queries != nil {
			mu.Lock()
			*queries = append(*queries, r.URL.Query())
			mu.Unlock()
		}
		term := r.URL.Query().Get("search")
		var matched []any
		for _, g := range groups {
			if b2SQLLike(term, g["name"].(string)) {
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
	fake.handlePrefix("GET /api/user-groups/", b2GroupDetailHandler(groups))
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"name": b2Str("admins")})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var id string
	b2Attr(t, resp, "id", &id)
	assert.Equal(t, b2UUID(233), id, "the group named exactly admins, not Admins or admins-extra")
	assert.Contains(t, fake.log()[0], "search=admins")
	assert.Equal(t, "GET /api/user-groups/"+b2UUID(233), fake.log()[len(fake.log())-1], "the group found is then read itself")

	// Without the exact comparison and with several pages the last group would
	// be missed: team-230 only exists on the third page of an unfiltered list.
	resp = b2Read(t, ds, map[string]tftypes.Value{"name": b2Str("team-230")})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	b2Attr(t, resp, "id", &id)
	assert.Equal(t, b2UUID(230), id)
}

// The search has more than one page of candidates and the exact match is on the
// second, because the groups are served in creation order and the exact name
// was created last. A lookup that read only the first page would report the
// group missing. Every request after the first must carry the same search term,
// or the later pages would list unrelated groups.
func TestGroupDataSource_Read_ByNameFindsAnExactMatchBeyondTheFirstSearchPage(t *testing.T) {
	var groups []map[string]any
	for i := 1; i <= 130; i++ {
		groups = append(groups, b2GroupJSON(i, fmt.Sprintf("admins-%03d", i)))
	}
	groups = append(groups, b2GroupJSON(131, "admins"), b2GroupJSON(132, "admins-after"))
	var queries []url.Values
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups", b2RecordingGroupSearchHandler(groups, &queries))
	fake.handlePrefix("GET /api/user-groups/", b2GroupDetailHandler(groups))
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"name": b2Str("admins")})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var id string
	b2Attr(t, resp, "id", &id)
	assert.Equal(t, b2UUID(131), id, "the exact match is the 131st candidate, on the second page")

	require.GreaterOrEqual(t, len(queries), 2, "the second page was requested")
	pages := map[string]bool{}
	for _, query := range queries {
		assert.Equal(t, "admins", query.Get("search"), "every page of the search carries the search term")
		pages[query.Get("pagination[page]")] = true
	}
	assert.True(t, pages["1"] && pages["2"], "pages seen: %v", pages)
}

// The search is a SQL LIKE: "_" and "%" in a name are wildcards, so the server
// returns look-alikes, and ASCII case is ignored. The lookup must still return
// only the group whose name is exactly the configured one, wherever it is among
// the candidates, and must not return a look-alike when there is no such group.
func TestGroupDataSource_Read_ByNameWithWildcardCharacters(t *testing.T) {
	var groups []map[string]any
	// 105 look-alike candidates for "team_1" (the "_" matches any character).
	for i := 1; i <= 105; i++ {
		groups = append(groups, b2GroupJSON(i, fmt.Sprintf("team-1-%03d", i)))
	}
	groups = append(groups,
		b2GroupJSON(200, "teamX1"),
		b2GroupJSON(201, "TEAM_1"),
		b2GroupJSON(202, "team_1"), // the group looked up: on the second page
		b2GroupJSON(203, "team 1"),
		b2GroupJSON(210, "50 percent off"),
		b2GroupJSON(211, "50%off"),
		b2GroupJSON(212, "50xoff"),
		b2GroupJSON(220, "a_b"),
		b2GroupJSON(221, "axb"),
		b2GroupJSON(230, "teamY2"),
		b2GroupJSON(231, "abc"),
	)

	cases := []struct {
		name       string
		look       string
		wantID     int // 0: not found
		candidates int // how many groups the server's LIKE matches
	}{
		{"underscore is a wildcard, exact match beyond page one", "team_1", 202, 109},
		{"upper-case look-alike is not the group", "TEAM_1", 201, 109},
		{"percent is a wildcard", "50%off", 211, 3},
		{"underscore among a few candidates", "a_b", 220, 2},
		{"a look-alike without the named group", "team_2", 0, 1},
		{"wildcard look-alikes without the named group", "a%b%c", 0, 1},
		{"underscore look-alikes without the named group", "50_off", 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidates := 0
			for _, g := range groups {
				if b2SQLLike(tc.look, g["name"].(string)) {
					candidates++
				}
			}
			require.Equal(t, tc.candidates, candidates, "the fake's LIKE returns the look-alikes this case is about")

			var queries []url.Values
			fake := newB2Fake(t)
			fake.handle("GET /api/user-groups", b2RecordingGroupSearchHandler(groups, &queries))
			fake.handlePrefix("GET /api/user-groups/", b2GroupDetailHandler(groups))
			ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

			resp := b2Read(t, ds, map[string]tftypes.Value{"name": b2Str(tc.look)})
			require.NotEmpty(t, queries)
			for _, query := range queries {
				assert.Equal(t, tc.look, query.Get("search"), "the term is sent as typed, for the server's LIKE to apply")
			}
			if tc.wantID == 0 {
				require.True(t, resp.Diagnostics.HasError())
				assert.Equal(t, []string{"Group Not Found"}, b2Summaries(resp))
				return
			}
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			var id, name string
			b2Attr(t, resp, "id", &id)
			b2Attr(t, resp, "name", &name)
			assert.Equal(t, b2UUID(tc.wantID), id)
			assert.Equal(t, tc.look, name)
		})
	}
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

// A group found by name that is renamed before its own record is read must not
// be returned for the old name: the detail's name is compared again.
func TestGroupDataSource_Read_ByNameReportsARenameDuringTheLookup(t *testing.T) {
	groups := []map[string]any{b2GroupJSON(1, "admins")}
	renamed := b2GroupJSON(1, "operators")
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups", b2GroupSearchHandler(groups))
	fake.handle("GET /api/user-groups/"+b2UUID(1), func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, http.StatusOK, renamed) })
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"name": b2Str("admins")})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{"Group changed while it was being read"}, b2Summaries(resp))
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "renamed")
	assert.True(t, resp.State.Raw.IsNull(), "no group is reported")
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
	fake.handlePrefix("GET /api/user-groups/", b2GroupDetailHandler(groups))
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
	fake.handle("GET /api/users", func(w http.ResponseWriter, r *http.Request) { b2Paginate(w, r, nil) })
	fake.handle("GET /api/oidc/clients", func(w http.ResponseWriter, r *http.Request) { b2Paginate(w, r, nil) })
	ds := b2Configure(t, datasources.NewGroupsDataSource(), fake.client())

	resp := b2Read(t, ds, nil)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var list types.List
	b2Attr(t, resp, "groups", &list)
	assert.Len(t, list.Elements(), 250)
}
