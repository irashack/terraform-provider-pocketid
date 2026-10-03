package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

// UserProfilePictureMaxBytes is the largest profile picture file the client
// sends. Pocket ID sets no limit of its own on this upload (the size-limit
// middleware guards client logos only); this is the cap of the client's upload
// helper, and the server's other limit is on pixels (see
// UserProfilePictureMaxPixels).
const UserProfilePictureMaxBytes = maxUploadBytes

// UserProfilePictureMaxPixels is the most pixels (width times height) Pocket ID
// 2.15 and later accept in an uploaded picture; it answers 400 invalid_image
// above it. Version 2.14 has no such limit.
const UserProfilePictureMaxPixels = 16_000_000

// UploadUserProfilePicture replaces a user's profile picture with file, through
// PUT /api/users/{id}/profile-picture. Pocket ID decodes the image itself
// (whatever the file name or media type say), scales and crops it to a
// 300-by-300 PNG and stores that, so what it serves afterwards is not the file
// that was sent. A user that does not exist is an error satisfying
// IsUserNotFound; an image the server cannot read is HTTP 400 with the code
// invalid_image. The upload is never retried.
func (c *Client) UploadUserProfilePicture(ctx context.Context, userID string, file MultipartFile) error {
	id, err := uuidSegment("user", userID)
	if err != nil {
		return err
	}
	file.FieldName = "file"
	_, err = c.upload(ctx, "PUT", "/api/users/"+id+"/profile-picture", file, UserProfilePictureMaxBytes)
	return err
}

// ResetUserProfilePicture removes a user's custom profile picture, through
// DELETE /api/users/{id}/profile-picture, so the default is served again. It
// succeeds when the user has no custom picture. A user that does not exist is an
// error satisfying IsUserNotFound.
func (c *Client) ResetUserProfilePicture(ctx context.Context, userID string) error {
	id, err := uuidSegment("user", userID)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", "/api/users/"+id+"/profile-picture", nil)
	return err
}

// GetUserProfilePicture returns the picture Pocket ID serves for a user, from
// GET /api/users/{id}/profile-picture.png: the custom picture if there is one,
// else the instance's default picture, else one generated from the user's
// initials. The server says nothing about which of the three it is. A user
// that does not exist is an error satisfying IsUserNotFound.
//
// Pocket ID lets any cache keep this picture for 15 minutes and serve it stale
// for an hour more (utils.SetCacheControlHeader, 2.14.0 to 2.17.0), and the
// upload goes to another URL, so a cache between the provider and the server can
// answer with the picture from before an upload. Each call therefore uses a URL
// no cache has seen (a random nocache parameter, which Pocket ID ignores) and
// asks caches to revalidate (Cache-Control: no-cache, Pragma: no-cache).
func (c *Client) GetUserProfilePicture(ctx context.Context, userID string) ([]byte, error) {
	id, err := uuidSegment("user", userID)
	if err != nil {
		return nil, err
	}
	return c.profilePictureUncached().doRequest(ctx, "GET", "/api/users/"+id+"/profile-picture.png?nocache="+profilePictureNonce(), nil)
}

// profilePictureNonce returns a value no earlier request used.
func profilePictureNonce() string {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a time-based value
		// still differs between requests.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(nonce[:])
}

// profilePictureUncached returns a copy of c whose requests carry the headers
// that make caches revalidate. Requests are built in transport.go, so the
// headers are added on the way out instead.
func (c *Client) profilePictureUncached() *Client {
	httpClient := *c.httpClient
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = profilePictureNoCacheTransport{base: base}
	uncached := *c
	uncached.httpClient = &httpClient
	return &uncached
}

// profilePictureNoCacheTransport adds Cache-Control: no-cache and
// Pragma: no-cache to every request it sends.
type profilePictureNoCacheTransport struct {
	base http.RoundTripper
}

func (t profilePictureNoCacheTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not change the caller's request.
	req = req.Clone(req.Context())
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	return t.base.RoundTrip(req)
}
