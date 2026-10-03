package datasources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// b2Fake is a small fake Pocket ID server. A request to a path nothing was
// registered for fails the test, so a data source that reaches for an endpoint
// it should not (a list where a single object will do, a request per group)
// is caught.
type b2Fake struct {
	t        *testing.T
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	prefixes map[string]http.HandlerFunc
	requests []string
}

func newB2Fake(t *testing.T) *b2Fake {
	t.Helper()
	return &b2Fake{t: t, handlers: map[string]http.HandlerFunc{}, prefixes: map[string]http.HandlerFunc{}}
}

// handle registers a handler for "METHOD /path" (no query).
func (f *b2Fake) handle(pattern string, h http.HandlerFunc) {
	f.handlers[pattern] = h
}

// handlePrefix registers a handler for every "METHOD /path/..." under a prefix
// ("GET /api/user-groups/") that has no handler of its own.
func (f *b2Fake) handlePrefix(pattern string, h http.HandlerFunc) {
	f.prefixes[pattern] = h
}

// client starts the server and returns a client for it.
func (f *b2Fake) client() *client.Client {
	f.t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		h, ok := f.handlers[r.Method+" "+r.URL.Path]
		if !ok {
			for prefix, candidate := range f.prefixes {
				if strings.HasPrefix(r.Method+" "+r.URL.Path, prefix) {
					h, ok = candidate, true
					break
				}
			}
		}
		if !ok {
			f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			b2JSON(w, http.StatusNotFound, map[string]any{"error": "API endpoint not found"})
			return
		}
		h(w, r)
	}))
	f.t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(f.t, err)
	return c
}

// log returns the requests received so far, as "METHOD /path?query".
func (f *b2Fake) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// count returns how many requests started with prefix ("GET /api/users").
func (f *b2Fake) count(prefix string) int {
	n := 0
	for _, r := range f.log() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func b2JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// b2NotFound writes Pocket ID's structured not-found error for a resource
// ("User group"), or the user_not_found code when resource is "user".
func b2NotFound(w http.ResponseWriter, resource string) {
	if resource == "user" {
		b2JSON(w, http.StatusNotFound, map[string]any{"error": "User not found", "code": "user_not_found", "request_id": "r"})
		return
	}
	b2JSON(w, http.StatusNotFound, map[string]any{"error": resource + " not found", "code": "not_found", "details": map[string]any{"resource": resource}, "request_id": "r"})
}

// b2Paginate answers a paginated list request, honoring pagination[page] and
// pagination[limit] (default 20), after filter has selected the items.
func b2Paginate(w http.ResponseWriter, r *http.Request, items []any) {
	page, limit := 1, 20
	if v := r.URL.Query().Get("pagination[page]"); v != "" {
		page, _ = strconv.Atoi(v)
	}
	if v := r.URL.Query().Get("pagination[limit]"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	totalPages := (len(items) + limit - 1) / limit
	if totalPages == 0 {
		totalPages = 1
	}
	start, end := (page-1)*limit, page*limit
	if start > len(items) {
		start = len(items)
	}
	if end > len(items) {
		end = len(items)
	}
	b2JSON(w, http.StatusOK, map[string]any{
		"data": items[start:end],
		"pagination": map[string]any{
			"totalPages": totalPages, "totalItems": len(items), "currentPage": page, "itemsPerPage": limit,
		},
	})
}

// b2UUID returns the n-th test UUID.
func b2UUID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

// b2Config builds a data source configuration from the schema: every
// attribute is null except those in values.
func b2Config(ctx context.Context, t *testing.T, ds datasource.DataSource, values map[string]tftypes.Value) tfsdk.Config {
	t.Helper()
	sch := b2Schema(t, ds)
	objType, ok := sch.Type().TerraformType(ctx).(tftypes.Object)
	require.True(t, ok)
	attrs := map[string]tftypes.Value{}
	for name, typ := range objType.AttributeTypes {
		attrs[name] = tftypes.NewValue(typ, nil)
	}
	for name, v := range values {
		_, known := attrs[name]
		require.True(t, known, "the schema has no attribute %q", name)
		attrs[name] = v
	}
	return tfsdk.Config{Schema: sch, Raw: tftypes.NewValue(objType, attrs)}
}

func b2Schema(t *testing.T, ds datasource.DataSource) schema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	ds.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return resp.Schema
}

func b2Str(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

// b2Configure hands the client to a data source.
func b2Configure(t *testing.T, ds datasource.DataSource, c *client.Client) datasource.DataSource {
	t.Helper()
	resp := &datasource.ConfigureResponse{}
	ds.(datasource.DataSourceWithConfigure).Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return ds
}

// b2Read reads a data source with the given configuration values and returns
// the response.
func b2Read(t *testing.T, ds datasource.DataSource, values map[string]tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	sch := b2Schema(t, ds)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
	ds.Read(ctx, datasource.ReadRequest{Config: b2Config(ctx, t, ds, values)}, resp)
	return resp
}

// b2Summaries lists the summaries of a response's error diagnostics.
func b2Summaries(resp *datasource.ReadResponse) []string {
	var out []string
	for _, d := range resp.Diagnostics.Errors() {
		out = append(out, d.Summary())
	}
	return out
}

// b2Attr reads one attribute of the resulting state into out.
func b2Attr(t *testing.T, resp *datasource.ReadResponse, name string, out any) {
	t.Helper()
	require.False(t, resp.State.GetAttribute(context.Background(), path.Root(name), out).HasError(), "attribute %s", name)
}
