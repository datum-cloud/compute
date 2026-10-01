// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// fakeQuerier serves entries the way the query API does: over [start, end)
// with both bounds truncated to the second, ordered by direction, cut at the
// limit. Queries are recorded but not evaluated. If pages is set, its pages
// are returned in order instead.
type fakeQuerier struct {
	entries []entry
	pages   [][]entry
	errs    []error // returned first, in order; nil entries fall through
	values  map[string][]string

	queries []queryRequest
}

func (f *fakeQuerier) queryRange(_ context.Context, req queryRequest) ([]entry, error) {
	f.queries = append(f.queries, req)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	if f.pages != nil {
		if len(f.pages) == 0 {
			return nil, nil
		}
		p := f.pages[0]
		f.pages = f.pages[1:]
		return p, nil
	}
	start := req.start.Truncate(time.Second).UnixNano()
	end := req.end.Truncate(time.Second).UnixNano()
	var out []entry
	for _, e := range f.entries {
		if e.ts >= start && e.ts < end {
			out = append(out, e)
		}
	}
	sortEntries(out, req.dir)
	return out[:min(req.limit, len(out))], nil
}

func (f *fakeQuerier) labelValues(_ context.Context, label string, _, _ time.Time) ([]string, error) {
	return f.values[label], nil
}

func line(ts int64, text string) entry {
	return entry{ts: ts, stream: "s", line: text, labels: map[string]string{}}
}

// lines builds n seconds of entries, perSecond distinct lines in each.
func lines(n, perSecond int) []entry {
	var out []entry
	for i := 1; i <= n; i++ {
		for j := range perSecond {
			ts := int64(i)*time.Second.Nanoseconds() + int64(j)*1000
			out = append(out, line(ts, fmt.Sprintf("%d.%d", i, j)))
		}
	}
	return out
}

func texts(entries []entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.line
	}
	return out
}

var (
	epoch = time.Unix(0, 0)
	far   = time.Unix(1<<20, 0)
)

func TestPages(t *testing.T) {
	sec := time.Second.Nanoseconds()
	both := []direction{forward, backward}
	tests := []struct {
		name     string
		entries  []entry
		pageSize int
		dirs     []direction
		want     []string // forward order; nil means every entry
	}{
		{name: "exact multiple of the page", entries: lines(9, 1), pageSize: 3, dirs: both},
		{name: "a second straddles each page boundary", entries: lines(7, 2), pageSize: 3, dirs: both},
		{name: "several seconds per page", entries: lines(20, 3), pageSize: 7, dirs: both},
		{
			// Second-truncated bounds can't reach past a page's worth of one
			// second, so the rest of it is skipped.
			name:     "a second wider than a page",
			entries:  append(lines(1, 4), line(2*sec, "after")),
			pageSize: 2,
			dirs:     []direction{forward},
			want:     []string{"1.0", "1.1", "after"},
		},
	}
	for _, tt := range tests {
		for _, dir := range tt.dirs {
			t.Run(tt.name+"/"+string(dir), func(t *testing.T) {
				var got []entry
				err := pages(context.Background(), &fakeQuerier{entries: tt.entries}, "{}", epoch, far, dir, tt.pageSize, func(p []entry) bool {
					got = append(got, p...)
					return true
				})
				if err != nil {
					t.Fatal(err)
				}
				want := tt.want
				if want == nil {
					want = texts(tt.entries)
				}
				if dir == backward {
					want = slices.Clone(want)
					slices.Reverse(want)
				}
				if !slices.Equal(texts(got), want) {
					t.Errorf("pages() = %v, want %v", texts(got), want)
				}
			})
		}
	}
}

func TestTail(t *testing.T) {
	q := &fakeQuerier{entries: lines(10, 1)}
	got, err := tail(context.Background(), q, "{}", epoch, far, 4)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"7.0", "8.0", "9.0", "10.0"}; !slices.Equal(texts(got), want) {
		t.Errorf("tail() = %v, want %v", texts(got), want)
	}
	if q.queries[0].dir != backward || q.queries[0].limit != 4 {
		t.Errorf("tail() asked %+v, want backward for 4 lines", q.queries[0])
	}
}
