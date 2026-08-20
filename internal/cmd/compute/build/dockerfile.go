package build

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// dockerfileWorkdir is the lowercase WORKDIR instruction keyword, matched
// case-insensitively against parser.Node.Value throughout this file.
const dockerfileWorkdir = "workdir"

// dockerfileDoc is a parsed Dockerfile, used for tracing findings to source lines.
type dockerfileDoc struct {
	path   string
	lines  []string // raw file lines (0-indexed), used to reconstruct multi-line instructions
	nodes  []*parser.Node
	stages []*dockerfileStage
}

type dockerfileStage struct {
	index   int
	alias   string
	nodes   []*parser.Node
	workdir string
}

type copyInstruction struct {
	sources []string
	dest    string
	from    string
}

func parseDockerfileAST(path string) (*dockerfileDoc, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	result, err := parser.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}

	d := &dockerfileDoc{
		path:  path,
		lines: strings.Split(string(content), "\n"),
		nodes: result.AST.Children,
	}
	d.buildStages()
	return d, nil
}

func (d *dockerfileDoc) buildStages() {
	var current *dockerfileStage
	for _, n := range d.nodes {
		switch strings.ToLower(n.Value) {
		case "from":
			current = &dockerfileStage{index: len(d.stages), workdir: "/"}
			current.alias = parseStageAlias(d.instructionArgs(n))
			d.stages = append(d.stages, current)
		case dockerfileWorkdir:
			if current == nil {
				continue
			}
			wd := strings.TrimSpace(d.instructionArgs(n))
			if wd != "" {
				current.workdir = normalizeDockerPath(wd, current.workdir)
			}
		}

		if current != nil {
			current.nodes = append(current.nodes, n)
		}
	}
}

func parseStageAlias(args string) string {
	fields := splitDockerWords(args)
	for i := 0; i+1 < len(fields); i++ {
		if strings.EqualFold(fields[i], "as") {
			return strings.ToLower(fields[i+1])
		}
	}
	return ""
}

