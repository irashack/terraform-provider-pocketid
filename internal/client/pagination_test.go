package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// pagedServer serves ids the way utils.Paginate does: pagination[limit]
// defaults to 20 and is clamped to 100 (or fixed at forcedLimit when that is
// set, for a server that ignores the requested size), a page past the end is
// clamped to the last page, and totalPages is 1 for an empty list. It records
// each request's query.
type pagedServer struct {
	mu          sync.Mutex
	ids         []string
	forcedLimit int
	queries     []map[string]string
	// mutate, when set, can change the response for a request (1-based count).
	mutate func(request int, page int, resp map[string]any)
}

func (s *pagedServer) start(t *testing.T, path string) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		require.Equal(t, path, r.URL.Path)
		query := map[string]string{}
		for key := range r.URL.Query() {
			query[key] = r.URL.Query().Get(key)
		}
		s.queries = append(s.queries, query)

		limit, _ := strconv.Atoi(query["pagination[limit]"])
		if limit < 1 {
			limit = 20
		} else if limit > 100 {
			limit = 100
		}
		if s.forcedLimit > 0 {
			limit = s.forcedLimit
		}
		page, _ := strconv.Atoi(query["pagination[page]"])
		if page < 1 {
			page = 1
		}
		totalPages := (len(s.ids) + limit - 1) / limit
		if totalPages == 0 {
			totalPages = 1
		}
		if page > totalPages {
			page = totalPages
		}
		start := (page - 1) * limit
		end := min(start+limit, len(s.ids))
		data := []map[string]string{}
		for _, id := range s.ids[start:end] {
			data = append(data, map[string]string{"id": id, "name": "n-" + id})
		}
		resp := map[string]any{
			"data": data,
			"pagination": map[string]any{
				"totalPages": totalPages, "totalItems": len(s.ids), "currentPage": page, "itemsPerPage": limit,
			},
		}
		if s.mutate != nil {
			s.mutate(len(s.queries), page, resp)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return c
}

func makeIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
	}
	return ids
}

func groupIDs(groups []client.UserGroup) []string {
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	return ids
}

func clientIDs(clients []client.OIDCClient) []string {
	ids := make([]string, 0, len(clients))
	for _, c := range clients {
		ids = append(ids, c.ID)
	}
	return ids
}

func TestListAll_ReturnsEveryPage(t *testing.T) {
	for _, n := range []int{0, 1, 20, 21, 100, 101, 250} {
		t.Run(fmt.Sprintf("%d groups", n), func(t *testing.T) {
			s := &pagedServer{ids: makeIDs(n)}
			groups, err := s.start(t, "/api/user-groups").ListUserGroups(context.Background())
			require.NoError(t, err)
			assert.Equal(t, s.ids, groupIDs(groups))
			wantPages := max(1, (n+99)/100)
			require.Len(t, s.queries, wantPages, "one request per page of 100")
			for i, query := range s.queries {
				assert.Equal(t, strconv.Itoa(i+1), query["pagination[page]"])
				assert.Equal(t, "100", query["pagination[limit]"])
				assert.Equal(t, "createdAt", query["sort[column]"])
				assert.Equal(t, "asc", query["sort[direction]"])
			}
		})
		t.Run(fmt.Sprintf("%d clients", n), func(t *testing.T) {
			s := &pagedServer{ids: makeIDs(n)}
			clients, err := s.start(t, "/api/oidc/clients").ListClients(context.Background())
			require.NoError(t, err)
			assert.Equal(t, s.ids, clientIDs(clients))
		})
	}
}

// A server that serves its own default page size whatever is asked still
// yields every object, because the walk follows totalPages.
func TestListAll_FollowsServerPageSize(t *testing.T) {
	s := &pagedServer{ids: makeIDs(101), forcedLimit: 20}
	clients, err := s.start(t, "/api/oidc/clients").ListClients(context.Background())
	require.NoError(t, err)
	assert.Equal(t, s.ids, clientIDs(clients))
	assert.Len(t, s.queries, 6)
}

func TestListAll_MalformedPaginationIsError(t *testing.T) {
	for name, mutate := range map[string]func(int, int, map[string]any){
		"missing block": func(_ int, _ int, resp map[string]any) { delete(resp, "pagination") },
		"zero pages": func(_ int, _ int, resp map[string]any) {
			resp["pagination"].(map[string]any)["totalPages"] = 0
		},
		"never advances": func(_ int, _ int, resp map[string]any) {
			resp["pagination"].(map[string]any)["currentPage"] = 1
		},
		"negative total": func(_ int, _ int, resp map[string]any) {
			resp["pagination"].(map[string]any)["totalItems"] = -1
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := &pagedServer{ids: makeIDs(150), mutate: mutate}
			groups, err := s.start(t, "/api/user-groups").ListUserGroups(context.Background())
			require.Error(t, err)
			assert.Nil(t, groups)
			assert.Contains(t, err.Error(), "refusing to guess")
		})
	}
}

