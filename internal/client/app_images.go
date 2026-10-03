package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"
)

// ApplicationImage names one of Pocket ID's application images
// (controller/app_images_controller.go, the same in 2.14.0 to 2.17.0).
type ApplicationImage string

const (
	ApplicationImageLogoLight             ApplicationImage = "logo_light"
	ApplicationImageLogoDark              ApplicationImage = "logo_dark"
	ApplicationImageEmailLogo             ApplicationImage = "email_logo"
	ApplicationImageBackground            ApplicationImage = "background"
	ApplicationImageFavicon               ApplicationImage = "favicon"
	ApplicationImageDefaultProfilePicture ApplicationImage = "default_profile_picture"
)

// applicationImageExtensions is Pocket ID's utils.GetImageMimeType: the file
// extensions it accepts for an image, and the type it serves each as. The
// server takes an upload's type from its file name, not its content.
var applicationImageExtensions = map[string]string{
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"svg":  "image/svg+xml",
	"ico":  "image/x-icon",
	"gif":  "image/gif",
	"webp": "image/webp",
	"avif": "image/avif",
	"heic": "image/heic",
}

type applicationImageRoute struct {
	// path follows /api/application-images/.
	path string
	// light is the logo's ?light= value; empty for other images.
	light string
	// extensions the upload handler accepts; nil for every extension in
	// applicationImageExtensions.
	extensions []string
	// deletable: the server has a DELETE route for the image (logo,
	// background and default profile picture only).
	deletable bool
}

var applicationImageRoutes = map[ApplicationImage]applicationImageRoute{
	ApplicationImageLogoLight:  {path: "logo", light: "true", deletable: true},
	ApplicationImageLogoDark:   {path: "logo", light: "false", deletable: true},
	ApplicationImageEmailLogo:  {path: "email", extensions: []string{"jpeg", "jpg", "png"}},
	ApplicationImageBackground: {path: "background", deletable: true},
	// updateFaviconHandler accepts SVG, PNG and ICO.
	ApplicationImageFavicon:               {path: "favicon", extensions: []string{"ico", "png", "svg"}},
	ApplicationImageDefaultProfilePicture: {path: "default-profile-picture", deletable: true},
}

// MaxApplicationImageBytes is the largest image this provider uploads.
// Pocket ID itself sets no size limit on application images.
const MaxApplicationImageBytes = maxUploadBytes

// ApplicationImages returns every image name, sorted.
func ApplicationImages() []ApplicationImage {
	images := make([]ApplicationImage, 0, len(applicationImageRoutes))
	for image := range applicationImageRoutes {
		images = append(images, image)
	}
	sort.Slice(images, func(i, j int) bool { return images[i] < images[j] })
	return images
}

// Valid reports whether image is one of Pocket ID's application images.
func (image ApplicationImage) Valid() bool {
	_, ok := applicationImageRoutes[image]
	return ok
}

// Extensions returns the lower-case file extensions Pocket ID accepts for
// image, sorted.
func (image ApplicationImage) Extensions() []string {
	route := applicationImageRoutes[image]
	if route.extensions != nil {
		return slices.Clone(route.extensions)
	}
	extensions := make([]string, 0, len(applicationImageExtensions))
	for extension := range applicationImageExtensions {
		extensions = append(extensions, extension)
	}
	sort.Strings(extensions)
	return extensions
}

// Deletable reports whether Pocket ID can remove image. The e-mail logo and
// the favicon have no DELETE route: they can only be replaced.
func (image ApplicationImage) Deletable() bool {
	return applicationImageRoutes[image].deletable
}

// ErrApplicationImageNotDeletable is returned for an image Pocket ID cannot
// remove; nothing is sent.
var ErrApplicationImageNotDeletable = errors.New("this image cannot be removed: Pocket ID only replaces it")

func (image ApplicationImage) endpoint(custom bool) (string, error) {
	route, ok := applicationImageRoutes[image]
	if !ok {
		return "", fmt.Errorf("%w: unknown application image %q", ErrInvalidIdentifier, string(image))
	}
	endpoint := "/api/application-images/" + route.path
	query := ""
	if route.light != "" {
		query = "light=" + route.light
	}
	if custom {
		// Pocket ID 2.15.0+ answers a GET without it with its bundled
		// logo when none was uploaded; 2.14.0 ignores it and never does.
		if query != "" {
			query += "&"
		}
		query += "default=false"
		// Pocket ID lets caches keep an image for 15 minutes and serve it
		// stale for a day (utils.SetCacheControlHeader). A URL no cache
		// has seen gets the image as it is now, so an upload is not
		// checked against, nor a refresh compared with, an older copy, by
		// a cache whose key includes the query string. A cache that leaves
		// this parameter out of its key still can answer from its copy;
		// request headers asking caches not to (Cache-Control: no-cache)
		// need a per-request header option in transport.go. Pocket ID
		// ignores the parameter.
		query += "&nocache=" + applicationImageNonce()
	}
	if query != "" {
		endpoint += "?" + query
	}
	return endpoint, nil
}

// applicationImageNonce returns a value no earlier request used.
func applicationImageNonce() string {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a time-based
		// value still differs between requests.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(nonce[:])
}

// UploadApplicationImage replaces image with content, a file whose type is
// given by extension (lower case, one Pocket ID accepts for the image). It is
// sent once and never retried.
func (c *Client) UploadApplicationImage(ctx context.Context, image ApplicationImage, extension string, content []byte) error {
	endpoint, err := image.endpoint(false)
	if err != nil {
		return err
	}
	if !slices.Contains(image.Extensions(), extension) {
		return fmt.Errorf("%w: Pocket ID does not accept a .%s file for the %s", ErrInvalidUpload, extension, string(image))
	}
	_, err = c.upload(ctx, http.MethodPut, endpoint, MultipartFile{
		FieldName:   "file",
		FileName:    "image." + extension,
		ContentType: applicationImageExtensions[extension],
		Content:     content,
	}, MaxApplicationImageBytes)
	return err
}

// GetApplicationImage returns the image that was uploaded for image, as
// Pocket ID serves it now (each request has a URL of its own, so a cache
// keyed on the whole URL does not answer it) (it strips metadata from JPEG, PNG and WebP files on
// upload, so the bytes can differ from the file sent). When there is none, the
// error satisfies IsNotFound(err, ResourceImage). Pocket ID copies its bundled
// e-mail logo, favicon and background into place at startup unless an
// administrator deleted them, so those are always found.
func (c *Client) GetApplicationImage(ctx context.Context, image ApplicationImage) ([]byte, error) {
	endpoint, err := image.endpoint(true)
	if err != nil {
		return nil, err
	}
	return c.doRequest(ctx, http.MethodGet, endpoint, nil)
}

// DeleteApplicationImage removes image. An image that is already gone
// answers IsNotFound(err, ResourceImage). For an image Pocket ID cannot remove
// it returns ErrApplicationImageNotDeletable without a request.
func (c *Client) DeleteApplicationImage(ctx context.Context, image ApplicationImage) error {
	endpoint, err := image.endpoint(false)
	if err != nil {
		return err
	}
	if !image.Deletable() {
		return ErrApplicationImageNotDeletable
	}
	_, err = c.doRequest(ctx, http.MethodDelete, endpoint, nil)
	return err
}
