package client_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every page of a search carries the search term. A walk that dropped it after
// the first page would return unrelated groups (or all of them), and one that
// stopped after the first page would lose the match.
func TestSearchUserGroups_EveryPageKeepsTheSearchTerm(t *testing.T) {
	for _, term := range []string{"admins", "team_1", "50%off", "a b"} {
		t.Run(term, func(t *testing.T) {
			s := &pagedServer{ids: makeIDs(250)}
			groups, err := s.start(t, "/api/user-groups").SearchUserGroups(context.Background(), term)
			require.NoError(t, err)
			assert.Equal(t, s.ids, groupIDs(groups), "all three pages are read")

			require.Len(t, s.queries, 3)
			for page, query := range s.queries {
				assert.Equal(t, term, query["search"], "page %d", page+1)
				assert.Equal(t, fmt.Sprint(page+1), query["pagination[page]"])
				assert.Equal(t, "100", query["pagination[limit]"])
			}
		})
	}
}

// An empty name searches for nothing: every group is listed, with no search
// parameter. So is a name with a backslash, which PostgreSQL would read as an
// escape in the LIKE pattern.
func TestSearchUserGroups_SendsNoSearchForEmptyOrBackslashNames(t *testing.T) {
	for _, name := range []string{"", `corp\admins`} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			s := &pagedServer{ids: makeIDs(130)}
			groups, err := s.start(t, "/api/user-groups").SearchUserGroups(context.Background(), name)
			require.NoError(t, err)
			assert.Len(t, groups, 130)
			require.Len(t, s.queries, 2)
			for _, query := range s.queries {
				_, sent := query["search"]
				assert.False(t, sent, "no search parameter")
			}
		})
	}
}
