package resources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
	"github.com/irashack/terraform-provider-pocketid/internal/resources"
)

const (
	signupTestTokenID = "55555555-5555-4555-8555-555555555555"
	signupTestGroupA  = "66666666-6666-4666-8666-666666666666"
	signupTestGroupB  = "77777777-7777-4777-8777-777777777777"
	signupTestSecret  = "signup-secret-value-0123"
)

// signupFake is a Pocket ID stand-in for the signup token routes. It behaves
// like the real server where it matters here: it drops group IDs that name no
// group, lists only the tokens it holds (an expired token is simply gone),
// and answers 204 to a delete whether or not the token exists.
type signupFake struct {
	mu       sync.Mutex
	groups   map[string]bool // groups that exist
	tokens   []map[string]any
	requests []scimRequest
	// failWith answers every request with this status and body.
	failWith *scimFailure
	// createBody, when set, replaces the answer to a create.
	createBody string
	nextID     int
	// failDelete answers a delete with this failure; keepOnDelete makes a
	// delete answer 204 without removing anything; failList answers the list.
	failDelete   *scimFailure
	keepOnDelete bool
	failList     *scimFailure
	// attachExtra lists groups the fake attaches although they were not
	// requested; usageLimitOverride makes it set a different usage limit.
	attachExtra        []string
	usageLimitOverride *int
}

func newSignupFake(t *testing.T) (*signupFake, *client.Client) {
	t.Helper()
	fake := &signupFake{groups: map[string]bool{signupTestGroupA: true, signupTestGroupB: true}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return fake, c
}

func (f *signupFake) addToken(id, token string, usageCount int, groups ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, f.tokenJSON(id, token, 3, usageCount, groups))
}

func (f *signupFake) tokenJSON(id, token string, limit, count int, groups []string) map[string]any {
	userGroups := []map[string]any{}
	for _, g := range groups {
		userGroups = append(userGroups, map[string]any{"id": g, "name": "g-" + g[:2], "friendlyName": "Group"})
	}
	return map[string]any{
		"id": id, "token": token, "expiresAt": "2026-10-03T10:00:00Z", "usageLimit": limit, "usageCount": count,
		"userGroups": userGroups, "createdAt": "2026-10-02T10:00:00Z",
	}
}

func (f *signupFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := scimRequest{Method: r.Method, Path: r.URL.Path}
	if r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			record.Body = body
		}
	}
	f.requests = append(f.requests, record)
	w.Header().Set("Content-Type", "application/json")
	if f.failWith != nil {
		w.WriteHeader(f.failWith.Status)
		_, _ = w.Write([]byte(f.failWith.Body))
		return
	}
	if r.Method == http.MethodDelete && f.failDelete != nil {
		w.WriteHeader(f.failDelete.Status)
		_, _ = w.Write([]byte(f.failDelete.Body))
		return
	}
	if r.Method == http.MethodGet && f.failList != nil {
		w.WriteHeader(f.failList.Status)
		_, _ = w.Write([]byte(f.failList.Body))
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/signup-tokens":
		if f.createBody != "" {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(f.createBody))
			return
		}
		var kept []string
		if ids, ok := record.Body["userGroupIds"].([]any); ok {
			for _, id := range ids {
				if s, _ := id.(string); f.groups[s] {
					kept = append(kept, s)
				}
			}
		}
		kept = append(kept, f.attachExtra...)
		f.nextID++
		id := fmt.Sprintf("55555555-5555-4555-8555-%012d", f.nextID)
		limit, _ := record.Body["usageLimit"].(float64)
		if f.usageLimitOverride != nil {
			limit = float64(*f.usageLimitOverride)
		}
		tok := f.tokenJSON(id, signupTestSecret, int(limit), 0, kept)
		f.tokens = append(f.tokens, tok)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(tok)
	case r.Method == http.MethodGet && r.URL.Path == "/api/signup-tokens":
		data := f.tokens
		if data == nil {
			data = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       data,
			"pagination": map[string]any{"totalPages": 1, "totalItems": len(data), "currentPage": 1, "itemsPerPage": 100},
		})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/signup-tokens/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/signup-tokens/")
		for i, tok := range f.tokens {
			if tok["id"] == id && !f.keepOnDelete {
				f.tokens = append(f.tokens[:i], f.tokens[i+1:]...)
				break
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(scimMissingRouteBody))
	}
}

func (f *signupFake) recorded() []scimRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scimRequest(nil), f.requests...)
}