// Pages that do not add up (the list changed between requests) are read
// again once; if they still do not add up, the call fails rather than return
// a list with gaps or duplicates.
func TestListAll_ChangedListIsReadAgainThenRefused(t *testing.T) {
	duplicateSecondPage := func(page int, resp map[string]any) {
		if page == 2 {
			resp["data"].([]map[string]string)[0]["id"] = "00000000-0000-4000-8000-000000000000"
		}
	}
	t.Run("changed once", func(t *testing.T) {
		s := &pagedServer{ids: makeIDs(150)}
		s.mutate = func(request int, page int, resp map[string]any) {
			if request <= 2 {
				duplicateSecondPage(page, resp)
			}
		}
		groups, err := s.start(t, "/api/user-groups").ListUserGroups(context.Background())
		require.NoError(t, err)
		assert.Equal(t, s.ids, groupIDs(groups))
		assert.Len(t, s.queries, 4, "the walk is repeated once")
	})
	t.Run("object on two pages", func(t *testing.T) {
		s := &pagedServer{ids: makeIDs(150)}
		s.mutate = func(_ int, page int, resp map[string]any) { duplicateSecondPage(page, resp) }
		_, err := s.start(t, "/api/user-groups").ListUserGroups(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changed while it was being read")
		assert.Len(t, s.queries, 4, "two walks, no more")
	})
	t.Run("count does not add up", func(t *testing.T) {
		s := &pagedServer{ids: makeIDs(150)}
		s.mutate = func(_ int, page int, resp map[string]any) {
			if page == 2 {
				resp["pagination"].(map[string]any)["totalItems"] = 151
			}
		}
		_, err := s.start(t, "/api/oidc/clients").ListClients(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changed while it was being read")
	})
}

func TestListAll_StopsAtPageCeiling(t *testing.T) {
	s := &pagedServer{ids: makeIDs(1500), forcedLimit: 1}
	_, err := s.start(t, "/api/user-groups").ListUserGroups(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not end after 1000 pages")
	assert.Len(t, s.queries, 1000)
}

// Every listed object's ID goes through the returned-ID rules before the
// list is returned: one ID that is not a form any Pocket ID object ID takes
// fails the whole list, on whichever page it appears, and is not quoted.
// IDs of every form the server can hold pass: UUIDs, client IDs its own
// pattern accepts (".." included) and CIMD clients' URLs, spaces and
// Unicode in the path included.
func TestListAll_ChecksEveryID(t *testing.T) {
	for _, bad := range []string{"", "a/b", "a b", "https://client.example.com/m?x=1", "unsafe\u00e9id", "https://client.example.com/a\u0000b"} {
		s := &pagedServer{ids: makeIDs(150)}
		s.ids[120] = bad
		_, err := s.start(t, "/api/user-groups").ListUserGroups(context.Background())
		require.ErrorIs(t, err, client.ErrInvalidIdentifier, "%q", bad)
		assert.Contains(t, err.Error(), "listing user groups: ")
		assert.Contains(t, err.Error(), "is not a valid object ID")
		if len(bad) >= 8 {
			assert.NotContains(t, err.Error(), bad)
		}
		assert.Len(t, s.queries, 2, "the walk stops at the page that held it, and is not repeated")
	}

	s := &pagedServer{ids: []string{
		validUUID, "my-app", "..", "a", "https://client.example.com/oauth/metadata.json",
		"https://client.example.com/a b/m\u00e9tadonn\u00e9es client.json",
	}}
	clients, err := s.start(t, "/api/oidc/clients").ListClients(context.Background())
	require.NoError(t, err)
	assert.Equal(t, s.ids, clientIDs(clients))
}

// A listed ID that contains the API key, as the server received it, fails
// the list without the key in the error. A UUID-shaped key would pass the
// form check on its own.
func TestListAll_RefusesTheReflectedKey(t *testing.T) {
	const key = "7d3f9a12-4c8e-4b6a-9f21-0e5d8c7b6a43"
	for _, configured := range []string{key, " \t" + key + " "} {
		for name, embed := range map[string]func(string) string{
			"as the ID":     func(k string) string { return k },
			"inside an ID":  func(k string) string { return "app-" + k },
			"inside a CIMD": func(k string) string { return "https://client.example.com/" + k },
		} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"data":[{"id":%q},{"id":%q}],"pagination":{"totalPages":1,"totalItems":2,"currentPage":1,"itemsPerPage":100}}`,
					validUUID, embed(r.Header.Get("X-API-KEY")))
			}))
			c, err := client.NewClient(server.URL, configured, false, 30)
			require.NoError(t, err)
			_, err = c.ListClients(context.Background())
			server.Close()
			require.ErrorIs(t, err, client.ErrInvalidIdentifier, name)
			assert.Contains(t, err.Error(), "contains the API key this provider", name)
			assert.NotContains(t, err.Error(), key, name)
		}
	}
}

// Pocket ID accepts any static API key of 16 or more characters, so a key
// can be all digits. A server that echoes the key it received as a number in
// the pagination block gets nothing into the error, whichever field it uses
// and whichever check fails; the errors keep their classification. A number
// too large for its field fails decoding with a fixed error that quotes
// nothing either.
func TestListAll_PaginationErrorsQuoteNothing(t *testing.T) {
	const (
		key      = "4815162342108151"         // 16 digits: fits an int
		longKey  = "481516234210815162342108" // 24 digits: too large for an int
		oneGroup = `[{"id":"` + validUUID + `"}]`
	)
	cases := map[string]struct {
		key, block string // KEY in block becomes the key the server received
		want       string
		wantIs     error
	}{
		"currentPage":           {key: key, block: `{"totalPages":1,"totalItems":1,"currentPage":KEY,"itemsPerPage":100}`, want: "refusing to guess"},
		"totalPages":            {key: key, block: `{"totalPages":KEY,"totalItems":1,"currentPage":1,"itemsPerPage":100}`, want: "refusing to guess"},
		"totalItems":            {key: key, block: `{"totalPages":1,"totalItems":KEY,"currentPage":1,"itemsPerPage":100}`, want: "changed while it was being read"},
		"negative totalItems":   {key: key, block: `{"totalPages":1,"totalItems":-KEY,"currentPage":1,"itemsPerPage":100}`, want: "refusing to guess"},
		"negative totalPages":   {key: key, block: `{"totalPages":-KEY,"totalItems":1,"currentPage":1,"itemsPerPage":100}`, want: "refusing to guess"},
		"currentPage overflow":  {key: longKey, block: `{"totalPages":1,"totalItems":1,"currentPage":KEY,"itemsPerPage":100}`, wantIs: client.ErrUndecodableResponse},
		"totalItems overflow":   {key: longKey, block: `{"totalPages":1,"totalItems":KEY,"currentPage":1,"itemsPerPage":100}`, wantIs: client.ErrUndecodableResponse},
		"itemsPerPage overflow": {key: longKey, block: `{"totalPages":1,"totalItems":1,"currentPage":1,"itemsPerPage":KEY}`, wantIs: client.ErrUndecodableResponse},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				block := strings.ReplaceAll(tc.block, "KEY", r.Header.Get("X-API-KEY"))
				_, _ = fmt.Fprintf(w, `{"data":%s,"pagination":%s}`, oneGroup, block)
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, tc.key, false, 30)
			require.NoError(t, err)

			// An answer that carries the key is refused before the
			// pagination block is looked at.
			_, err = c.ListUserGroups(context.Background())
			require.ErrorIs(t, err, client.ErrInvalidIdentifier)
			assert.NotContains(t, err.Error(), tc.key)
			assert.NotContains(t, err.Error(), tc.key[:16], "not even in part")
			assert.Positive(t, requests.Load())

			// The same numbers, when they are not the key, fail the
			// pagination checks or the decoder, which quote nothing either.
			numbers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"data":%s,"pagination":%s}`, oneGroup, strings.ReplaceAll(tc.block, "KEY", tc.key))
			}))
			defer numbers.Close()
			c, err = client.NewClient(numbers.URL, "an-unrelated-static-key", false, 30)
			require.NoError(t, err)
			_, err = c.ListUserGroups(context.Background())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), tc.key)
			assert.NotContains(t, err.Error(), tc.key[:16], "not even in part")
			if tc.wantIs != nil {
				assert.ErrorIs(t, err, tc.wantIs)
				assert.Equal(t, client.ErrUndecodableResponse.Error(), err.Error(), "the decoder's own text is dropped")
			} else {
				assert.Contains(t, err.Error(), tc.want)
			}
		})
	}
}
