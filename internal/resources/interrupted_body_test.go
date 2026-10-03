package resources_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// interruptResponse answers with a 2xx status and a body that breaks off
// before its declared length, the way a connection lost mid-response looks.
func interruptResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, buf, err := w.(http.Hijacker).Hijack()
	require.NoError(t, err)
	interruptOn(conn, buf)
}

func interruptOn(conn net.Conn, buf *bufio.ReadWriter) {
	_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 4000\r\n\r\n{\"id\":\"")
	_ = buf.Flush()
	_ = conn.Close()
}

// SetGroupMembers whose 2xx answer breaks off mid-body: the client reports
// the foundation's ResponseBodyError marked as accepted (ErrResultUnread),
// never a rejection. pocketid_group_members then treats the write as made with
// an unknown result: when the group cannot be read afterwards either, it keeps
// its identity with the requested users as candidates to clean up.
func TestGroupMembersResource_InterruptedResponseBodyIsAnUnreadResult(t *testing.T) {
	s, c := newGMServer(t)
	s.putInterrupted = true

	_, err := c.SetGroupMembers(context.Background(), s.groupID, []string{gmUUID(103)})
	var bodyErr *client.ResponseBodyError
	require.ErrorAs(t, err, &bodyErr)
	assert.True(t, bodyErr.Accepted)
	assert.ErrorIs(t, err, client.ErrResultUnread)
	assert.False(t, client.IsDefiniteRejection(err))

	s, c = newGMServer(t)
	s.putInterrupted = true
	s.failGetsAfterPut = true
	r, sch := gmResource(t, c)
	resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101)})
	require.True(t, resp.Diagnostics.HasError())
	assert.Equal(t, "Group members may have changed", resp.Diagnostics.Errors()[0].Summary())
	require.False(t, resp.State.Raw.IsNull(), "the write was accepted, so the resource is kept")
	candidates, null := gmStateUnresolved(t, resp.State)
	assert.False(t, null)
	assert.Equal(t, []string{gmUUID(101)}, candidates)
	assert.Equal(t, 1, s.putCount(), "never sent twice")
	assert.Equal(t, []string{gmUUID(101)}, s.memberSet(), "the request did take effect")
}

// A profile-picture upload whose 2xx answer breaks off mid-body was accepted:
// it is reported as an upload that may have replaced the picture, never as
// one Pocket ID refused, and it is not repeated.
func TestUserProfilePictureResource_InterruptedUploadResponseIsNotARefusal(t *testing.T) {
	s, _ := newPPServer(t)
	content := ppPNG(t, 60, 40, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			s.mu.Lock()
			s.log = append(s.log, r.Method+" "+r.URL.Path)
			s.custom = content
			s.mu.Unlock()
			interruptResponse(t, w)
			return
		}
		s.serve(w, r)
	}))
	t.Cleanup(server.Close)
	interrupting, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	err = interrupting.UploadUserProfilePicture(context.Background(), ppUser, client.MultipartFile{FileName: "p.png", ContentType: "image/png", Content: content})
	var bodyErr *client.ResponseBodyError
	require.True(t, errors.As(err, &bodyErr))
	assert.True(t, bodyErr.Accepted)
	assert.ErrorIs(t, err, client.ErrResultUnread)
	assert.False(t, client.IsDefiniteRejection(err))

	r, sch := ppResource(t, interrupting)
	resp := ppCreate(t, r, sch, ppFile(t, content), "")
	require.True(t, resp.Diagnostics.HasError())
	detail := resp.Diagnostics.Errors()[0].Detail()
	assert.Contains(t, detail, "may or may not")
	assert.NotContains(t, detail, "refused")
	assert.NotContains(t, detail, "Nothing was changed")
	assert.Equal(t, 2, s.count("PUT"), "one upload per call, never repeated")
}

// A PUT that Pocket ID accepted, whose answer names a member by an unusable
// ID, is an accepted write with an unknown result (ErrResultUnread alongside
// ErrInvalidIdentifier), never a refusal: the group is read to verify. When
// that read shows the requested members, they are recorded; when it fails,
// the resource is kept with the requested users as candidates to clean up.
func TestGroupMembersResource_UnusableIDInAnAcceptedAnswerIsVerified(t *testing.T) {
	unusable := func(s *gmServer) map[string]any {
		group := s.groupJSON()
		group["users"] = []any{map[string]any{"id": "not-a-user-id"}}
		return group
	}

	t.Run("verification read succeeds", func(t *testing.T) {
		s, c := newGMServer(t)
		s.putBody = unusable(s)
		r, sch := gmResource(t, c)
		resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101)})
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		require.Len(t, resp.Diagnostics.Warnings(), 1, "the unusable answer is reported")
		assert.Equal(t, []string{gmUUID(101)}, gmStateIDs(t, resp.State))
		assert.Equal(t, 1, s.putCount(), "never sent twice")
	})
	t.Run("verification read fails", func(t *testing.T) {
		s, c := newGMServer(t)
		s.putBody = unusable(s)
		s.failGetsAfterPut = true
		r, sch := gmResource(t, c)
		resp := gmCreate(t, r, sch, s.groupID, []string{gmUUID(101)})
		require.True(t, resp.Diagnostics.HasError())
		assert.Equal(t, "Group members may have changed", resp.Diagnostics.Errors()[0].Summary())
		assert.NotContains(t, resp.Diagnostics.Errors()[0].Detail(), "Nothing was changed")
		require.False(t, resp.State.Raw.IsNull(), "the accepted write keeps the resource")
		candidates, null := gmStateUnresolved(t, resp.State)
		assert.False(t, null)
		assert.Equal(t, []string{gmUUID(101)}, candidates)
		assert.Equal(t, []string{gmUUID(101)}, s.memberSet(), "the write took effect")
	})
}

// A group and its members addressed in another letter case than the server's
// are the same group and users (PostgreSQL answers with its own lower-case
// spelling): the plan is not taken for a difference, and the write and its
// verification address the group the read described, in the server's
// spelling.
func TestGroupMembersResource_IDsInAnotherCaseAreTheSame(t *testing.T) {
	const group = "abcdef01-0000-4000-8000-0000000000aa"
	const lettered = "abcdef02-0000-4000-8000-0000000000bb"
	s, c := newGMServer(t)
	s.anyCase = true
	s.groupID = group
	s.users[lettered] = true
	r, sch := gmResource(t, c)
	resp := gmCreate(t, r, sch, strings.ToUpper(group), []string{strings.ToUpper(lettered), gmUUID(102)})
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"/api/user-groups/" + group + "/users"}, s.putPaths, "the PUT uses the server's spelling")
	assert.Equal(t, []string{gmUUID(102), lettered}, s.memberSet())
}