func (f *signupFake) mutations() []scimRequest {
	var out []scimRequest
	for _, r := range f.recorded() {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

func signupResource(t *testing.T, c *client.Client) (resource.Resource, schema.Schema) {
	t.Helper()
	r := resources.NewSignupTokenResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	return r, scimSchema(t, r)
}

func signupSet(ids ...string) []tftypes.Value {
	out := make([]tftypes.Value, 0, len(ids))
	for _, id := range ids {
		out = append(out, tftypes.NewValue(tftypes.String, id))
	}
	return out
}

// signupPlan is the plan Terraform hands Create: configured values known,
// computed ones unknown.
func signupPlan(groups []tftypes.Value, limit int) map[string]any {
	plan := map[string]any{
		"ttl": "24h", "usage_limit": limit,
		"id": tftypes.UnknownValue, "token": tftypes.UnknownValue, "expires_at": tftypes.UnknownValue,
		"created_at": tftypes.UnknownValue, "usage_count": tftypes.UnknownValue, "expired": tftypes.UnknownValue,
	}
	if groups != nil {
		plan["user_group_ids"] = groups
	}
	return plan
}

func signupCreate(t *testing.T, r resource.Resource, sch schema.Schema, plan map[string]any) *resource.CreateResponse {
	t.Helper()
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: scimObject(t, sch, plan)}}, resp)
	return resp
}

func signupStored(id string, groups []tftypes.Value, expired bool) map[string]any {
	state := map[string]any{
		"id": id, "ttl": "24h", "usage_limit": 3, "token": signupTestSecret,
		"expires_at": "2026-10-03T10:00:00Z", "created_at": "2026-10-02T10:00:00Z", "usage_count": 0, "expired": expired,
	}
	if groups != nil {
		state["user_group_ids"] = groups
	}
	return state
}

func signupAttr(t *testing.T, state tfsdk.State, name string, target any) {
	t.Helper()
	require.False(t, state.GetAttribute(context.Background(), path.Root(name), target).HasError())
}

func TestSignupTokenResource_Schema(t *testing.T) {
	r := resources.NewSignupTokenResource()
	sch := scimSchema(t, r)

	token, ok := sch.Attributes["token"].(schema.StringAttribute)
	require.True(t, ok)
	assert.True(t, token.Computed)
	assert.True(t, token.Sensitive, "the token value is a secret")

	for _, name := range []string{"ttl", "usage_limit", "user_group_ids"} {
		attr := sch.Attributes[name]
		assert.True(t, attr.IsOptional(), name)
		switch a := attr.(type) {
		case schema.StringAttribute:
			assert.NotEmpty(t, a.PlanModifiers, "%s must force replacement", name)
		case schema.Int64Attribute:
			assert.NotEmpty(t, a.PlanModifiers, "%s must force replacement", name)
		case schema.SetAttribute:
			assert.NotEmpty(t, a.PlanModifiers, "%s must force replacement", name)
		}
	}
	for _, name := range []string{"id", "expires_at", "created_at", "usage_count", "expired"} {
		assert.True(t, sch.Attributes[name].IsComputed(), name)
		assert.False(t, sch.Attributes[name].IsOptional(), name)
	}
	_, hasImport := resources.NewSignupTokenResource().(resource.ResourceWithImportState)
	assert.False(t, hasImport, "a token's value is not recoverable, so there is no import")
}

func TestSignupTokenResource_CreateSendsTheRequestAndRecordsTheToken(t *testing.T) {
	fake, c := newSignupFake(t)
	r, sch := signupResource(t, c)

	resp := signupCreate(t, r, sch, signupPlan(signupSet(signupTestGroupA, signupTestGroupB), 3))
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

	mutations := fake.mutations()
	require.Len(t, mutations, 1)
	assert.Equal(t, "24h", mutations[0].Body["ttl"])
	assert.Equal(t, float64(3), mutations[0].Body["usageLimit"])
	assert.ElementsMatch(t, []any{signupTestGroupA, signupTestGroupB}, mutations[0].Body["userGroupIds"])

	var id, token types.String
	var expired types.Bool
	var limit, count types.Int64
	var groups types.Set
	signupAttr(t, resp.State, "id", &id)
	signupAttr(t, resp.State, "token", &token)
	signupAttr(t, resp.State, "expired", &expired)
	signupAttr(t, resp.State, "usage_limit", &limit)
	signupAttr(t, resp.State, "usage_count", &count)
	signupAttr(t, resp.State, "user_group_ids", &groups)
	assert.Equal(t, "55555555-5555-4555-8555-000000000001", id.ValueString())
	assert.Equal(t, signupTestSecret, token.ValueString())
	assert.False(t, expired.ValueBool())
	assert.EqualValues(t, 3, limit.ValueInt64())
	assert.EqualValues(t, 0, count.ValueInt64())
	assert.Len(t, groups.Elements(), 2)
}

