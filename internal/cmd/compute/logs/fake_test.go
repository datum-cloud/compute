// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"time"

	logsapi "go.miloapis.com/telemetry/cli/logs"
)

// fakeQuerier serves entries the way the query API does: over [start, end)
// with both bounds truncated to the second, ordered by direction, cut at the
// limit. Queries are recorded but not evaluated. If pages is set, its pages
// are returned in order instead.
type fakeQuerier struct {
	entries []logsapi.Entry
	pages   [][]logsapi.Entry
	values  map[string][]string

	queries []logsapi.Query
}

func (f *fakeQuerier) QueryRange(_ context.Context, q logsapi.Query) ([]logsapi.Entry, error) {
	f.queries = append(f.queries, q)
	if f.pages != nil {
		if len(f.pages) == 0 {
			return nil, nil
		}
		p := f.pages[0]
		f.pages = f.pages[1:]
		return p, nil
	}
	start := q.Start.Truncate(time.Second)
	end := q.End.Truncate(time.Second)
	var out []logsapi.Entry
	for _, e := range f.entries {
		if !e.Time.Before(start) && e.Time.Before(end) {
			out = append(out, e)
		}
	}
	logsapi.Sort(out, q.Direction)
	return out[:min(q.Limit, len(out))], nil
}

func (f *fakeQuerier) LabelValues(_ context.Context, label string, _, _ time.Time) ([]string, error) {
	return f.values[label], nil
}

func line(ts int64, text string) logsapi.Entry {
	return logsapi.Entry{Time: time.Unix(0, ts), Line: text, Labels: map[string]string{}}
}

var (
	epoch = time.Unix(0, 0)
	far   = time.Unix(1<<20, 0)
)
