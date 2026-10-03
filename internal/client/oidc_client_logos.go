package client

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// clientLogoRequestLimit is the size limit Pocket ID puts on a logo upload
// request (middleware.FileSizeLimitMiddleware.Add(2<<20) on
// POST /oidc/clients/:id/logo, 2.14.0 to 2.17.0). It applies to the whole
// multipart body, not just the file.
const clientLogoRequestLimit = 2 << 20

// ClientLogoMaxBytes is the largest logo file this client uploads: the
// server's request limit less room for the multipart framing around the
// file (boundaries and part headers, a few hundred bytes).
const ClientLogoMaxBytes = clientLogoRequestLimit - 1024

// clientLogoTypes are the file extensions Pocket ID accepts for a client
// logo, with their media types (utils.GetImageMimeType, unchanged 2.14.0 to
// 2.17.0). The server takes the type from the uploaded file name's
// extension, lower-cased, and refuses any other with HTTP 415.
var clientLogoTypes = map[string]string{
	"avif": "image/avif",
	"gif":  "image/gif",
	"heic": "image/heic",
	"ico":  "image/x-icon",
	"jpeg": "image/jpeg",
	"jpg":  "image/jpeg",
	"png":  "image/png",
	"svg":  "image/svg+xml",
	"webp": "image/webp",
}

// ClientLogoExtensions lists the accepted logo file extensions, sorted.
func ClientLogoExtensions() []string {
	extensions := make([]string, 0, len(clientLogoTypes))
	for extension := range clientLogoTypes {
		extensions = append(extensions, extension)
	}
	sort.Strings(extensions)
	return extensions
}

// ClientLogoMediaType returns the media type Pocket ID serves a logo with
// for a file extension (with or without its leading dot, in any case), and
// whether the server accepts that extension at all.
func ClientLogoMediaType(extension string) (string, bool) {
	mediaType, ok := clientLogoTypes[strings.ToLower(strings.TrimPrefix(extension, "."))]
	return mediaType, ok
}

func clientLogoEndpoint(clientID string, light bool) (string, error) {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/oidc/clients/%s/logo?light=%t", id, light), nil
}

// UploadClientLogo sets the light (light = true) or dark logo of an OIDC
// client from an image file's content, through a multipart upload to
// POST /api/oidc/clients/{id}/logo?light=... . extension names the image
// type (see ClientLogoMediaType). Pocket ID strips metadata from JPEG, PNG
// and WebP images before storing them, so the stored image is not
// byte-for-byte the uploaded one.
//
// Nothing is sent for an unknown extension or a file over
// ClientLogoMaxBytes. The upload is never retried: after a failure other
// than a definite rejection the logo may or may not have been replaced.
func (c *Client) UploadClientLogo(ctx context.Context, clientID string, light bool, extension string, content []byte) error {
	endpoint, err := clientLogoEndpoint(clientID, light)
	if err != nil {
		return err
	}
	extension = strings.ToLower(strings.TrimPrefix(extension, "."))
	mediaType, ok := clientLogoTypes[extension]
	if !ok {
		return fmt.Errorf("%w: Pocket ID accepts client logos of these types only: %s", ErrInvalidUpload, strings.Join(ClientLogoExtensions(), ", "))
	}
	_, err = c.upload(ctx, "POST", endpoint, MultipartFile{
		FieldName:   "file",
		FileName:    "logo." + extension,
		ContentType: mediaType,
		Content:     content,
	}, ClientLogoMaxBytes)
	return err
}

// GetClientLogo returns the image Pocket ID serves now as the light
// (light = true) or dark logo of an OIDC client, from
// GET /api/oidc/clients/{id}/logo?light=... . Without a light logo it answers
// ResourceImage's not-found error. Asked for the dark logo of a client that
// has none, it serves the light logo instead, so check
// OIDCClient.HasDarkLogo before relying on what it returns.
//
// Pocket ID lets any cache keep a logo for 15 minutes and serve it stale for
// 12 hours more (utils.SetCacheControlHeader), so a cache between the
// provider and the server could answer with an earlier logo. The read
// therefore goes through getBinaryUncached: a URL no cache has seen (a random
// nocache parameter, which Pocket ID ignores) and request headers that ask
// caches to revalidate (Cache-Control: no-cache, Pragma: no-cache). Like every
// read it is retried after a transient failure, with a new nocache value each
// time.
func (c *Client) GetClientLogo(ctx context.Context, clientID string, light bool) ([]byte, error) {
	id, err := clientIDSegment(clientID)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("light", strconv.FormatBool(light))
	body, _, err := c.getBinaryUncached(ctx, "/api/oidc/clients/"+id+"/logo", query, 0)
	return body, err
}

// DeleteClientLogo removes the light (light = true) or dark logo of an OIDC
// client. Pocket ID answers ResourceImage's not-found error when the client
// has no such logo, and ResourceOIDCClient's when the client is gone. The
// DELETE is never retried.
func (c *Client) DeleteClientLogo(ctx context.Context, clientID string, light bool) error {
	endpoint, err := clientLogoEndpoint(clientID, light)
	if err != nil {
		return err
	}
	_, err = c.doRequest(ctx, "DELETE", endpoint, nil)
	return err
}