func TestSignupTokenResource_CreateWithoutGroupsKeepsTheSetNullAndSendsAnEmptyList(t *testing.T) {
	fake, c := newSignupFake(t)
	r, sch := signupResource(t, c)

	resp := signupCreate(t, r, sch, signupPlan(nil, 1))
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []any{}, fake.mutations()[0].Body["userGroupIds"])
	var groups types.Set
	signupAttr(t, resp.State, "user_group_ids", &groups)
	assert.True(t, groups.IsNull())
}

// Pocket ID drops IDs that name no group. The token it made is a live
// registration credential with the wrong groups, so the provider deletes it
// again, confirms against the list that it is gone, and only then records
// nothing. The diagnostic names the ignored IDs and never the token.
func TestSignupTokenResource_CreateDeletesATokenWhoseGroupPocketIDIgnored(t *testing.T) {
	fake, c := newSignupFake(t)
	r, sch := signupResource(t, c)
	const unknown = "88888888-8888-4888-8888-888888888888"

	resp := signupCreate(t, r, sch, signupPlan(signupSet(signupTestGroupA, unknown), 1))
	require.True(t, resp.Diagnostics.HasError())

	mutations := fake.mutations()
	require.Len(t, mutations, 2, "one creation and one deletion, nothing repeated")
	assert.Equal(t, http.MethodPost, mutations[0].Method)
	assert.Equal(t, http.MethodDelete, mutations[1].Method)
	assert.Equal(t, "/api/signup-tokens/55555555-5555-4555-8555-000000000001", mutations[1].Path)
	assert.Empty(t, fake.tokens, "the token must be gone from Pocket ID")
	assert.True(t, resp.State.Raw.IsNull(), "a confirmed deletion leaves nothing in state")

	detail := signupDiagnosticText(resp)
	assert.Contains(t, detail, unknown)
	assert.NotContains(t, detail, signupTestGroupA)
	assert.NotContains(t, detail, signupTestSecret)
	assert.Contains(t, detail, "deleted again")
}

func signupDiagnosticText(resp *resource.CreateResponse) string {
	var text string
	for _, d := range resp.Diagnostics {
		text += d.Summary() + " " + d.Detail() + " "
	}
	return text
}

// When the deletion cannot be confirmed, the token may still be valid: its ID
// and value stay in state (tainted by the error) so the next apply deletes it,
// and the diagnostic says the cleanup failed.
func TestSignupTokenResource_CreateKeepsATokenWhoseCleanupCannotBeConfirmed(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*signupFake)
		want  string
	}{
		{"the deletion fails", func(f *signupFake) { f.failDelete = &scimFailure{500, `{"error":"boom signup-secret-value-0123"}`} }, "HTTP 500"},
		{"the deletion is refused", func(f *signupFake) { f.failDelete = &scimFailure{403, `{"error":"no"}`} }, "HTTP 403"},
		{"Pocket ID still lists the token", func(f *signupFake) { f.keepOnDelete = true }, "still listed"},
		{"the confirming list cannot be read", func(f *signupFake) { f.failList = &scimFailure{503, `{"error":"later"}`} }, "could not be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, c := newSignupFake(t)
			tc.setup(fake)
			r, sch := signupResource(t, c)
			const unknown = "88888888-8888-4888-8888-888888888888"

			resp := signupCreate(t, r, sch, signupPlan(signupSet(signupTestGroupA, unknown), 1))
			require.True(t, resp.Diagnostics.HasError())

			var deletes int
			for _, m := range fake.mutations() {
				if m.Method == http.MethodDelete {
					deletes++
				}
			}
			assert.Equal(t, 1, deletes, "exactly one attempt to delete")

			require.False(t, resp.State.Raw.IsNull(), "an unconfirmed cleanup keeps the token in state")
			var id, token types.String
			signupAttr(t, resp.State, "id", &id)
			signupAttr(t, resp.State, "token", &token)
			assert.NotEmpty(t, id.ValueString())
			assert.Equal(t, signupTestSecret, token.ValueString())

			detail := signupDiagnosticText(resp)
			assert.Contains(t, detail, "could not be confirmed")
			assert.Contains(t, detail, "tainted")
			assert.Contains(t, detail, tc.want)
			assert.Contains(t, detail, unknown)
			assert.NotContains(t, detail, signupTestSecret)
		})
	}
}

