// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"slices"
	"time"
)

// pages walks [start, end) in dir, passing fn each page's unseen entries until
// fn returns false or the window is exhausted.
//
// The API truncates window bounds to whole seconds, so each page resumes at
// the second of the previous page's last line and drops the lines from that
// second it already returned. A second holding more than a page of lines
// can't be paged into; the rest of it is skipped.
func pages(ctx context.Context, q querier, query string, start, end time.Time, dir direction, pageSize int, fn func([]entry) bool) error {
	edge := int64(-1)
	seen := map[string]bool{}

	for {
		page, err := q.queryRange(ctx, queryRequest{query: query, start: start, end: end, limit: pageSize, dir: dir})
		if err != nil {
			return err
		}

		fresh := make([]entry, 0, len(page))
		for _, e := range page {
			if second(e.ts) != edge || !seen[e.key()] {
				fresh = append(fresh, e)
			}
		}
		if len(fresh) > 0 && !fn(fresh) {
			return nil
		}
		if len(page) < pageSize {
			return nil
		}

		last := second(page[len(page)-1].ts)
		if len(fresh) == 0 {
			if dir == backward {
				end = time.Unix(last, 0)
			} else {
				start = time.Unix(last+1, 0)
			}
			edge, seen = -1, map[string]bool{}
			continue
		}

		if last != edge {
			edge, seen = last, map[string]bool{}
		}
		for _, e := range page {
			if second(e.ts) == edge {
				seen[e.key()] = true
			}
		}
		if dir == backward {
			end = time.Unix(last+1, 0)
		} else {
			start = time.Unix(last, 0)
		}
	}
}

func second(ts int64) int64 {
	return time.Unix(0, ts).Unix()
}

// tail returns the newest n lines in [start, end), oldest first.
func tail(ctx context.Context, q querier, query string, start, end time.Time, n int) ([]entry, error) {
	if n == 0 {
		return nil, nil
	}
	var out []entry
	err := pages(ctx, q, query, start, end, backward, min(n, maxLimit), func(page []entry) bool {
		out = append(out, page...)
		return len(out) < n
	})
	if err != nil {
		return nil, err
	}
	out = out[:min(n, len(out))]
	slices.Reverse(out)
	return out, nil
}

// all passes every line in [start, end) to emit, oldest first.
func all(ctx context.Context, q querier, query string, start, end time.Time, emit func(entry)) error {
	return pages(ctx, q, query, start, end, forward, maxLimit, func(page []entry) bool {
		for _, e := range page {
			emit(e)
		}
		return true
	})
}
