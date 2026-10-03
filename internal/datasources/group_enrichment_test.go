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

func b2SetStrings(t *testing.T, set types.Set) []string {
	t.Helper()
	require.False(t, set.IsNull(), "a set the data source reports is empty, never null")
	var out []string
	for _, element := range set.Elements() {
		out = append(out, strings.Trim(element.String(), `"`))
	}
	return out
}

func b2MapStrings(t *testing.T, m types.Map) map[string]string {
	t.Helper()
	require.False(t, m.IsNull(), "a map the data source reports is empty, never null")
	out := map[string]string{}
	for key, element := range m.Elements() {
		out[key] = strings.Trim(element.String(), `"`)
	}
	return out
}

// pocketid_group reports the group's claims, members, allowed clients and user
// count, read from the group's own record.
func TestGroupDataSource_Read_ReportsClaimsMembersAndClients(t *testing.T) {
	group := b2GroupJSON(5, "ops")
	group["customClaims"] = []any{map[string]any{"key": "tier", "value": "gold"}, map[string]any{"key": "region", "value": "eu"}}
	group["users"] = []any{map[string]any{"id": b2UUID(101)}, map[string]any{"id": b2UUID(102)}}
	group["allowedOidcClients"] = []any{map[string]any{"id": "grafana"}}
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups/"+b2UUID(5), func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, http.StatusOK, group) })
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"id": b2Str(b2UUID(5))})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	var claims types.Map
	var members, clients types.Set
	var count int64
	b2Attr(t, resp, "custom_claims", &claims)
	b2Attr(t, resp, "member_ids", &members)
	b2Attr(t, resp, "allowed_client_ids", &clients)
	b2Attr(t, resp, "user_count", &count)
	assert.Equal(t, map[string]string{"tier": "gold", "region": "eu"}, b2MapStrings(t, claims))
	assert.ElementsMatch(t, []string{b2UUID(101), b2UUID(102)}, b2SetStrings(t, members))
	assert.Equal(t, []string{"grafana"}, b2SetStrings(t, clients))
	assert.Equal(t, int64(2), count)
}

// A group with nothing attached reports empty collections and a zero count, so
// length() and for expressions work on them.
func TestGroupDataSource_Read_EmptyGroupHasEmptyCollections(t *testing.T) {
	group := b2GroupJSON(6, "empty")
	group["customClaims"] = nil
	group["users"] = nil
	group["allowedOidcClients"] = nil
	fake := newB2Fake(t)
	fake.handle("GET /api/user-groups/"+b2UUID(6), func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, http.StatusOK, group) })
	ds := b2Configure(t, datasources.NewGroupDataSource(), fake.client())

	resp := b2Read(t, ds, map[string]tftypes.Value{"id": b2Str(b2UUID(6))})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var claims types.Map
	var members, clients types.Set
	var count int64
	b2Attr(t, resp, "custom_claims", &claims)
	b2Attr(t, resp, "member_ids", &members)
	b2Attr(t, resp, "allowed_client_ids", &clients)
	b2Attr(t, resp, "user_count", &count)
	assert.Empty(t, b2MapStrings(t, claims))
	assert.Empty(t, b2SetStrings(t, members))
	assert.Empty(t, b2SetStrings(t, clients))
	assert.Equal(t, int64(0), count)
}

// b2GroupListFixture builds a list-endpoint view of n groups (claims and a
// member count, no members, no clients), a user list in which user i belongs
// to group i%n, and a client list in which client i allows group i%n.
type b2GroupListFixture struct {
	groups  []any
	users   []any
	clients []any
}

func newB2GroupListFixture(groups, users, clients int) b2GroupListFixture {
	var f b2GroupListFixture
	counts := make([]int, groups)
	for i := 0; i < users; i++ {
		counts[i%groups]++
	}
	for i := 0; i < groups; i++ {
		g := b2GroupJSON(i+1, fmt.Sprintf("team-%d", i+1))
		delete(g, "users")
		delete(g, "allowedOidcClients")
		g["userCount"] = counts[i]
		g["customClaims"] = []any{map[string]any{"key": "n", "value": fmt.Sprint(i + 1)}}
		f.groups = append(f.groups, g)
	}
	for i := 0; i < users; i++ {
		f.users = append(f.users, map[string]any{
			"id": b2UUID(1000 + i), "username": fmt.Sprintf("u%d", i), "email": fmt.Sprintf("u%d@example.com", i),
			"userGroups": []any{map[string]any{"id": b2UUID(i%groups + 1)}}, "customClaims": []any{},
		})
	}
	for i := 0; i < clients; i++ {
		f.clients = append(f.clients, map[string]any{
			"id": fmt.Sprintf("client-%d", i), "name": fmt.Sprintf("client %d", i),
			"allowedUserGroups": []any{map[string]any{"id": b2UUID(i%groups + 1)}},
		})
	}
	return f
}

