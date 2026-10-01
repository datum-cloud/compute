// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/datumctl/plugin"
)

const (
	logsAPIPath = "/apis/o11y.miloapis.com/v1alpha1/logs/loki/api/v1"

	// maxLimit is the query API's server-side cap on returned lines.
	maxLimit = 5000

	requestTimeout = 60 * time.Second

	// tokenTTL bounds token reuse: --follow polls several times a second and
	// plugin.Token execs datumctl on every call.
	tokenTTL = time.Minute
)

type direction string

const (
	backward direction = "backward"
	forward  direction = "forward"
)

// entry is one log line, lifted out of its stream.
type entry struct {
	ts     int64
	labels map[string]string
	stream string // canonical label set
	line   string
}

func (e entry) key() string {
	return strconv.FormatInt(e.ts, 10) + "\x00" + e.stream + "\x00" + e.line
}

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

// fatal reports whether retrying err cannot help.
func fatal(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// queryRequest is one query_range call over [start, end).
type queryRequest struct {
	query string
	start time.Time
	end   time.Time
	limit int
	dir   direction
}

type querier interface {
	queryRange(ctx context.Context, req queryRequest) ([]entry, error)
	labelValues(ctx context.Context, label string, start, end time.Time) ([]string, error)
}

// logsClient talks to the project's Loki-compatible query API.
type logsClient struct {
	base  string
	http  *http.Client
	token func() (string, error)

	mu      sync.Mutex
	cached  string
	fetched time.Time
}

func newLogsClient(project string) (*logsClient, error) {
	pctx := plugin.Context()
	if pctx.APIHost == "" {
		return nil, errors.New("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}
	return &logsClient{
		base:  util.ProjectControlPlaneURL(pctx.APIHost, project) + logsAPIPath,
		http:  &http.Client{Timeout: requestTimeout},
		token: plugin.Token,
	}, nil
}

func (c *logsClient) bearer(refresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if refresh || c.cached == "" || time.Since(c.fetched) > tokenTTL {
		t, err := c.token()
		if err != nil {
			return "", fmt.Errorf("getting credentials: %w", err)
		}
		c.cached, c.fetched = t, time.Now()
	}
	return c.cached, nil
}

func (c *logsClient) queryRange(ctx context.Context, req queryRequest) ([]entry, error) {
	q := between(req.start, req.end)
	q.Set("query", req.query)
	q.Set("limit", strconv.Itoa(req.limit))
	q.Set("direction", string(req.dir))
	var data struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/query_range", q, &data); err != nil {
		return nil, err
	}
	if data.ResultType != "streams" {
		return nil, fmt.Errorf("logs query returned %q results, expected streams", data.ResultType)
	}

	var entries []entry
	for _, s := range data.Result {
		key := canonical(s.Stream)
		for _, v := range s.Values {
			ts, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			entries = append(entries, entry{ts: ts, labels: s.Stream, stream: key, line: v[1]})
		}
	}
	sortEntries(entries, req.dir)
	return entries, nil
}

func (c *logsClient) labelValues(ctx context.Context, label string, start, end time.Time) ([]string, error) {
	q := between(start, end)
	var values []string
	if err := c.get(ctx, "/label/"+url.PathEscape(label)+"/values", q, &values); err != nil {
		return nil, err
	}
	return values, nil
}

// get fetches path into data, retrying once with a fresh token on 401.
func (c *logsClient) get(ctx context.Context, path string, q url.Values, data any) error {
	u := c.base + path + "?" + q.Encode()
	for attempt := 0; ; attempt++ {
		token, err := c.bearer(attempt > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("querying logs: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			continue
		}
		return decode(resp, data)
	}
}

func decode(resp *http.Response, data any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	var envelope struct {
		Status string          `json:"status"`
		Error  string          `json:"error"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decoding logs response: %w", err)
	}
	if envelope.Status != "success" {
		return fmt.Errorf("logs query failed: %s", envelope.Error)
	}
	if err := json.Unmarshal(envelope.Data, data); err != nil {
		return fmt.Errorf("decoding logs response: %w", err)
	}
	return nil
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	detail := strings.TrimSpace(string(body))

	// Loki errors carry "error"; Kubernetes Status objects carry "message".
	var envelope struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		detail = cmp.Or(envelope.Error, envelope.Message, detail)
	}

	msg := "logs query returned " + resp.Status
	if detail != "" {
		msg += ": " + detail
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		msg += " — run 'datumctl login' to refresh your credentials"
	case http.StatusForbidden:
		msg += " — reading logs requires the o11y.miloapis.com logs.query permission on this project"
	case http.StatusNotFound:
		msg += " — this project may not have the telemetry API enabled"
	}
	return &apiError{status: resp.StatusCode, msg: msg}
}

// between is the window parameters, in nanoseconds.
func between(start, end time.Time) url.Values {
	return url.Values{
		"start": {strconv.FormatInt(start.UnixNano(), 10)},
		"end":   {strconv.FormatInt(end.UnixNano(), 10)},
	}
}

func canonical(labels map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		b.WriteString(k + "=" + labels[k] + "\x01")
	}
	return b.String()
}

// sortEntries orders entries across streams; the API groups them by stream.
func sortEntries(entries []entry, dir direction) {
	sort.SliceStable(entries, func(i, j int) bool {
		if dir == forward {
			return entries[i].ts < entries[j].ts
		}
		return entries[i].ts > entries[j].ts
	})
}
