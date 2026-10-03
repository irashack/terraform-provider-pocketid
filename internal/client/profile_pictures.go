package client

import (
	"context"
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
func (c *Client) GetUserProfilePicture(ctx context.Context, userID string) ([]byte, error) {
	id, err := uuidSegment("user", userID)
	if err != nil {
		return nil, err
	}
	return c.doRequest(ctx, "GET", "/api/users/"+id+"/profile-picture.png", nil)
}