// The other two ways a created token can differ from the request are cleaned
// up the same way as an ignored group: groups nobody asked for would widen
// who a registration joins, and a different usage limit would let more
// people register than was asked for.
func TestSignupTokenResource_CreateDeletesATokenWithGroupsOrALimitNobodyAskedFor(t *testing.T) {
	override := 5
	cases := []struct {
		name          string
		setup         func(*signupFake)
		groups        []tftypes.Value
		wantInMessage string
		wantStateSets []string // the groups the recorded state shows when cleanup fails
		wantLimit     int64
	}{
		{
			"an extra group",
			func(f *signupFake) { f.attachExtra = []string{signupTestGroupB} },
			signupSet(signupTestGroupA),
			"attached groups that were not requested: " + signupTestGroupB,
			[]string{signupTestGroupA, signupTestGroupB}, 3,
		},
		{
			"an extra group when none was requested",
			func(f *signupFake) { f.attachExtra = []string{signupTestGroupB} },
			nil,
			"attached groups that were not requested: " + signupTestGroupB,
			[]string{signupTestGroupB}, 3,
		},
		{
			"a different usage limit",
			func(f *signupFake) { f.usageLimitOverride = &override },
			signupSet(signupTestGroupA),
			"set a usage limit of 5, not the requested value",
			[]string{signupTestGroupA}, 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name+", cleanup confirmed", func(t *testing.T) {
			fake, c := newSignupFake(t)
			tc.setup(fake)
			r, sch := signupResource(t, c)

			resp := signupCreate(t, r, sch, signupPlan(tc.groups, 3))
			require.True(t, resp.Diagnostics.HasError())

			mutations := fake.mutations()
			require.Len(t, mutations, 2, "one creation and one deletion")
			assert.Equal(t, http.MethodDelete, mutations[1].Method)
			assert.Empty(t, fake.tokens, "the token must be gone from Pocket ID")
			assert.True(t, resp.State.Raw.IsNull())
			detail := signupDiagnosticText(resp)
			assert.Contains(t, detail, tc.wantInMessage)
			assert.Contains(t, detail, "deleted again")
			assert.NotContains(t, detail, signupTestSecret)
		})
		t.Run(tc.name+", cleanup not confirmed", func(t *testing.T) {
			fake, c := newSignupFake(t)
			tc.setup(fake)
			fake.failDelete = &scimFailure{500, `{"error":"boom"}`}
			r, sch := signupResource(t, c)

			resp := signupCreate(t, r, sch, signupPlan(tc.groups, 3))
			require.True(t, resp.Diagnostics.HasError())

			var deletes int
			for _, m := range fake.mutations() {
				if m.Method == http.MethodDelete {
					deletes++
				}
			}
			assert.Equal(t, 1, deletes, "exactly one attempt to delete")
			require.False(t, resp.State.Raw.IsNull(), "an unconfirmed cleanup keeps the token in state")
			var groups types.Set
			var limit types.Int64
			signupAttr(t, resp.State, "user_group_ids", &groups)
			signupAttr(t, resp.State, "usage_limit", &limit)
			assert.Equal(t, tc.wantStateSets, signupSetStrings(t, groups), "the state shows what the token really has")
			assert.Equal(t, tc.wantLimit, limit.ValueInt64())
			detail := signupDiagnosticText(resp)
			assert.Contains(t, detail, tc.wantInMessage)
			assert.Contains(t, detail, "could not be confirmed")
			assert.Contains(t, detail, "tainted")
			assert.NotContains(t, detail, signupTestSecret)
		})
	}
}

func signupSetStrings(t *testing.T, set types.Set) []string {
	t.Helper()
	var out []string
	for _, e := range set.Elements() {
		s, ok := e.(types.String)
		require.True(t, ok)
		out = append(out, s.ValueString())
	}
	sort.Strings(out)
	return out
}