func (f b2GroupListFixture) serve(fake *b2Fake) {
	fake.handle("GET /api/user-groups", func(w http.ResponseWriter, r *http.Request) { b2Paginate(w, r, f.groups) })
	fake.handle("GET /api/users", func(w http.ResponseWriter, r *http.Request) { b2Paginate(w, r, f.users) })
	fake.handle("GET /api/oidc/clients", func(w http.ResponseWriter, r *http.Request) { b2Paginate(w, r, f.clients) })
}

type b2GroupsRow struct {
	ID               string
	Name             string
	CustomClaims     map[string]string
	MemberIDs        []string
	AllowedClientIDs []string
	UserCount        int64
}

func b2ReadGroupsList(t *testing.T, fake *b2Fake) []b2GroupsRow {
	t.Helper()
	ds := b2Configure(t, datasources.NewGroupsDataSource(), fake.client())
	resp := b2Read(t, ds, nil)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var list types.List
	b2Attr(t, resp, "groups", &list)
	var rows []b2GroupsRow
	for _, element := range list.Elements() {
		object := element.(types.Object).Attributes()
		rows = append(rows, b2GroupsRow{
			ID:               strings.Trim(object["id"].String(), `"`),
			Name:             strings.Trim(object["name"].String(), `"`),
			CustomClaims:     b2MapStrings(t, object["custom_claims"].(types.Map)),
			MemberIDs:        b2SetStrings(t, object["member_ids"].(types.Set)),
			AllowedClientIDs: b2SetStrings(t, object["allowed_client_ids"].(types.Set)),
			UserCount:        object["user_count"].(types.Int64).ValueInt64(),
		})
	}
	return rows
}

// The list reports members and allowed clients for every group from one pass
// over the users and one over the clients, not one request per group. 250
// users make three user pages, so a member on the last page must still count.
func TestGroupsDataSource_Read_BuildsRelationsFromBulkLists(t *testing.T) {
	f := newB2GroupListFixture(7, 250, 9)
	fake := newB2Fake(t)
	f.serve(fake)

	rows := b2ReadGroupsList(t, fake)
	require.Len(t, rows, 7)
	totalMembers, totalClients := 0, 0
	for i, row := range rows {
		assert.Equal(t, fmt.Sprintf("team-%d", i+1), row.Name)
		assert.Equal(t, map[string]string{"n": fmt.Sprint(i + 1)}, row.CustomClaims)
		assert.Len(t, row.MemberIDs, int(row.UserCount), "member_ids agrees with the list's user_count")
		totalMembers += len(row.MemberIDs)
		totalClients += len(row.AllowedClientIDs)
	}
	assert.Equal(t, 250, totalMembers)
	assert.Equal(t, 9, totalClients)
	assert.Contains(t, rows[0].MemberIDs, b2UUID(1000), "the first user belongs to the first group")
	assert.Contains(t, rows[(249)%7].MemberIDs, b2UUID(1249), "the last user, on the third page, is counted")
	assert.Contains(t, rows[1].AllowedClientIDs, "client-1")

	assert.Zero(t, fake.count("GET /api/user-groups/"), "no group is read on its own")
	assert.Equal(t, 3, fake.count("GET /api/users"))
	assert.Equal(t, 1, fake.count("GET /api/oidc/clients"))
}

// A group nobody belongs to and no client allows has empty sets.
func TestGroupsDataSource_Read_GroupWithoutRelations(t *testing.T) {
	f := newB2GroupListFixture(3, 1, 0)
	fake := newB2Fake(t)
	f.serve(fake)

	rows := b2ReadGroupsList(t, fake)
	require.Len(t, rows, 3)
	assert.Equal(t, []string{b2UUID(1000)}, rows[0].MemberIDs)
	assert.Empty(t, rows[1].MemberIDs)
	assert.Empty(t, rows[1].AllowedClientIDs)
	assert.Equal(t, int64(0), rows[1].UserCount)
}

