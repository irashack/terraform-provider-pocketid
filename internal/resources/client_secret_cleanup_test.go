package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// When Pocket ID 2.17 creates a secret together with a client and revoking
// it is not confirmed, the client's own secret must not be generated beside
// it. The confirming list counts only if it can be relied on: a list whose
// entries lack a usable ID, carry a prefix outside Pocket ID's contract, or
// repeat an ID is no proof that the secret is gone, whatever else it holds.
func TestClientCreateKeepsAutomaticSecretWhenConfirmingListIsMalformed(t *testing.T) {
	const autoID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const otherID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	const wholeValue = "WHOLEsecretVALUEinTheWrongField"
	entry := func(id, prefix string) string {
		return `{"id":"` + id + `","prefix":"` + prefix + `","createdAt":"2026-10-02T10:00:00Z","isActive":true}`
	}
	malformed := map[string]string{
		"empty object":         `[{}]`,
		"ID not a UUID":        `[` + entry("other", "abcd") + `]`,
		"valid then empty":     `[` + entry(otherID, "abcd") + `,{}]`,
		"value as prefix":      `[` + entry(otherID, wholeValue) + `]`,
		"duplicate IDs":        `[` + entry(otherID, "abcd") + `,` + entry(otherID, "abcd") + `]`,
		"names the secret too": `[` + entry(autoID, "synt") + `,{}]`,
	}
	for _, revokeStatus := range []int{http.StatusServiceUnavailable, http.StatusForbidden, http.StatusNotFound} {
		for name, list := range malformed {
			t.Run(fmt.Sprintf("DELETE %d, %s", revokeStatus, name), func(t *testing.T) {
				outcome := runAutomaticSecretCleanup(t, revokeStatus, list)
				assert.Zero(t, outcome.secretPosts, "no second secret is generated")
				assert.Equal(t, 1, outcome.revokes, "the revocation is never retried")
				require.True(t, outcome.response.Diagnostics.HasError())
				text := clientSecretDiagText(outcome.response.Diagnostics)
				assert.Contains(t, text, autoID, "the secret left behind is named")
				assert.Contains(t, text, "could not confirm revocation")
				assert.NotContains(t, text, wholeValue)
				assert.NotContains(t, text, "synthetic-auto-secret")
				assert.NotContains(t, text, "synthetic-token")
			})
		}
	}

	// The same table with a list that can be relied on shows what the
	// refusals above are measured against.
	for name, tc := range map[string]struct {
		list      string
		continues bool
	}{
		"the secret is gone":                        {`[` + entry(otherID, "abcd") + `]`, true},
		"the client holds no secret":                {`[]`, true},
		"the client holds no secret, null":          {`null`, true},
		"the secret is still listed":                {`[` + entry(autoID, "synt") + `]`, false},
		"a migrated secret beside a missing secret": {`[` + entry(otherID, "") + `]`, true},
	} {
		t.Run("DELETE 503, reliable list: "+name, func(t *testing.T) {
			outcome := runAutomaticSecretCleanup(t, http.StatusServiceUnavailable, tc.list)
			if tc.continues {
				assert.Equal(t, 1, outcome.secretPosts)
				assert.False(t, outcome.response.Diagnostics.HasError(), clientSecretDiagText(outcome.response.Diagnostics))
				return
			}
			assert.Zero(t, outcome.secretPosts)
			assert.True(t, outcome.response.Diagnostics.HasError())
		})
	}
}

type automaticSecretCleanup struct {
	response    resource.CreateResponse
	revokes     int
	secretPosts int
}

// runAutomaticSecretCleanup creates a non-public client against a 2.17
// server whose create response carries an automatic secret. Revoking that
// secret is answered with revokeStatus, and the confirming list with list.
func runAutomaticSecretCleanup(t *testing.T, revokeStatus int, list string) automaticSecretCleanup {
	t.Helper()
	const created = `{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","name":"fixture","callbackURLs":["https://example.invalid/callback"],"pkceEnabled":true,` +
		`"createdSecret":{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","prefix":"synt","secret":"synthetic-auto-secret"}}`
	var outcome automaticSecretCleanup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/version/current":
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
		case "POST /api/oidc/clients":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, created)
		case "DELETE /api/oidc/clients/dddddddd-dddd-4ddd-8ddd-dddddddddddd/secrets/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa":
			outcome.revokes++
			w.WriteHeader(revokeStatus)
		case "GET /api/oidc/clients/dddddddd-dddd-4ddd-8ddd-dddddddddddd/secrets":
			_, _ = fmt.Fprint(w, list)
		case "POST /api/oidc/clients/dddddddd-dddd-4ddd-8ddd-dddddddddddd/secrets":
			outcome.secretPosts++
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","prefix":"synt","createdAt":"2026-10-02T10:00:00Z","isActive":true,"secret":"synthetic-managed-secret"}`)
		case "DELETE /api/oidc/clients/dddddddd-dddd-4ddd-8ddd-dddddddddddd":
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/oidc/clients/dddddddd-dddd-4ddd-8ddd-dddddddddddd":
			_, _ = fmt.Fprint(w, `{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"}`)
		default:
			t.Errorf("unexpected method/path %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 1)
	require.NoError(t, err)

	r := &clientResource{client: c}
	ctx := context.Background()
	schemaResp := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	model := lifecycleModel()
	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	require.False(t, plan.Set(ctx, &model).HasError())
	outcome.response = resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &outcome.response)
	return outcome
}
