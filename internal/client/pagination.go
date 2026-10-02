package client

import (
	"context"
	"encoding/json"
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
	// listAttempts is how many times a walk is repeated when the list changed
	// under it (an object appeared twice, or the count did not add up).
	listAttempts = 2
)

// errListChanged reports a walk whose pages did not add up, which happens when
// objects are created or deleted between page requests.
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
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	return &result, nil
}

// listAll returns every object of a paginated list endpoint (GET only), in
// the order the server sorts them. query holds the endpoint's own filters
// (for example "search"); listAll adds the page, the page size (Pocket ID's
// maximum) and a sort by creation time, so pages do not shift between
// requests. id returns an object's ID, used to detect overlap between pages.
//
// It fails rather than return a partial list: on a pagination block that is
// missing, inconsistent or does not advance, on more than maxListPages pages,
// and when the pages do not add up (an object seen twice, or a total that
// differs from what was collected) twice in a row. what names the objects in
// errors, for example "user groups".
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