// Pocket ID 2.14's client list carries a count of allowed groups, not the
// groups. The allowed clients are then read group by group; members still come
// from the user list.
func TestGroupsDataSource_Read_FallsBackToPerGroupReadsOnServers214(t *testing.T) {
	f := newB2GroupListFixture(4, 5, 0)
	f.clients = []any{map[string]any{"id": "client-a", "name": "a", "allowedUserGroupsCount": 1}}
	fake := newB2Fake(t)
	f.serve(fake)
	fake.handlePrefix("GET /api/user-groups/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/user-groups/")
		group := b2GroupJSON(1, "x")
		group["id"] = id
		group["allowedOidcClients"] = []any{}
		if id == b2UUID(2) {
			group["allowedOidcClients"] = []any{map[string]any{"id": "client-a"}}
		}
		b2JSON(w, http.StatusOK, group)
	})

	rows := b2ReadGroupsList(t, fake)
	require.Len(t, rows, 4)
	assert.Equal(t, []string{"client-a"}, rows[1].AllowedClientIDs)
	assert.Empty(t, rows[0].AllowedClientIDs)
	assert.Equal(t, 4, fake.count("GET /api/user-groups/"), "one read per group, and only there")
	assert.Equal(t, 1, fake.count("GET /api/users"))
}

func TestGroupsDataSource_Read_ReportsAFailedBulkRead(t *testing.T) {
	f := newB2GroupListFixture(2, 1, 0)
	fake := newB2Fake(t)
	f.serve(fake)
	fake.handle("GET /api/users", func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, 403, map[string]any{"error": "no"}) })
	ds := b2Configure(t, datasources.NewGroupsDataSource(), fake.client())

	resp := b2Read(t, ds, nil)
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, []string{"Unable to Read Group Members"}, b2Summaries(resp))
}

// pocketid_user and pocketid_users report custom claims; no claims is an empty
// map.
func TestUserDataSources_Read_ReportCustomClaims(t *testing.T) {
	users := []any{
		map[string]any{"id": b2UUID(1), "username": "alice", "email": "alice@example.com", "userGroups": nil,
			"customClaims": []any{map[string]any{"key": "dept", "value": "ops"}}},
		map[string]any{"id": b2UUID(2), "username": "bob", "email": "bob@example.com", "userGroups": nil, "customClaims": nil},
	}
	fake := newB2Fake(t)
	fake.handle("GET /api/users", func(w http.ResponseWriter, r *http.Request) { b2Paginate(w, r, users) })
	fake.handle("GET /api/users/"+b2UUID(1), func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, 200, users[0]) })
	c := fake.client()

	single := b2Read(t, b2Configure(t, datasources.NewUserDataSource(), c), map[string]tftypes.Value{"id": b2Str(b2UUID(1))})
	require.False(t, single.Diagnostics.HasError(), "%v", single.Diagnostics)
	var claims types.Map
	b2Attr(t, single, "custom_claims", &claims)
	assert.Equal(t, map[string]string{"dept": "ops"}, b2MapStrings(t, claims))

	list := b2Read(t, b2Configure(t, datasources.NewUsersDataSource(), c), nil)
	require.False(t, list.Diagnostics.HasError(), "%v", list.Diagnostics)
	var rows types.List
	b2Attr(t, list, "users", &rows)
	require.Len(t, rows.Elements(), 2)
	first := rows.Elements()[0].(types.Object).Attributes()["custom_claims"].(types.Map)
	second := rows.Elements()[1].(types.Object).Attributes()["custom_claims"].(types.Map)
	assert.Equal(t, map[string]string{"dept": "ops"}, b2MapStrings(t, first))
	assert.Empty(t, b2MapStrings(t, second))
}

// A user without an email address reads as null, not as an empty string.
func TestUserDataSources_Read_UserWithoutEmailHasNullEmail(t *testing.T) {
	user := map[string]any{"id": b2UUID(3), "username": "carol", "email": nil, "userGroups": nil, "customClaims": nil}
	fake := newB2Fake(t)
	fake.handle("GET /api/users", func(w http.ResponseWriter, r *http.Request) { b2Paginate(w, r, []any{user}) })
	fake.handle("GET /api/users/"+b2UUID(3), func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, 200, user) })
	c := fake.client()

	single := b2Read(t, b2Configure(t, datasources.NewUserDataSource(), c), map[string]tftypes.Value{"id": b2Str(b2UUID(3))})
	require.False(t, single.Diagnostics.HasError(), "%v", single.Diagnostics)
	var email types.String
	b2Attr(t, single, "email", &email)
	assert.True(t, email.IsNull())

	list := b2Read(t, b2Configure(t, datasources.NewUsersDataSource(), c), nil)
	require.False(t, list.Diagnostics.HasError(), "%v", list.Diagnostics)
	var rows types.List
	b2Attr(t, list, "users", &rows)
	require.Len(t, rows.Elements(), 1)
	assert.True(t, rows.Elements()[0].(types.Object).Attributes()["email"].(types.String).IsNull())
}