// An answer with a usable ID but no token value is a token nobody can hand
// out; it is deleted again like any other token that is not as requested.
func TestSignupTokenResource_CreateDeletesATokenWhoseAnswerHadNoValue(t *testing.T) {
	fake, c := newSignupFake(t)
	fake.createBody = fmt.Sprintf(`{"id":%q,"usageLimit":1,"userGroups":[]}`, signupTestTokenID)
	r, sch := signupResource(t, c)

	resp := signupCreate(t, r, sch, signupPlan(nil, 1))
	require.True(t, resp.Diagnostics.HasError())
	mutations := fake.mutations()
	require.Len(t, mutations, 2)
	assert.Equal(t, http.MethodDelete, mutations[1].Method)
	assert.Equal(t, "/api/signup-tokens/"+signupTestTokenID, mutations[1].Path)
	assert.True(t, resp.State.Raw.IsNull())
	assert.Contains(t, signupDiagnosticText(resp), "did not carry the token value")
}

func TestSignupTokenResource_CreateKeepsTheIDOfATokenWithNoValueWhenCleanupFails(t *testing.T) {
	fake, c := newSignupFake(t)
	fake.createBody = fmt.Sprintf(`{"id":%q,"usageLimit":1,"userGroups":[]}`, signupTestTokenID)
	fake.failDelete = &scimFailure{500, `{"error":"boom"}`}
	r, sch := signupResource(t, c)

	resp := signupCreate(t, r, sch, signupPlan(nil, 1))
	require.True(t, resp.Diagnostics.HasError())
	require.False(t, resp.State.Raw.IsNull(), "a token that may still exist is recorded by its ID")
	var id types.String
	signupAttr(t, resp.State, "id", &id)
	assert.Equal(t, signupTestTokenID, id.ValueString())
	assert.Contains(t, signupDiagnosticText(resp), "could not be confirmed")
}

func TestSignupTokenResource_CreateFailuresAreReportedOnceAndRecordNothing(t *testing.T) {
	cases := []struct {
		name         string
		failure      *scimFailure
		createBody   string
		mayHaveBeen  bool
		wantNoSecret string
	}{
		{"a server error", &scimFailure{500, `{"error":"signup-secret-value-0123"}`}, "", true, signupTestSecret},
		{"a rate limit", &scimFailure{429, `{"error":"slow down"}`}, "", false, ""},
		{"a rejected request", &scimFailure{400, `{"error":"bad ttl","code":"invalid_request"}`}, "", false, ""},
		{"an answer without an ID", nil, `{"token":"signup-secret-value-0123"}`, true, signupTestSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, c := newSignupFake(t)
			fake.failWith = tc.failure
			fake.createBody = tc.createBody
			r, sch := signupResource(t, c)

			resp := signupCreate(t, r, sch, signupPlan(nil, 1))
			require.True(t, resp.Diagnostics.HasError())
			assert.True(t, resp.State.Raw.IsNull())
			assert.Len(t, fake.recorded(), 1, "a creation is never repeated")
			detail := resp.Diagnostics.Errors()[0].Detail()
			assert.Equal(t, tc.mayHaveBeen, strings.Contains(detail, "may have been created"), detail)
			assert.NotContains(t, detail, signupTestSecret)
		})
	}
}

func TestSignupTokenResource_CreateRefusesABadTTLWithoutARequest(t *testing.T) {
	for _, ttl := range []string{"", "soon", "1s", "745h"} {
		fake, c := newSignupFake(t)
		r, sch := signupResource(t, c)
		plan := signupPlan(nil, 1)
		plan["ttl"] = ttl
		resp := signupCreate(t, r, sch, plan)
		assert.True(t, resp.Diagnostics.HasError(), "ttl %q", ttl)
		assert.Empty(t, fake.recorded())
	}
}

func TestSignupTokenResource_ReadRefreshesAListedToken(t *testing.T) {
	fake, c := newSignupFake(t)
	fake.addToken(signupTestTokenID, signupTestSecret, 2, signupTestGroupA)
	r, sch := signupResource(t, c)

	state := scimState(t, sch, signupStored(signupTestTokenID, signupSet(signupTestGroupA), false))
	resp := scimRead(t, r, state)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var count types.Int64
	var expired types.Bool
	signupAttr(t, resp.State, "usage_count", &count)
	signupAttr(t, resp.State, "expired", &expired)
	assert.EqualValues(t, 2, count.ValueInt64())
	assert.False(t, expired.ValueBool())
}

