package datasources_test

import (
	"net/http"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/datasources"
)

func b2PasskeyJSON(id, name, created string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "createdAt": created,
		"credentialID": "c2VjcmV0LWNyZWRlbnRpYWwtaWQ=", "attestationType": "none",
		"transport": []string{"internal", "hybrid"}, "backupEligible": true, "backupState": false,
		"aaguid": "adce0002-35bc-c60a-648b-0b25f1f05503", "hasIcon": true,
	}
}

func b2ReadPasskeys(t *testing.T, items []any) (rows []map[string]any, fake *b2Fake) {
	t.Helper()
	fake = newB2Fake(t)
	fake.handle("GET /api/users/"+b2UUID(1)+"/webauthn-credentials", func(w http.ResponseWriter, _ *http.Request) {
		b2JSON(w, http.StatusOK, items)
	})
	ds := b2Configure(t, datasources.NewUserPasskeysDataSource(), fake.client())
	resp := b2Read(t, ds, map[string]tftypes.Value{"user_id": b2Str(b2UUID(1))})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var list types.List
	b2Attr(t, resp, "passkeys", &list)
	for _, element := range list.Elements() {
		attrs := element.(types.Object).Attributes()
		row := map[string]any{}
		for name, value := range attrs {
			switch v := value.(type) {
			case types.String:
				if v.IsNull() {
					row[name] = nil
				} else {
					row[name] = v.ValueString()
				}
			case types.Bool:
				row[name] = v.ValueBool()
			case types.Set:
				row[name] = b2SetStrings(t, v)
			}
		}
		rows = append(rows, row)
	}
	return rows, fake
}

// Passkeys read oldest first whatever order the server answers in (it sorts
// nothing), with their backup flags and transports, and with no credential
// material: the schema has no attribute for the credential ID.
func TestUserPasskeysDataSource_Read(t *testing.T) {
	newer := b2PasskeyJSON("p-new", "Phone", "2026-03-01T00:00:00Z")
	older := b2PasskeyJSON("p-old", "Laptop", "2026-01-01T00:00:00Z")
	older["backupState"] = true
	rows, fake := b2ReadPasskeys(t, []any{newer, older})

	require.Len(t, rows, 2)
	assert.Equal(t, "p-old", rows[0]["id"])
	assert.Equal(t, "Laptop", rows[0]["name"])
	assert.Equal(t, "2026-01-01T00:00:00Z", rows[0]["created_at"])
	assert.Equal(t, true, rows[0]["backup_eligible"])
	assert.Equal(t, true, rows[0]["backup_state"])
	assert.ElementsMatch(t, []string{"internal", "hybrid"}, rows[0]["transports"])
	assert.Equal(t, "adce0002-35bc-c60a-648b-0b25f1f05503", rows[0]["aaguid"])
	assert.Equal(t, "p-new", rows[1]["id"])
	assert.Equal(t, false, rows[1]["backup_state"])
	assert.Len(t, fake.log(), 1)

	var names []string
	for name := range rows[0] {
		names = append(names, name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"aaguid", "backup_eligible", "backup_state", "created_at", "id", "name", "transports"}, names)
}

func TestUserPasskeysDataSource_Read_NoAuthenticatorModel(t *testing.T) {
	older := b2PasskeyJSON("p-1", "Key", "2026-01-01T00:00:00Z")
	delete(older, "aaguid") // Pocket ID 2.14 does not report it
	zero := b2PasskeyJSON("p-2", "Other", "2026-01-02T00:00:00Z")
	zero["aaguid"] = "00000000-0000-0000-0000-000000000000"
	rows, _ := b2ReadPasskeys(t, []any{older, zero})
	require.Len(t, rows, 2)
	assert.Nil(t, rows[0]["aaguid"])
	assert.Nil(t, rows[1]["aaguid"], "the all-zero identifier means none was reported")
}

func TestUserPasskeysDataSource_Read_NoPasskeys(t *testing.T) {
	ds := b2Configure(t, datasources.NewUserPasskeysDataSource(), func() *b2Fake {
		f := newB2Fake(t)
		f.handle("GET /api/users/"+b2UUID(1)+"/webauthn-credentials", func(w http.ResponseWriter, _ *http.Request) {
			b2JSON(w, http.StatusOK, []any{})
		})
		return f
	}().client())
	resp := b2Read(t, ds, map[string]tftypes.Value{"user_id": b2Str(b2UUID(1))})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var list types.List
	b2Attr(t, resp, "passkeys", &list)
	assert.False(t, list.IsNull(), "no passkeys is an empty list")
	assert.Empty(t, list.Elements())
}

func TestUserPasskeysDataSource_Read_Failures(t *testing.T) {
	cases := map[string]struct {
		handler http.HandlerFunc
		userID  string
		summary string
	}{
		"missing user": {func(w http.ResponseWriter, _ *http.Request) { b2NotFound(w, "user") }, b2UUID(1), "User Not Found"},
		"other 404": {func(w http.ResponseWriter, _ *http.Request) {
			b2JSON(w, 404, map[string]any{"error": "API endpoint not found"})
		}, b2UUID(1), "Unable to Read Passkeys"},
		"refused":    {func(w http.ResponseWriter, _ *http.Request) { b2JSON(w, 403, map[string]any{"error": "no"}) }, b2UUID(1), "Unable to Read Passkeys"},
		"not a UUID": {func(http.ResponseWriter, *http.Request) {}, "someone", "Unable to Read Passkeys"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newB2Fake(t)
			fake.handle("GET /api/users/"+b2UUID(1)+"/webauthn-credentials", tc.handler)
			ds := b2Configure(t, datasources.NewUserPasskeysDataSource(), fake.client())
			resp := b2Read(t, ds, map[string]tftypes.Value{"user_id": b2Str(tc.userID)})
			require.True(t, resp.Diagnostics.HasError())
			assert.Equal(t, []string{tc.summary}, b2Summaries(resp))
			if tc.userID == "someone" {
				assert.Empty(t, fake.log(), "no request for an ID that is not a UUID")
			}
		})
	}
}

func TestUserPasskeysDataSource_Schema(t *testing.T) {
	sch := b2Schema(t, datasources.NewUserPasskeysDataSource())
	userID, ok := sch.Attributes["user_id"].(schema.StringAttribute)
	require.True(t, ok)
	assert.True(t, userID.Required)
	assert.NotEmpty(t, userID.Validators, "a user_id that is not a UUID is refused at plan time")
	assert.True(t, sch.Attributes["passkeys"].IsComputed())
}
