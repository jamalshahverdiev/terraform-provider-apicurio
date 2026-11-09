package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// pageLimit is the page size used when following offset/limit paging.
const pageLimit = 100

// listAll follows the offset/limit paging of a list endpoint and returns
// every item found under key (`groups`, `artifacts`, `versions`, ...). The
// `count` field is ignored: 3.3 can skip it (`skipCount`), and a page shorter
// than the limit already marks the end.
func listAll[T any](ctx context.Context, c *Client, path string, query url.Values, key, op string) ([]T, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = append([]string(nil), v...)
	}
	var all []T
	for offset := 0; ; offset += pageLimit {
		q.Set("limit", strconv.Itoa(pageLimit))
		q.Set("offset", strconv.Itoa(offset))
		var page map[string]json.RawMessage
		if err := c.request(ctx, http.MethodGet, path, q, nil, &page, op); err != nil {
			return nil, err
		}
		var items []T
		if raw, ok := page[key]; ok && len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("%s: decode %q of the page at offset %d: %w", op, key, offset, err)
			}
		}
		all = append(all, items...)
		if len(items) < pageLimit {
			return all, nil
		}
	}
}
