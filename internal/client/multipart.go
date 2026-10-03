package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// maxUploadBytes is the most any upload sends, whatever the caller allows.
// Pocket ID's own limits are lower (2 MiB for a client logo, through
// middleware.FileSizeLimitMiddleware).
const maxUploadBytes = 10 << 20

// MultipartFile is the one file part of a multipart/form-data upload.
type MultipartFile struct {
	// FieldName is the form field. Pocket ID's upload endpoints (client logo,
	// application images, profile picture) all read "file".
	FieldName string
	// FileName is sent in the part header. Pocket ID takes the image type from
	// its extension (utils.GetFileExtension), so "logo.png" matters where
	// "logo" would be refused. It must be a plain name: no path separators,
	// quotes or control characters.
	FileName string
	// ContentType is the part's media type; empty sends
	// application/octet-stream.
	ContentType string
	// Content is the file's bytes.
	Content []byte
}

// ErrInvalidUpload marks an upload refused before anything was sent.
var ErrInvalidUpload = errors.New("invalid upload")

func (f MultipartFile) check(maxBytes int64) error {
	if f.FieldName == "" || strings.ContainsAny(f.FieldName, "\"\\/;=") || hasControl(f.FieldName) {
		return fmt.Errorf("%w: the form field name must be a plain, non-empty word", ErrInvalidUpload)
	}
	if f.FileName == "" || len(f.FileName) > 255 || strings.ContainsAny(f.FileName, "\"\\/") || hasControl(f.FileName) || f.FileName == "." || f.FileName == ".." {
		return fmt.Errorf("%w: the file name must be a plain name without path separators, quotes or control characters", ErrInvalidUpload)
	}
	if f.ContentType != "" {
		if hasControl(f.ContentType) {
			return fmt.Errorf("%w: the content type contains control characters", ErrInvalidUpload)
		}
		if _, _, err := mime.ParseMediaType(f.ContentType); err != nil {
			return fmt.Errorf("%w: the content type is not a media type", ErrInvalidUpload)
		}
	}
	if int64(len(f.Content)) > maxBytes {
		return fmt.Errorf("%w: %d bytes is over the %d-byte limit", ErrInvalidUpload, len(f.Content), maxBytes)
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// upload sends one file as multipart/form-data to endpoint (which may carry
// a query, such as "?light=false") and returns the response body. method must
// be POST or PUT, the methods Pocket ID's upload endpoints use and ones Go's
// transport never replays; the body is a one-shot reader (see send). It is
// never retried, whatever the answer: a failed upload is reported, not
// repeated. maxBytes is the endpoint's own size limit (0 or more than
// maxUploadBytes means maxUploadBytes); a larger file is refused before
// anything is sent. Errors, logging, size limits on the response and the
// redaction of the API key are those of every other request.
func (c *Client) upload(ctx context.Context, method, endpoint string, file MultipartFile, maxBytes int64) ([]byte, error) {
	if method != http.MethodPost && method != http.MethodPut {
		return nil, fmt.Errorf("%w: an upload is sent with POST or PUT", ErrInvalidUpload)
	}
	if maxBytes <= 0 || maxBytes > maxUploadBytes {
		maxBytes = maxUploadBytes
	}
	if err := file.check(maxBytes); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("request not sent: %w", err)
	}

	contentType := file.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	// check refused quotes, backslashes and control characters, so the names
	// need no escaping here.
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, file.FieldName, file.FileName))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, fmt.Errorf("error building upload: %w", err)
	}
	if _, err := part.Write(file.Content); err != nil {
		return nil, fmt.Errorf("error building upload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("error building upload: %w", err)
	}

	return c.send(ctx, method, endpoint, writer.FormDataContentType(), body.Bytes())
}
