// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	agentdocs "go.datum.net/compute/docs/agent"
)

const (
	// knowledgePath serves the tier-1 knowledge document.
	knowledgePath = "/llms-full.txt"

	// runbookPrefix serves the skills. The URL says "runbooks" while the repo
	// directory says "skills": the path is the framework's, fixed by the
	// capability documents the assistant already ships, so it is not ours to
	// rename.
	runbookPrefix = "/runbooks/"

	textContentType     = "text/plain; charset=utf-8"
	markdownContentType = "text/markdown; charset=utf-8"
)

// document is one static file, read out of the embedded FS at startup.
type document struct {
	body        []byte
	contentType string
}

// knowledgeHandler serves compute's knowledge and skills over plain HTTP.
//
// Routing is an exact-match lookup in a table enumerated once from the embedded
// FS, never a path join against a directory: a request either names a document
// listed at startup or it 404s, so "..", an absolute path, or an escaped
// separator has nothing to traverse to.
//
// These documents are public and carry no auth check. The assistant fetches
// them to build a system prompt, before it holds any project context, and they
// are static text with no tenant data in them — the credential-bearing surface
// is /mcp alone.
type knowledgeHandler struct {
	docs map[string]document
}

// newKnowledgeHandler reads every embedded document into memory. Failing here
// is a build problem, not a runtime one, so the server refuses to start rather
// than serve a capability document's URLs as 404s.
func newKnowledgeHandler() (*knowledgeHandler, error) {
	docs := make(map[string]document)

	knowledge, err := agentdocs.FS.ReadFile(agentdocs.KnowledgeFile)
	if err != nil {
		return nil, fmt.Errorf("reading embedded knowledge: %w", err)
	}
	docs[knowledgePath] = document{body: knowledge, contentType: textContentType}

	entries, err := fs.ReadDir(agentdocs.FS, agentdocs.SkillsDir)
	if err != nil {
		return nil, fmt.Errorf("reading embedded skills: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		body, err := agentdocs.FS.ReadFile(path.Join(agentdocs.SkillsDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading embedded skill %s: %w", entry.Name(), err)
		}
		docs[runbookPrefix+entry.Name()] = document{body: body, contentType: markdownContentType}
	}

	if len(docs) < 2 {
		return nil, fmt.Errorf("no skills embedded from %s", agentdocs.SkillsDir)
	}
	return &knowledgeHandler{docs: docs}, nil
}

func (h *knowledgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	doc, ok := h.docs[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Every byte served is fixed at build time and already in memory, so a
	// response is one write of a known-size buffer — no per-request I/O that
	// could outlast the server's header timeout.
	w.Header().Set("Content-Type", doc.contentType)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(doc.body))
}

// paths returns the served URL paths, sorted, for the startup log.
func (h *knowledgeHandler) paths() []string {
	out := make([]string, 0, len(h.docs))
	for p := range h.docs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