func (d *dockerfileDoc) instructionArgs(n *parser.Node) string {
	raw := d.rawInstruction(n.StartLine)
	raw = strings.ReplaceAll(raw, "\\\n", " ")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	fields := strings.Fields(raw)
	if len(fields) <= 1 {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
}

func (d *dockerfileDoc) findEntrypointProducer(entrypoint string, tc toolchain) (*parser.Node, string) {
	if len(d.stages) == 0 {
		return nil, ""
	}
	stage := d.stages[len(d.stages)-1]
	target := normalizeDockerPath(entrypoint, stage.workdir)
	sourceStage := ""

	for hops := 0; hops < len(d.stages)+1 && stage != nil; hops++ {
		node, nextStage, nextTarget := d.traceStagePath(stage, target, tc)
		if node != nil {
			return node, sourceStage
		}
		if nextStage == nil || nextStage == stage && nextTarget == target {
			return nil, sourceStage
		}
		if sourceStage == "" {
			sourceStage = nextStage.name()
		}
		stage = nextStage
		target = nextTarget
	}
	return nil, sourceStage
}

// walkStageNodesBackward walks stage's nodes backward — a later instruction
// shadows an earlier one at the same destination — tracking WORKDIR changes
// as it goes, and calls onNode with each non-WORKDIR node and the workdir in
// effect when it ran. Stops once onNode returns true.
func (d *dockerfileDoc) walkStageNodesBackward(stage *dockerfileStage, onNode func(n *parser.Node, workdir string) bool) {
	workdir := stage.workdir
	for _, n := range slices.Backward(stage.nodes) {
		if strings.EqualFold(n.Value, dockerfileWorkdir) {
			workdir = stageWorkdirBefore(stage, n)
			continue
		}
		if onNode(n, workdir) {
			return
		}
	}
}

// matchCopyTarget reports whether n is a COPY/ADD instruction with a source
// resolving to target under workdir, returning that source alongside the
// parsed instruction.
func (d *dockerfileDoc) matchCopyTarget(n *parser.Node, workdir, target string) (ci copyInstruction, src string, ok bool) {
	if !strings.EqualFold(n.Value, "copy") && !strings.EqualFold(n.Value, "add") {
		return copyInstruction{}, "", false
	}
	ci, ok = parseCopyInstruction(n.Flags, d.instructionArgs(n))
	if !ok {
		return copyInstruction{}, "", false
	}
	src, ok = copySourceForTarget(ci, target, workdir)
	return ci, src, ok
}

func (d *dockerfileDoc) findEntrypointCopy(entrypoint string) *parser.Node {
	if len(d.stages) == 0 {
		return nil
	}
	stage := d.stages[len(d.stages)-1]
	target := normalizeDockerPath(entrypoint, stage.workdir)
	var found *parser.Node
	d.walkStageNodesBackward(stage, func(n *parser.Node, workdir string) bool {
		if _, _, ok := d.matchCopyTarget(n, workdir, target); ok {
			found = n
			return true
		}
		return false
	})
	return found
}

func (d *dockerfileDoc) findEntrypointCopySourceStage(entrypoint string) string {
	if len(d.stages) == 0 {
		return ""
	}
	stage := d.stages[len(d.stages)-1]
	target := normalizeDockerPath(entrypoint, stage.workdir)
	var sourceStage string
	d.walkStageNodesBackward(stage, func(n *parser.Node, workdir string) bool {
		ci, _, ok := d.matchCopyTarget(n, workdir, target)
		if !ok {
			return false
		}
		if ci.from == "" {
			// This COPY does target the right destination, but it's from the
			// build context rather than another stage — keep looking further
			// back for one that names a source stage.
			return false
		}
		sourceStage = ci.from
		return true
	})
	return sourceStage
}

func (s *dockerfileStage) name() string {
	if s.alias != "" {
		return s.alias
	}
	return fmt.Sprint(s.index)
}

func (d *dockerfileDoc) traceStagePath(stage *dockerfileStage, target string, tc toolchain) (*parser.Node, *dockerfileStage, string) {
	var resultNode *parser.Node
	var nextStage *dockerfileStage
	var nextTarget string
	d.walkStageNodesBackward(stage, func(n *parser.Node, workdir string) bool {
		if strings.EqualFold(n.Value, "run") {
			if runProducesPath(d.instructionArgs(n), target, workdir, tc) {
				resultNode = n
				return true
			}
			return false
		}
		ci, src, ok := d.matchCopyTarget(n, workdir, target)
		if !ok {
			return false
		}
		if ci.from == "" {
			return true
		}
		fromStage := d.stageByName(ci.from)
		if fromStage == nil {
			return true
		}
		nextStage = fromStage
		nextTarget = normalizeDockerPath(src, "/")
		return true
	})
	return resultNode, nextStage, nextTarget
}

func (d *dockerfileDoc) stageByName(name string) *dockerfileStage {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, stage := range d.stages {
		if stage.alias == name || fmt.Sprint(stage.index) == name {
			return stage
		}
	}
	return nil
}

func stageWorkdirBefore(stage *dockerfileStage, before *parser.Node) string {
	workdir := "/"
	for _, n := range stage.nodes {
		if n == before {
			return workdir
		}
		if strings.EqualFold(n.Value, dockerfileWorkdir) {
			// This is intentionally best-effort; parser-normalised Value does not
			// preserve shell expansion semantics, so keep unsupported cases literal.
			parts := strings.Fields(n.Original)
			if len(parts) > 1 {
				workdir = normalizeDockerPath(strings.Join(parts[1:], " "), workdir)
			}
		}
	}
	return workdir
}

func parseCopyInstruction(flags []string, args string) (copyInstruction, bool) {
	ci := copyInstruction{}
	for _, flag := range flags {
		if from, ok := strings.CutPrefix(flag, "--from="); ok {
			ci.from = from
		}
	}

	words := splitDockerWords(args)
	var positional []string
	for _, word := range words {
		if after, ok := strings.CutPrefix(word, "--from="); ok {
			ci.from = after
			continue
		}
		if strings.HasPrefix(word, "--") {
			continue
		}
		positional = append(positional, word)
	}
	if len(positional) < 2 {
		return ci, false
	}
	ci.sources = positional[:len(positional)-1]
	ci.dest = positional[len(positional)-1]
	return ci, true
}

func copySourceForTarget(ci copyInstruction, target, workdir string) (string, bool) {
	dest := normalizeDockerPath(ci.dest, workdir)
	destIsDir := strings.HasSuffix(ci.dest, "/") || len(ci.sources) > 1
	for _, src := range ci.sources {
		candidate := dest
		if destIsDir {
			candidate = normalizeDockerPath(path.Base(src), dest)
		}
		if candidate == target {
			return src, true
		}
	}
	return "", false
}

func runProducesPath(args, target, workdir string, tc toolchain) bool {
	words := splitDockerWords(args)
	if len(words) == 0 {
		return false
	}
	if !runMatchesToolchain(words, tc) {
		return false
	}
	for i, word := range words {
		var out string
		switch {
		case word == "-o" && i+1 < len(words):
			out = words[i+1]
		case strings.HasPrefix(word, "-o="):
			out = strings.TrimPrefix(word, "-o=")
		}
		if out != "" && normalizeDockerPath(out, workdir) == target {
			return true
		}
	}
	return false
}

// runMatchesToolchain reports whether words (a RUN line) invokes tc's
// toolchain. "go"/"build" must be adjacent ("go build ...") — independently
// present anywhere lets an unrelated multi-command RUN line match by
// accident (e.g. "go generate ./... && some-tool build -o /app .").
func runMatchesToolchain(words []string, tc toolchain) bool {
	clean := make([]string, len(words))
	for i, word := range words {
		clean[i] = strings.Trim(word, " ;&|")
	}
	for i, word := range clean {
		switch word {
		case "go":
			if i+1 < len(clean) && clean[i+1] == "build" { //nolint:goconst
				return tc.Kind == toolchainGo
			}
		case "clang++", "clang", "g++", "gcc", "cc":
			return tc.Kind == toolchainNative
		}
	}
	return false
}

func normalizeDockerPath(p, workdir string) string {
	p = strings.TrimSpace(strings.Trim(p, `"'`))
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return path.Join("/", workdir, p)
}

func splitDockerWords(s string) []string {
	s = strings.ReplaceAll(s, "\\\n", " ")
	var words []string
	var b strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if b.Len() > 0 {
				words = append(words, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		words = append(words, b.String())
	}
	return words
}

// rawInstruction returns the raw Dockerfile text starting at startLine (1-based),
// following backslash line continuations. This preserves the original formatting
// that the buildkit parser normalises away in Node.Original.
func (d *dockerfileDoc) rawInstruction(startLine int) string {
	if startLine < 1 || startLine > len(d.lines) {
		return ""
	}
	var sb strings.Builder
	for i := startLine - 1; i < len(d.lines); i++ {
		line := d.lines[i]
		sb.WriteString(line)
		if !strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") {
			break
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