// A token Pocket ID no longer lists (expired and purged, or deleted) stays in
// state marked expired. Dropping it would plan a new token, minting a
// registration credential nobody asked for.
func TestSignupTokenResource_ReadKeepsATokenThatIsNoLongerListed(t *testing.T) {
	fake, c := newSignupFake(t)
	fake.addToken("99999999-9999-4999-8999-999999999999", "another-token", 0) // some other token
	r, sch := signupResource(t, c)

	before := scimState(t, sch, signupStored(signupTestTokenID, signupSet(signupTestGroupA), false))
	resp := scimRead(t, r, before)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	require.False(t, resp.State.Raw.IsNull(), "an expired token must not be dropped from state")

	var expired types.Bool
	var token types.String
	var groups types.Set
	var limit types.Int64
	signupAttr(t, resp.State, "expired", &expired)
	signupAttr(t, resp.State, "token", &token)
	signupAttr(t, resp.State, "user_group_ids", &groups)
	signupAttr(t, resp.State, "usage_limit", &limit)
	assert.True(t, expired.ValueBool())
	// Everything an input depends on is unchanged, so a plan has nothing to replace.
	assert.Equal(t, signupTestSecret, token.ValueString())
	assert.Equal(t, []string{signupTestGroupA}, signupSetStrings(t, groups))
	assert.EqualValues(t, 3, limit.ValueInt64())
	var ttl types.String
	signupAttr(t, resp.State, "ttl", &ttl)
	assert.Equal(t, "24h", ttl.ValueString())

	// A second refresh is stable.
	again := scimRead(t, r, resp.State)
	require.False(t, again.Diagnostics.HasError())
	assert.True(t, resp.State.Raw.Equal(again.State.Raw))
}

func TestSignupTokenResource_ReadFailureIsAnErrorAndKeepsState(t *testing.T) {
	for _, failure := range []scimFailure{{403, `{"error":"no","code":"missing_permission"}`}, {404, scimNotFoundBody}, {404, scimMissingRouteBody}} {
		fake, c := newSignupFake(t)
		failed := failure
		fake.failWith = &failed
		r, sch := signupResource(t, c)

		resp := scimRead(t, r, scimState(t, sch, signupStored(signupTestTokenID, nil, false)))
		assert.True(t, resp.Diagnostics.HasError(), "status %d", failure.Status)
		assert.False(t, resp.State.Raw.IsNull(), "an unread list says nothing about the token (status %d)", failure.Status)
	}
}

func TestSignupTokenResource_DeleteDeletesTheToken(t *testing.T) {
	fake, c := newSignupFake(t)
	fake.addToken(signupTestTokenID, signupTestSecret, 0)
	r, sch := signupResource(t, c)

	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: scimState(t, sch, signupStored(signupTestTokenID, nil, false))}, resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	mutations := fake.mutations()
	require.Len(t, mutations, 1)
	assert.Equal(t, http.MethodDelete, mutations[0].Method)
	assert.Equal(t, "/api/signup-tokens/"+signupTestTokenID, mutations[0].Path)
}

// Pocket ID answers 204 for a token that is gone (expired and purged), so
// destroying an expired token succeeds.
func TestSignupTokenResource_DeleteOfAnExpiredTokenSucceeds(t *testing.T) {
	fake, c := newSignupFake(t)
	r, sch := signupResource(t, c)

	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: scimState(t, sch, signupStored(signupTestTokenID, nil, true))}, resp)
	assert.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Len(t, fake.mutations(), 1)
}

func TestSignupTokenResource_DeleteErrorsAreErrors(t *testing.T) {
	for _, failure := range []scimFailure{{500, `{"error":"boom"}`}, {403, `{"error":"no"}`}, {404, scimMissingRouteBody}} {
		fake, c := newSignupFake(t)
		failed := failure
		fake.failWith = &failed
		r, sch := signupResource(t, c)

		resp := &resource.DeleteResponse{}
		r.Delete(context.Background(), resource.DeleteRequest{State: scimState(t, sch, signupStored(signupTestTokenID, nil, false))}, resp)
		assert.True(t, resp.Diagnostics.HasError(), "status %d", failure.Status)
	}
}

func TestSignupTokenResource_UpdateIsRefused(t *testing.T) {
	_, c := newSignupFake(t)
	r, sch := signupResource(t, c)
	state := scimState(t, sch, signupStored(signupTestTokenID, nil, false))
	resp := &resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: sch, Raw: state.Raw}}, resp)
	assert.True(t, resp.Diagnostics.HasError())
}
