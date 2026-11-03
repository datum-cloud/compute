// SPDX-License-Identifier: AGPL-3.0-only

// Package agentdocs embeds the knowledge and skills compute publishes to an AI
// assistant.
//
// Embedding rather than reading from disk keeps the server a single
// self-contained binary: a stripped image cannot silently start answering 404
// for knowledge compute's capability document promises.
package agentdocs

import "embed"

// FS holds the knowledge document and one file per skill. Paths within it are
// "llms-full.txt" and "skills/<name>.md".
//
//go:embed llms-full.txt skills/*.md
var FS embed.FS

const (
	// KnowledgeFile is the tier-1 knowledge document: the compute resource
	// model and how to read its conditions.
	KnowledgeFile = "llms-full.txt"

	// SkillsDir holds the triage procedures, one Markdown file per skill.
	SkillsDir = "skills"
)
