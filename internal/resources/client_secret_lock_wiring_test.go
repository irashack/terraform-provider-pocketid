package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

const (
	lockRaceAutoID  = "a1a1a1a1-0000-4000-8000-000000000001"
	lockRaceGenID   = "a2a2a2a2-0000-4000-8000-000000000002"
	lockRaceOwnID   = "a3a3a3a3-0000-4000-8000-000000000003"
	lockRaceOtherID = "a4a4a4a4-0000-4000-8000-000000000004"
)

// lockRaceFake is a Pocket ID 2.17 with two confidential clients: "app",
// which does not exist until pocketid_client creates it (with an automatic
// secret, as 2.17 does), and "other", which exists. The DELETE of app's
// automatic secret is held until release is closed and then answered 503
// after it took effect, so pocketid_client confirms the revocation with a
// list. A POST of a chosen value to app's secrets creates the secret and
// answers 502, so pocketid_client_secret reports an uncertain create and
// names the secrets that appeared meanwhile. events records, in order, every
// request that reads or changes a secret of app, and app's own reads.
type lockRaceFake struct {
	t        *testing.T
	mu       sync.Mutex
	created  bool
	secrets  map[string][]string
	events   []string
	arrived  chan struct{} // closed when the held DELETE arrives
	release  chan struct{}
	arriveMu sync.Once
}

func (f *lockRaceFake) record(event string) {
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
}

