// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFollowerAdmit(t *testing.T) {
	sec := time.Second.Nanoseconds()
	f := follower{floor: 2 * sec}

	got := f.admit([]entry{line(1*sec, "left out by --tail"), line(2*sec, "b"), line(3*sec, "c")})
	if want := []string{"b", "c"}; !slices.Equal(texts(got), want) {
		t.Errorf("admit() = %v, want %v", texts(got), want)
	}
	// An overlapping poll re-returns c.
	got = f.admit([]entry{line(3*sec, "c"), line(4*sec, "d")})
	if want := []string{"d"}; !slices.Equal(texts(got), want) {
		t.Errorf("admit() = %v, want %v", texts(got), want)
	}

	// Keys live for the overlap plus the second a poll's start is truncated by.
	f.admit([]entry{line(4*sec+followOverlap.Nanoseconds()+sec, "e")})
	if _, ok := f.seen[line(4*sec, "d").key()]; !ok {
		t.Error("forgot d while a truncated poll could still return it")
	}
	if _, ok := f.seen[line(3*sec, "c").key()]; ok {
		t.Error("kept c past the overlap")
	}
}

func TestFollow(t *testing.T) {
	defer func(d time.Duration) { followErrorInterval = d }(followErrorInterval)
	followErrorInterval = time.Millisecond

	now := time.Now().UnixNano()
	q := &fakeQuerier{
		errs:  []error{errors.New("connection reset"), nil, &apiError{status: http.StatusForbidden, msg: "forbidden"}},
		pages: [][]entry{{line(now, "back")}},
	}
	var status strings.Builder
	var printed []string
	err := follow(context.Background(), q, "{}", &follower{}, time.Now(), func(e entry) {
		printed = append(printed, e.line)
	}, &status)

	if err == nil || err.Error() != "forbidden" {
		t.Errorf("follow() = %v, want the 403 to end it", err)
	}
	if want := []string{"back"}; !slices.Equal(printed, want) {
		t.Errorf("printed %v, want %v", printed, want)
	}
	if s := status.String(); !strings.Contains(s, "retrying") || !strings.Contains(s, "Reconnected") {
		t.Errorf("status = %q, want the outage and recovery reported", s)
	}
	if got, want := q.queries[2].start, time.Unix(0, now).Add(-followOverlap); !got.Equal(want) {
		t.Errorf("poll start = %v, want %v", got, want)
	}
}
