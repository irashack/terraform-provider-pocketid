package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// nocacheParameter is the query parameter getBinaryUncached adds with a fresh
// random value, so that no cache between the provider and Pocket ID can
// answer from a stored copy.
const nocacheParameter = "nocache"

// getBinaryUncached reads a binary resource (an image) as the server holds it
// right now. Pocket ID marks its image GETs publicly cacheable, so the request
// bypasses caches twice over: the query gets a random nocache parameter, and
// the request carries Cache-Control: no-cache and Pragma: no-cache.
//
// endpoint is a path without a query; query holds the endpoint's own
// parameters (for example light=false) and is not modified. At most maxBytes
// of a 2xx body are read (0, or anything above the general response limit,
// means that limit); a larger body is a ResponseBodyError with TooLarge set.
// It returns the body and the response's media type (lower case, without
// parameters, empty when it is missing, malformed or contains the API key);
// the media type is server-controlled, so compare it rather than log it.
//
// It is never retried, unlike other reads: the answer is used as evidence of
// what the server holds at this moment, and a second attempt could be served
// by a different cache or after a change, so a failure is reported instead.
// Otherwise it goes through the same path as every request: one connection
// per request, the read deadline, sanitized errors and logs, and the usual
// classification of a non-2xx answer (IsNotFound with ResourceImage for a
// missing image).
func (c *Client) getBinaryUncached(ctx context.Context, endpoint string, query url.Values, maxBytes int64) ([]byte, string, error) {
	if strings.ContainsAny(endpoint, "?#") {
		return nil, "", errors.New("getBinaryUncached: endpoint must be a path; pass its parameters in query")
	}
	if maxBytes <= 0 || maxBytes > maxResponseBodyBytes {
		maxBytes = maxResponseBodyBytes
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", fmt.Errorf("getBinaryUncached: could not generate a cache-busting value: %w", err)
	}
	values := url.Values{}
	for key, list := range query {
		values[key] = append([]string(nil), list...)
	}
	values.Set(nocacheParameter, hex.EncodeToString(nonce))

	policy := c.retry
	if policy.maxAttempts == 0 {
		policy = defaultRetryPolicy
	}
	ctx, cancel := context.WithTimeout(ctx, policy.maxElapsed)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, "", fmt.Errorf("request not sent: %w", err)
	}

	headers := http.Header{}
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Pragma", "no-cache")
	body, contentType, err := c.sendWith(ctx, http.MethodGet, endpoint+"?"+values.Encode(), "", nil,
		sendOptions{accept: "*/*", headers: headers, maxBody: maxBytes})
	if err != nil {
		return nil, "", err
	}
	return body, c.mediaType(contentType), nil
}

// mediaType returns the media type of a Content-Type header in lower case and
// without parameters, or "" when it is missing, malformed, not a type/subtype
// pair or contains the API key. mime.ParseMediaType also parses
// Content-Disposition values, so it accepts a bare token such as
// "attachment"; a media type needs the slash, and its parser has already
// checked that both sides of it are tokens.
func (c *Client) mediaType(header string) string {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil || !strings.Contains(mediaType, "/") ||
		(c.apiToken != "" && strings.Contains(mediaType, strings.ToLower(c.apiToken))) {
		return "" // ParseMediaType lower-cases, so compare the key in lower case
	}
	return mediaType
}
