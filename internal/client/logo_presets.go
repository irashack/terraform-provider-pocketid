package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// LogoPreset is one icon of Pocket ID's icon library (Pocket ID 2.18.0 and
// later): the collection set by ICON_LIBRARY_URL, by default the selfh.st
// icons through jsDelivr, from which the admin interface offers OIDC client
// logos. LogoURL is the icon's regular image; DarkLogoURL, when set, its white
// variant for dark backgrounds. Both are absolute URLs into the library, built
// by the server from its configured base URL.
type LogoPreset struct {
	Name        string  `json:"name"`
	Reference   string  `json:"reference"`
	LogoURL     string  `json:"logoUrl"`
	DarkLogoURL *string `json:"darkLogoUrl"`
}

func (p *LogoPreset) shownTexts() []string {
	return []string{p.Name, p.Reference, p.LogoURL, optionalText(p.DarkLogoURL)}
}

// logoPresetReferencePattern is the server's own rule for a reference
// (logopreset.referencePattern in 2.18.0): selfh.st's file-name slugs. The
// server skips index entries that do not match, so no other reference can
// ever be found.
var logoPresetReferencePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidLogoPresetReference reports whether reference has the form of an icon
// library reference, such as "jellyfin" or "home-assistant".
func ValidLogoPresetReference(reference string) bool {
	return logoPresetReferencePattern.MatchString(reference)
}

// Error codes of the logo preset search (apperror in 2.18.0).
const (
	codeLogoPresetsDisabled    = "logo_presets_disabled"
	codeLogoPresetsUnavailable = "logo_presets_unavailable"
)

// IsLogoPresetsDisabled reports whether err is Pocket ID's answer that its
// icon library is turned off (ICON_LIBRARY_URL=disabled): HTTP 403 with the
// code logo_presets_disabled.
func IsLogoPresetsDisabled(err error) bool {
	var status *HTTPError
	return errors.As(err, &status) && status.StatusCode == http.StatusForbidden && status.Code == codeLogoPresetsDisabled
}

// IsLogoPresetsUnavailable reports whether err is Pocket ID's answer that it
// could not load the icon library's index: HTTP 502 with the code
// logo_presets_unavailable.
func IsLogoPresetsUnavailable(err error) bool {
	var status *HTTPError
	return errors.As(err, &status) && status.StatusCode == http.StatusBadGateway && status.Code == codeLogoPresetsUnavailable
}

// IsMissingEndpoint reports whether err is the router's own 404 for an /api
// route the server does not have: an older Pocket ID, or a wrong base URL.
func IsMissingEndpoint(err error) bool {
	var status *HTTPError
	return errors.As(err, &status) && status.MissingEndpoint
}

// SearchLogoPresets searches Pocket ID's icon library through
// GET /api/oidc/logo-presets?search=... (Pocket ID 2.18.0 and later). The
// server ranks the matches (an exact name or reference first, then prefixes,
// substrings and tags; an empty search lists the collection alphabetically)
// and returns at most 30 of them, so a caller looking for one icon must
// compare references itself.
//
// The server loads the library's index.json itself and keeps it for a day.
// IsLogoPresetsDisabled and IsLogoPresetsUnavailable classify its two
// refusals; IsMissingEndpoint a server older than 2.18.0.
func (c *Client) SearchLogoPresets(ctx context.Context, search string) ([]LogoPreset, error) {
	if c.containsKey(search) {
		return nil, fmt.Errorf("%w: the search contains the API key this provider sends; nothing was sent", ErrInvalidIdentifier)
	}
	endpoint := "/api/oidc/logo-presets"
	if search != "" {
		endpoint += "?" + url.Values{"search": {search}}.Encode()
	}
	body, err := c.doRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	var presets []LogoPreset
	if err := json.Unmarshal(body, &presets); err != nil {
		return nil, fmt.Errorf("error decoding the logo presets: %w", err)
	}
	for i := range presets {
		if err := c.checkReturnedText(presets[i].shownTexts()...); err != nil {
			return nil, err
		}
	}
	return presets, nil
}

// FindLogoPreset returns the icon whose reference is exactly reference, and
// whether there is one.
func (c *Client) FindLogoPreset(ctx context.Context, reference string) (*LogoPreset, bool, error) {
	presets, err := c.SearchLogoPresets(ctx, reference)
	if err != nil {
		return nil, false, err
	}
	for i := range presets {
		if presets[i].Reference == reference {
			return &presets[i], true, nil
		}
	}
	return nil, false, nil
}

// logoDownloadTimeout bounds one icon download, redirects included.
const logoDownloadTimeout = 30 * time.Second

// logoDownloadMaxRedirects is how many redirects an icon download follows.
const logoDownloadMaxRedirects = 5

// DownloadLogo fetches an icon library image from rawURL, an absolute http or
// https URL as LogoPreset gives it, for an upload with UploadClientLogo. It
// returns the content and the image type Pocket ID would take from the URL's
// file name (see ClientLogoMediaType).
//
// The download is made by the provider, not by Pocket ID, and is unrelated to
// the Pocket ID API: it goes through a client of its own that never sends the
// API key or any other credential, never verifies TLS less strictly than
// usual (skip_tls_verify applies to Pocket ID only), follows at most a few
// redirects and only to http or https, and reads at most ClientLogoMaxBytes.
// A URL that carries the API key is refused before anything is sent. Errors
// name the URL's host, never its body.
func (c *Client) DownloadLogo(ctx context.Context, rawURL string) ([]byte, string, error) {
	if c.containsKey(rawURL) {
		return nil, "", fmt.Errorf("%w: the icon URL contains the API key this provider sends; nothing was downloaded", ErrInvalidIdentifier)
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, "", errors.New("the icon URL is not an absolute http or https URL without credentials")
	}
	extension := strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))
	if _, ok := ClientLogoMediaType(extension); !ok {
		return nil, "", fmt.Errorf("the icon URL on %s does not name an image type Pocket ID accepts for a logo (%s)", u.Host, strings.Join(ClientLogoExtensions(), ", "))
	}

	ctx, cancel := context.WithTimeout(ctx, logoDownloadTimeout)
	defer cancel()
	downloader := &http.Client{
		Transport: newTransport(&tls.Config{MinVersion: tls.VersionTLS12}),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= logoDownloadMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", logoDownloadMaxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirected to a URL that is not http or https")
			}
			if c.containsKey(req.URL.String()) {
				return errors.New("redirected to a URL that contains the API key")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(withRequestContext(ctx), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("the icon download from %s could not be prepared", u.Host)
	}
	req.Header.Set("User-Agent", "terraform-provider-pocketid")
	req.Close = true
	resp, err := downloader.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, "", fmt.Errorf("downloading the icon from %s failed: %s", u.Host, sanitizeDownloadError(c, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("downloading the icon from %s failed: HTTP %d", u.Host, resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, int64(ClientLogoMaxBytes)+1))
	if err != nil {
		return nil, "", fmt.Errorf("downloading the icon from %s failed while reading it", u.Host)
	}
	if len(content) > ClientLogoMaxBytes {
		return nil, "", fmt.Errorf("the icon from %s is larger than %d bytes, the most Pocket ID accepts for a logo", u.Host, ClientLogoMaxBytes)
	}
	if len(content) == 0 {
		return nil, "", fmt.Errorf("the icon from %s is empty", u.Host)
	}
	return content, extension, nil
}

// sanitizeDownloadError returns a transport error's text, or a generic one
// when the text carries the API key.
func sanitizeDownloadError(c *Client, err error) string {
	text := err.Error()
	if c.containsKey(text) {
		return "the error's text is withheld"
	}
	return text
}
