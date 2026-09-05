// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	agentdocs "go.datum.net/compute/docs/agent"
)

// newTestMux mirrors the routes run() registers, so these tests exercise the
// same path handling a real request meets.
func newTestMux(t *testing.T) (*http.ServeMux, *knowledgeHandler) {
	t.Helper()
	docs, err := newKnowledgeHandler()
	if err != nil {
		t.Fatalf("newKnowledgeHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(knowledgePath, docs)
	mux.Handle(runbookPrefix, docs)
	return mux, docs
}

func get(t *testing.T, h http.Handler, target string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Result()
}

func TestServesKnowledge(t *testing.T) {
	mux, _ := newTestMux(t)

	resp := get(t, mux, knowledgePath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", knowledgePath, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != textContentType {
		t.Errorf("Content-Type = %q, want %q", got, textContentType)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	want, err := agentdocs.FS.ReadFile(agentdocs.KnowledgeFile)
	if err != nil {
		t.Fatalf("reading embedded knowledge: %v", err)
	}
	if string(body) != string(want) {
		t.Errorf("body does not match the embedded document (%d vs %d bytes)", len(body), len(want))
	}
}

// TestServesEverySkill pins the URL shape: the repo directory is skills/, but
// the capability documents the assistant ships point at /runbooks/.
func TestServesEverySkill(t *testing.T) {
	mux, _ := newTestMux(t)

	entries, err := fs.ReadDir(agentdocs.FS, agentdocs.SkillsDir)
	if err != nil {
		t.Fatalf("reading skills: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no skills embedded")
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		target := runbookPrefix + entry.Name()
		resp := get(t, mux, target)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("Content-Type"); got != markdownContentType {
			t.Errorf("GET %s Content-Type = %q, want %q", target, got, markdownContentType)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		want, err := agentdocs.FS.ReadFile(path.Join(agentdocs.SkillsDir, entry.Name()))
		if err != nil {
			t.Fatalf("reading embedded skill: %v", err)
		}
		if string(body) != string(want) {
			t.Errorf("GET %s body does not match the embedded document", target)
		}
	}
}

// TestSkillsMatchDocumentedSet fails when a skill is added or removed without
// the capability document being updated, since each name is a published URL.
func TestSkillsMatchDocumentedSet(t *testing.T) {
	_, docs := newTestMux(t)

	want := map[string]bool{
		"/llms-full.txt":                      true,
		"/runbooks/workload-not-available.md": true,
		"/runbooks/quota-triage.md":           true,
		"/runbooks/instance-not-ready.md":     true,
		"/runbooks/referenced-data-triage.md": true,
		"/runbooks/placement-triage.md":       true,
		"/runbooks/stalled-transient.md":      true,
	}
	got := docs.paths()
	if len(got) != len(want) {
		t.Fatalf("served paths = %v, want %d entries", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected served path %q; update the capability document too", p)
		}
	}
}

func TestMissingRunbookIs404(t *testing.T) {
	mux, _ := newTestMux(t)

	for _, target := range []string{
		"/runbooks/does-not-exist.md",
		"/runbooks/",
		"/runbooks/workload-not-available", // no extension
		"/runbooks/nested/dir.md",
	} {
		if resp := get(t, mux, target); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, resp.StatusCode)
		}
	}
}

// TestTraversalIsRefused covers escapes out of the runbook prefix. The handler
// matches paths exactly against a table enumerated at startup, so nothing here
// can resolve to a file the table does not already publish. The mux may clean
// and redirect a path first, so the assertion is the end-to-end one: whatever
// finally answers 200 must be one of the enumerated public documents.
func TestTraversalIsRefused(t *testing.T) {
	mux, docs := newTestMux(t)

	published := make(map[string]bool, len(docs.paths()))
	for _, p := range docs.paths() {
		published[p] = true
	}

	targets := []string{
		"/runbooks/../llms-full.txt",
		"/runbooks/../../etc/passwd",
		"/runbooks/%2e%2e/%2e%2e/etc/passwd",
		"/runbooks/..%2fllms-full.txt",
		"/runbooks/./../../go.mod",
		"/runbooks//etc/passwd",
		"/runbooks/skills/../../embed.go",
		"/runbooks/../../../../../../etc/hosts",
	}

	for _, target := range targets {
		// Straight at the handler: only an exact table hit may return 200.
		if resp := get(t, docs, target); resp.StatusCode == http.StatusOK {
			t.Errorf("handler GET %s = 200, want a refusal", target)
		}

		// Through the mux, following its path-cleaning redirects.
		at := target
		for hop := 0; hop < 5; hop++ {
			resp := get(t, mux, at)
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				loc := resp.Header.Get("Location")
				if loc == "" {
					t.Fatalf("GET %s = %d with no Location", at, resp.StatusCode)
				}
				at = loc
				continue
			}
			if resp.StatusCode == http.StatusOK && !published[at] {
				t.Errorf("GET %s served %s, which is not a published document", target, at)
			}
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 200 on a published path or 404", target, resp.StatusCode)
			}
			break
		}
	}
}

// TestDocsNeedNoAuth: the capability document points the assistant at these
// URLs before it holds any project context, and they carry no tenant data.
func TestDocsNeedNoAuth(t *testing.T) {
	mux, _ := newTestMux(t)

	req := httptest.NewRequest(http.MethodGet, knowledgePath, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("unauthenticated GET %s = %d, want 200", knowledgePath, rec.Code)
	}
}

func TestMutatingMethodsRefused(t *testing.T) {
	mux, _ := newTestMux(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, knowledgePath, strings.NewReader("x")))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", method, knowledgePath, rec.Code)
		}
	}
}

func TestHeadServesNoBody(t *testing.T) {
	mux, _ := newTestMux(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, knowledgePath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD %s = %d, want 200", knowledgePath, rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned %d bytes of body", rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") == "" {
		t.Error("HEAD returned no Content-Length")
	}
}
