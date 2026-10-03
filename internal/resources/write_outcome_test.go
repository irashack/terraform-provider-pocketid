package resources

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// An accepted write is never a refusal, whatever else its error wraps; a 4xx
// to the write itself and an identifier the client would not send are.
func TestWriteRefused(t *testing.T) {
	forbidden := &client.HTTPError{StatusCode: 403}
	for name, tc := range map[string]struct {
		err     error
		refused bool
	}{
		"no error":                         {nil, false},
		"4xx":                              {forbidden, true},
		"5xx":                              {&client.HTTPError{StatusCode: 503}, false},
		"not sent: identifier":             {fmt.Errorf("x: %w", client.ErrInvalidIdentifier), true},
		"accepted, unusable ID in answer":  {fmt.Errorf("%w: %w", client.ErrResultUnread, client.ErrInvalidIdentifier), false},
		"accepted, 403 on the read-back":   {fmt.Errorf("%w; reading back: %w", client.ErrResultUnread, forbidden), false},
		"accepted, undecodable answer":     {fmt.Errorf("%w: %w", client.ErrResultUnread, client.ErrUndecodableResponse), false},
		"transport failure, no status":     {errors.New("connection reset"), false},
		"identifier inside a 4xx wrapping": {fmt.Errorf("%w: %w", forbidden, client.ErrInvalidIdentifier), true},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.refused, writeRefused(tc.err))
			assert.Equal(t, tc.refused, definitelyRejected(tc.err), "the client resource agrees")
			assert.Equal(t, tc.refused, apiWriteRefused(tc.err), "the API resources agree")
		})
	}
}

// Group, member and secret IDs are UUIDs, the same in any letter case.
func TestUUIDComparisonsIgnoreCase(t *testing.T) {
	const lower = "abcdef01-0000-4000-8000-0000000000aa"
	upper := strings.ToUpper(lower)
	assert.Empty(t, missingFrom([]string{upper}, []string{lower}), "a client's group in another case is not missing")
	assert.True(t, sameMembers([]string{upper}, []string{lower}))
	assert.Empty(t, groupMembersDiff([]string{lower}, []string{upper}))
	assert.Equal(t, []string{upper}, groupMembersUnique([]string{upper, lower}), "one ID, the first spelling kept")

	// A failed secret create counts as new only the secrets that were not
	// listed before, in any spelling.
	known := map[string]bool{strings.ToLower(upper): true}
	text := describeClientSecrets([]client.ClientSecretMetadata{{ID: lower, Prefix: "abcd"}}, known)
	assert.NotContains(t, text, "new since this attempt")
}
