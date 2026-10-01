// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLogsClient(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/label/datum_workload_name/values":
			w.Write([]byte(`{"status":"success","data":["api"]}`))
			return
		case "/query_range":
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		switch r.URL.Query().Get("query") {
		case "denied":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"kind":"Status","message":"forbidden by policy"}`))
		case "matrix":
			w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		default:
			w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[
				{"stream":{"i":"a"},"values":[["3","a3"],["1","a1"]]},
				{"stream":{"i":"b"},"values":[["2","b2"]]}]}}`))
		}
	}))
	defer srv.Close()

	// The first token is stale, so the client must refresh it after a 401.
	tokens := []string{"stale", "fresh"}
	c := &logsClient{base: srv.URL, http: srv.Client(), token: func() (string, error) {
		tok := tokens[0]
		tokens = tokens[1:]
		return tok, nil
	}}
	ctx := context.Background()
	start := time.Unix(0, 1_000_000_123)

	entries, err := c.queryRange(ctx, queryRequest{query: "{a}", start: start, end: start, limit: 10, dir: forward})
	if err != nil {
		t.Fatalf("queryRange() = %v", err)
	}
	if got, want := texts(entries), []string{"a1", "b2", "a3"}; !slices.Equal(got, want) {
		t.Errorf("queryRange() lines = %v, want %v merged oldest first", got, want)
	}
	if got := gotQuery.Get("start"); got != "1000000123" {
		t.Errorf("start = %s, want nanoseconds", got)
	}

	values, err := c.labelValues(ctx, labelWorkload, start, start)
	if err != nil || !slices.Equal(values, []string{testWorkload}) {
		t.Errorf("labelValues() = %v, %v", values, err)
	}

	_, err = c.queryRange(ctx, queryRequest{query: "denied"})
	if err == nil || !fatal(err) || !strings.Contains(err.Error(), "forbidden by policy") || !strings.Contains(err.Error(), "logs.query") {
		t.Errorf("queryRange(denied) = %v, want a fatal error with the server's reason and a hint", err)
	}

	_, err = c.queryRange(ctx, queryRequest{query: "matrix"})
	if err == nil || !strings.Contains(err.Error(), "expected streams") {
		t.Errorf("queryRange(matrix) = %v, want a result type error", err)
	}
}
