// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"fmt"
	"io"
	"time"
)

const (
	// followOverlap is how far before the newest printed line each poll
	// starts, to catch lines still being ingested.
	followOverlap = 10 * time.Second

	followMinInterval = 500 * time.Millisecond
	followMaxInterval = 2 * time.Second
	followBackoffStep = 250 * time.Millisecond

	// followEndSlack keeps a local clock running behind the collector's from
	// hiding fresh lines.
	followEndSlack = time.Minute
)

// followErrorInterval is a variable so tests can shorten it.
var followErrorInterval = 10 * time.Second

// follower remembers what was printed so overlapping polls don't repeat it.
type follower struct {
	cursor int64 // newest timestamp printed
	seen   map[string]int64

	// floor drops lines older than the backlog, which --tail left out; the
	// first poll's overlap would otherwise print them after it.
	floor int64
}

// admit returns the entries not printed before and records them.
func (f *follower) admit(entries []entry) []entry {
	if f.seen == nil {
		f.seen = map[string]int64{}
	}
	var fresh []entry
	for _, e := range entries {
		if e.ts < f.floor {
			continue
		}
		if _, ok := f.seen[e.key()]; ok {
			continue
		}
		f.seen[e.key()] = e.ts
		fresh = append(fresh, e)
		f.cursor = max(f.cursor, e.ts)
	}
	// A poll starts at cursor-overlap truncated to the second; nothing older
	// comes back.
	cutoff := f.cursor - (followOverlap + time.Second).Nanoseconds()
	for k, ts := range f.seen {
		if ts < cutoff {
			delete(f.seen, k)
		}
	}
	return fresh
}

// follow polls for new lines until ctx is done. Errors retrying can't fix end
// it; others are reported once and retried.
func follow(ctx context.Context, q querier, query string, f *follower, since time.Time, emit func(entry), status io.Writer) error {
	var interval time.Duration
	failing, skipOverlap := false, false

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}

		start := since
		if f.cursor > 0 {
			start = time.Unix(0, f.cursor)
			if !skipOverlap {
				start = start.Add(-followOverlap)
			}
		}
		entries, err := q.queryRange(ctx, queryRequest{
			query: query,
			start: start,
			end:   time.Now().Add(followEndSlack),
			limit: maxLimit,
			dir:   forward,
		})
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil && fatal(err):
			return err
		case err != nil:
			if !failing {
				fmt.Fprintf(status, "Lost contact with the logs API, retrying: %v\n", err)
				failing = true
			}
			interval = followErrorInterval
			continue
		}
		if failing {
			fmt.Fprintln(status, "Reconnected.")
			failing = false
		}

		fresh := f.admit(entries)
		for _, e := range fresh {
			emit(e)
		}

		full := len(entries) >= maxLimit
		// A full page of already-printed lines means the overlap alone exceeds
		// a page; step past it instead of refetching it forever.
		skipOverlap = full && len(fresh) == 0
		switch {
		case full:
			interval = 0
		case len(fresh) > 0:
			interval = followMinInterval
		default:
			interval = min(max(interval, followMinInterval)+followBackoffStep, followMaxInterval)
		}
	}
}