func (f *lockRaceFake) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	clientJSON := func(id string) string {
		return `{"id":"` + id + `","name":"fixture","callbackURLs":["https://example.invalid/callback"],"pkceEnabled":true,"isPublic":false,"isGroupRestricted":false,"allowedUserGroups":[]}`
	}
	secretObject := func(id string) string {
		return `{"id":"` + id + `","prefix":"` + id[:4] + `","createdAt":"2026-10-02T10:00:00Z","expiresAt":null,"isActive":true}`
	}
	path := r.URL.Path
	switch {
	case path == "/api/version/current":
		_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
	case r.Method == http.MethodGet && path == "/api/oidc/clients/app":
		f.mu.Lock()
		created := f.created
		f.mu.Unlock()
		if !created {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":"OIDC client not found","code":"not_found","details":{"resource":"OIDC client"}}`)
			return
		}
		f.record("GET app")
		_, _ = fmt.Fprint(w, clientJSON("app"))
	case r.Method == http.MethodGet && path == "/api/oidc/clients/other":
		_, _ = fmt.Fprint(w, clientJSON("other"))
	case r.Method == http.MethodPost && path == "/api/oidc/clients":
		f.mu.Lock()
		f.created = true
		f.secrets["app"] = []string{lockRaceAutoID}
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		body := strings.TrimSuffix(clientJSON("app"), "}") + `,"createdSecret":{"id":"` + lockRaceAutoID + `","prefix":"auto","secret":"autosynthetic-server-created"}}`
		_, _ = fmt.Fprint(w, body)
	case r.Method == http.MethodDelete && path == "/api/oidc/clients/app/secrets/"+lockRaceAutoID:
		f.record("DELETE auto")
		f.arriveMu.Do(func() { close(f.arrived) })
		<-f.release
		f.mu.Lock()
		f.secrets["app"] = removeString(f.secrets["app"], lockRaceAutoID)
		f.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/secrets"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/oidc/clients/"), "/secrets")
		if id == "app" {
			f.record("LIST app")
		}
		f.mu.Lock()
		list := make([]string, 0, len(f.secrets[id]))
		for _, secret := range f.secrets[id] {
			list = append(list, secretObject(secret))
		}
		f.mu.Unlock()
		_, _ = fmt.Fprint(w, "["+strings.Join(list, ",")+"]")
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/secrets"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/oidc/clients/"), "/secrets")
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Secret string `json:"secret"`
		}
		_ = json.Unmarshal(raw, &body)
		switch {
		case id == "other":
			f.mu.Lock()
			f.secrets["other"] = append(f.secrets["other"], lockRaceOtherID)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"id":%q,"prefix":%q,"createdAt":"2026-10-02T10:00:00Z","expiresAt":null,"isActive":true,"secret":%q}`, lockRaceOtherID, body.Secret[:4], body.Secret)
		case body.Secret != "":
			f.record("POST chosen")
			f.mu.Lock()
			f.secrets["app"] = append(f.secrets["app"], lockRaceOwnID)
			f.mu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
		default:
			f.record("POST generate")
			f.mu.Lock()
			f.secrets["app"] = append(f.secrets["app"], lockRaceGenID)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"id":%q,"prefix":"gen1","createdAt":"2026-10-02T10:00:00Z","expiresAt":null,"isActive":true,"secret":"gen1synthetic-generated-0001"}`, lockRaceGenID)
		}
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func removeString(list []string, value string) []string {
	out := list[:0]
	for _, item := range list {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

// pocketid_client and pocketid_client_secret change one client's secrets at
// the same time. pocketid_client's whole secret sequence on create (revoking
// the secret Pocket ID created with the client, confirming that with a list
// after an uncertain answer, and generating its own) and
// pocketid_client_secret's (the list before, the POST with an uncertain
// answer, the list after that names what appeared) must not interleave:
// otherwise the secret resource names pocketid_client's secret as a
// candidate for its own lost create. A secret change on another client goes
// ahead meanwhile, every operation finishes (no deadlock), the client
// generates exactly one secret, and each resource keeps only its own.
func TestClientSecretLock_ClientAndSecretResourcesDoNotInterleave(t *testing.T) {
	fake := &lockRaceFake{t: t, secrets: map[string][]string{}, arrived: make(chan struct{}), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	released := false
	defer func() {
		if !released {
			close(fake.release)
		}
	}()
	newClient := func() *client.Client {
		c, err := client.NewClient(server.URL, "synthetic-token", false, 10)
		require.NoError(t, err)
		return c
	}
	ctx := context.Background()

	// pocketid_client creates "app" and stops inside its secret sequence.
	clientDone := make(chan resource.CreateResponse, 1)
	go func() {
		r := &clientResource{client: newClient()}
		schemaResp := resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
		model := lifecycleModel()
		model.ClientID = types.StringValue("app")
		plan := tfsdk.Plan{Schema: schemaResp.Schema}
		if diags := plan.Set(ctx, &model); diags.HasError() {
			t.Errorf("plan: %v", diags)
		}
		resp := resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
		clientDone <- resp
	}()
	select {
	case <-fake.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("pocketid_client never started revoking the automatic secret")
	}

	// pocketid_client_secret starts on the same client.
	type secretOutcome struct {
		resp  resource.CreateResponse
		state *clientSecretResourceModel
	}
	secretDone := make(chan secretOutcome, 1)
	go func() {
		config := clientSecretPlanned()
		config.SecretWO, config.SecretWOVersion = types.StringValue(clientSecretTestValue), types.StringValue("1")
		resp, state := clientSecretCreate(t, newClient(), config)
		secretDone <- secretOutcome{resp, state}
	}()

	// A secret on another client is created meanwhile.
	otherDone := make(chan *clientSecretResourceModel, 1)
	go func() {
		config := clientSecretPlanned()
		config.ClientID = types.StringValue("other")
		config.SecretWO, config.SecretWOVersion = types.StringValue(clientSecretTestValue), types.StringValue("1")
		_, state := clientSecretCreate(t, newClient(), config)
		otherDone <- state
	}()
	select {
	case state := <-otherDone:
		require.NotNil(t, state, "a secret of another client is created while app's secrets are busy")
		assert.Equal(t, lockRaceOtherID, state.ID.ValueString())
	case <-time.After(10 * time.Second):
		t.Fatal("a secret change on another client waited for app's lock")
	}

	// Give a secret sequence that does not wait for the lock time to reach
	// the server, then let pocketid_client continue.
	time.Sleep(300 * time.Millisecond)
	close(fake.release)
	released = true

	var clientResp resource.CreateResponse
	var secret secretOutcome
	for range 2 {
		select {
		case clientResp = <-clientDone:
		case secret = <-secretDone:
		case <-time.After(20 * time.Second):
			t.Fatal("the two resources did not both finish: deadlock")
		}
	}

	fake.mu.Lock()
	events := append([]string(nil), fake.events...)
	fake.mu.Unlock()
	t.Logf("app's secret requests in order: %v", events)

	// pocketid_client's sequence runs from its DELETE to its generation,
	// with nothing of the secret resource's in between, and the secret
	// resource's whole sequence runs after it.
	start, end := indexOf(events, "DELETE auto"), indexOf(events, "POST generate")
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	assert.Equal(t, []string{"DELETE auto", "LIST app", "POST generate"}, events[start:end+1],
		"pocketid_client's revocation, its confirming list and its generation are not interleaved")
	for _, event := range events[:start] {
		assert.NotEqual(t, "POST chosen", event)
	}
	assert.Equal(t, []string{"GET app", "LIST app", "POST chosen", "LIST app"}, events[end+1:],
		"pocketid_client_secret's read, list, uncertain create and confirming list run as one sequence afterwards")
	assert.Equal(t, 1, countOf(events, "POST generate"), "pocketid_client generates exactly one secret")

	require.False(t, clientResp.Diagnostics.HasError(), "%v", clientResp.Diagnostics)
	var clientState clientResourceModel
	require.False(t, clientResp.State.Get(ctx, &clientState).HasError())
	assert.Equal(t, lockRaceGenID, clientState.ClientSecretID.ValueString(), "pocketid_client keeps the secret it generated")

	require.True(t, secret.resp.Diagnostics.HasError())
	assert.Nil(t, secret.state, "an uncertain create records nothing")
	detail := clientSecretDiagText(secret.resp.Diagnostics)
	assert.Contains(t, detail, "One secret appeared on the client during the attempt")
	for _, line := range strings.Split(detail, "\n") {
		switch {
		case strings.Contains(line, lockRaceOwnID):
			assert.Contains(t, line, "[new since this attempt]", "its own secret is the candidate")
		case strings.Contains(line, lockRaceGenID):
			assert.NotContains(t, line, "[new since this attempt]", "pocketid_client's secret is never a candidate")
		}
	}
	assert.NotContains(t, detail, clientSecretTestValue)
}

func indexOf(list []string, value string) int {
	for i, item := range list {
		if item == value {
			return i
		}
	}
	return -1
}

func countOf(list []string, value string) int {
	n := 0
	for _, item := range list {
		if item == value {
			n++
		}
	}
	return n
}

// clientSecretLockHeld reports whether some holder has the client's secret
// lock right now.
func clientSecretLockHeld(clientID string) bool {
	clientSecretLocksMu.Lock()
	lock, ok := clientSecretLocks[clientID]
	clientSecretLocksMu.Unlock()
	if !ok {
		return false
	}
	if lock.TryLock() {
		lock.Unlock()
		return false
	}
	return true
}

// lockObservingServer serves fake and records, for every request, whether
// client c1's secret lock was held while the server handled it.
func lockObservingServer(t *testing.T, fake *fakePocketID) (*client.Client, func() map[string][]bool) {
	t.Helper()
	var mu sync.Mutex
	held := map[string][]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		locked := clientSecretLockHeld("c1")
		mu.Lock()
		call := r.Method + " " + r.URL.Path
		held[call] = append(held[call], locked)
		mu.Unlock()
		fake.serve(w, r)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 5)
	require.NoError(t, err)
	return c, func() map[string][]bool {
		mu.Lock()
		defer mu.Unlock()
		return held
	}
}

// On update, pocketid_client reads, generates and revokes its secrets only
// while it holds the client's secret lock, and holds it during neither the
// client's own PUT nor its group write.
func TestClientUpdateHoldsTheSecretLockForSecretChangesOnly(t *testing.T) {
	const heldID = "00000000-0000-4000-8000-000000000000"
	assertHeld := func(t *testing.T, held map[string][]bool, calls ...string) {
		t.Helper()
		for _, call := range calls {
			require.NotEmpty(t, held[call], "%s was requested", call)
			for _, locked := range held[call] {
				assert.True(t, locked, "%s runs under the secret lock", call)
			}
		}
		for _, locked := range held["PUT /api/oidc/clients/c1"] {
			assert.False(t, locked, "the client's PUT runs without the secret lock")
		}
		assert.False(t, clientSecretLockHeld("c1"), "the lock is released afterwards")
	}

	t.Run("generation", func(t *testing.T) {
		fake := managedFake(t, "2.16.0")
		fake.secrets = nil
		c, held := lockObservingServer(t, fake)
		r := &clientResource{client: c}
		prior := managedModel()
		prior.GenerateSecret = types.BoolValue(false)
		prior.ClientSecret, prior.ClientSecretID = types.StringNull(), types.StringNull()
		planned := prior
		planned.GenerateSecret = types.BoolValue(true)
		planned.ClientSecret, planned.ClientSecretID = types.StringUnknown(), types.StringUnknown()
		resp, _ := runUpdate(t, r, prior, planned, configOf(planned))
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assertHeld(t, held(), "POST /api/oidc/clients/c1/secrets")
	})
	t.Run("revocation of a secret identified by its prefix", func(t *testing.T) {
		fake := managedFake(t, "2.17.0")
		c, held := lockObservingServer(t, fake)
		r := &clientResource{client: c}
		prior := managedModel()
		prior.ClientSecretID = types.StringNull()
		prior.GenerateSecret = types.BoolNull()
		planned := prior
		planned.GenerateSecret = types.BoolValue(false)
		planned.ClientSecret, planned.ClientSecretID = types.StringNull(), types.StringNull()
		resp, _ := runUpdate(t, r, prior, planned, configOf(planned))
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assertHeld(t, held(), "GET /api/oidc/clients/c1/secrets", "DELETE /api/oidc/clients/c1/secrets/"+heldID)
	})
}

// After an uncertain create, a secret listed before in one letter case and
// after in another is the same secret, not one that appeared.
func TestClientSecretResource_UncertainCreateCountsSecretsAsUUIDs(t *testing.T) {
	const existing = "abcdef01-0000-4000-8000-0000000000aa"
	const created = "abcdef02-0000-4000-8000-0000000000bb"
	posted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		object := func(id string) string {
			return `{"id":"` + id + `","prefix":"abcd","createdAt":"2026-10-02T10:00:00Z","expiresAt":null,"isActive":true}`
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/version/current":
			_, _ = fmt.Fprint(w, `{"currentVersion":"2.17.0"}`)
		case "GET /api/oidc/clients/app":
			_, _ = fmt.Fprint(w, `{"id":"app","name":"app","callbackURLs":[],"isPublic":false,"allowedUserGroups":[]}`)
		case "GET /api/oidc/clients/app/secrets":
			if !posted {
				_, _ = fmt.Fprint(w, "["+object(strings.ToUpper(existing))+"]")
				return
			}
			_, _ = fmt.Fprint(w, "["+object(existing)+","+object(created)+"]")
		case "POST /api/oidc/clients/app/secrets":
			posted = true
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "synthetic-token", false, 2)
	require.NoError(t, err)
	resp, state := clientSecretCreate(t, c, clientSecretPlanned())
	require.True(t, resp.Diagnostics.HasError())
	assert.Nil(t, state)
	detail := clientSecretDiagText(resp.Diagnostics)
	assert.Contains(t, detail, "One secret appeared on the client during the attempt")
	for _, line := range strings.Split(detail, "\n") {
		if strings.Contains(strings.ToLower(line), existing) {
			assert.NotContains(t, line, "[new since this attempt]", "the secret listed before is not new")
		}
	}
}
