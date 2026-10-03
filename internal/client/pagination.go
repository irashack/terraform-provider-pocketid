package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// PaginationInfo represents pagination metadata
type PaginationInfo struct {
	TotalPages   int `json:"totalPages"`
	TotalItems   int `json:"totalItems"`
	CurrentPage  int `json:"currentPage"`
	ItemsPerPage int `json:"itemsPerPage"`
}

// PaginatedResponse represents a paginated API response
type PaginatedResponse[T any] struct {
	Data       []T            `json:"data"`
	Pagination PaginationInfo `json:"pagination"`
}

const (
	// maxPageSize is the largest page Pocket ID serves: utils.Paginate clamps
	// pagination[limit] to 100 (and uses 20 when it is absent), unchanged from
	// v2.14.0 to v2.17.0.
	maxPageSize = 100
	// maxListPages bounds one walk: 100,000 objects at the largest page size.
	maxListPages = 1000
	// listAttempts is how many times a walk is made when its pages did not
	// add up (an object appeared twice, or the count differed from the
	// server's total).
	listAttempts = 2
)

// errListChanged reports a walk whose pages did not add up: an object
// appeared on two pages, or the number collected differed from the server's
// total. Objects created or deleted between page requests cause it.
var errListChanged = errors.New("the list changed while it was being read")

// getPage fetches one page of a paginated list endpoint. query is copied, not
// modified.
func getPage[T any](ctx context.Context, c *Client, endpoint string, query url.Values, page, limit int) (*PaginatedResponse[T], error) {
	values := url.Values{}
	for key, list := range query {
		values[key] = append([]string(nil), list...)
	}
	if page > 0 {
		values.Set("pagination[page]", strconv.Itoa(page))
	}
	if limit > 0 {
		values.Set("pagination[limit]", strconv.Itoa(limit))
	}
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	body, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	var result PaginatedResponse[T]
	if err := decodeResponse(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// listAll returns the objects of a paginated list endpoint (GET only) by
// requesting every page the server reports, in the order the server sorts
// them. query holds the endpoint's own filters (for example "search");
// listAll adds the page, the page size (Pocket ID's maximum) and a sort by
// creation time, so that updates to existing objects and objects created
// during the walk (which sort last) do not shift earlier pages. id returns an
// object's ID. what names the objects in errors, for example "user groups".
//
// What it guarantees:
//   - Every object's ID (as id returns it) passes checkReturnedID's rules
//     before anything is returned: it does not contain the API key, and it
//     has a form a Pocket ID object ID takes (a UUID, a client ID or a CIMD
//     client's URL; listAll does not know which kind it lists). Any other ID
//     fails the whole list. IDs nested inside the objects are the caller's
//     to check.
//   - It ends: it refuses a pagination block that is missing or inconsistent,
//     a page number the server did not honor (a server that never advances),
//     and more than maxListPages pages.
//   - It never returns an object twice, and never returns fewer objects than
//     the server's total on the last page without failing: either case reads
//     the list once more, then is an error.
//
// What it does not guarantee: Pocket ID's lists are offset pages with no
// snapshot or cursor, so a walk is not atomic. When an object on an earlier
// page is deleted while the walk is in progress, later objects move back one
// place and the one at a page boundary is not seen; if another object is
// created at the same time, the counts still agree and the list is returned
// without it. A list read while nothing else changes it is complete.
func listAll[T any](ctx context.Context, c *Client, what, endpoint string, query url.Values, id func(T) string) ([]T, error) {
	sorted := url.Values{}
	for key, list := range query {
		sorted[key] = list
	}
	// CreatedAt is sortable on every model (model.Base); a column the server
	// does not know is ignored, never an error.
	sorted.Set("sort[column]", "createdAt")
	sorted.Set("sort[direction]", "asc")

	var err error
	for attempt := 1; attempt <= listAttempts; attempt++ {
		var all []T
		all, err = walkPages(ctx, c, what, endpoint, sorted, id)
		if err == nil {
			return all, nil
		}
		if !errors.Is(err, errListChanged) {
			return nil, err
		}
	}
	return nil, err
}

func walkPages[T any](ctx context.Context, c *Client, what, endpoint string, query url.Values, id func(T) string) ([]T, error) {
	var all []T
	seen := map[string]struct{}{}
	for page := 1; page <= maxListPages; page++ {
		resp, err := getPage[T](ctx, c, endpoint, query, page, maxPageSize)
		if err != nil {
			return nil, err
		}
		info := resp.Pagination
		if info.TotalPages <= 0 || info.TotalItems < 0 || info.CurrentPage != page {
			return nil, fmt.Errorf("listing %s: page %d came back with an unusable pagination block (currentPage %d, totalPages %d, totalItems %d); refusing to guess whether the list is complete",
				what, page, info.CurrentPage, info.TotalPages, info.TotalItems)
		}
		for _, item := range resp.Data {
			key := id(item)
			// Every object ID goes through checkReturnedID's rules; a list
			// can hold any kind, so any form an object ID takes is accepted.
			if err := c.checkResponseID("object", isOIDCClientID, "", key); err != nil {
				return nil, fmt.Errorf("listing %s: %w", what, err)
			}
			if _, dup := seen[key]; dup {
				return nil, fmt.Errorf("listing %s: %w (an object appeared on two pages)", what, errListChanged)
			}
			seen[key] = struct{}{}
		}
		all = append(all, resp.Data...)

		if page >= info.TotalPages || len(resp.Data) == 0 {
			if len(all) != info.TotalItems {
				return nil, fmt.Errorf("listing %s: %w (%d collected, %d reported)", what, errListChanged, len(all), info.TotalItems)
			}
			return all, nil
		}
	}
	return nil, fmt.Errorf("listing %s did not end after %d pages; refusing to read further", what, maxListPages)
}
