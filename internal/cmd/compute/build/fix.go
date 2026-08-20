package build

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/fatih/color"
)

// printFixAnalysisSummary reports what the analysis found before --fix acts
// on it. When issues remain, it deliberately doesn't announce a count here —
// the "fixed [...]" line printed per applied edit (or the final failure
// report, if a finding can't be auto-fixed) already conveys what was found,
// and a separate count line here would just repeat that across every
// rebuild-and-recheck with no new information.
func printFixAnalysisSummary(result *analysisResult) {
	printAnalysisNotes(result)
	if result.OK() {
		fmt.Fprintln(os.Stderr, "No compatibility issues found")
	}
}

// applyExactLineFixes applies every exact-line edit, per file, from the
// highest line number down. A multi-line edit.New shifts every line below it
// in the file, so applying top-down would invalidate the line number of any
// pending edit further down; applying bottom-up guarantees each edit still
// sees its original line number when its turn comes. If two edits target the
// same original line, only the first (in finding order) matches —
// applyExactLineEdit re-reads the file and checks edit.Old against current
// content, so the second harmlessly no-ops and becomes applicable again after
// the next rebuild.
func applyExactLineFixes(result *analysisResult) (int, error) {
	if result == nil {
		return 0, nil
	}
	type editRef struct {
		finding finding
		edit    sourceEdit
	}
	var refs []editRef
	for _, finding := range result.Findings {
		for _, edit := range finding.Edits {
			if isExactLineEdit(edit) {
				refs = append(refs, editRef{finding, edit})
			}
		}
	}
	slices.SortStableFunc(refs, func(a, b editRef) int {
		if c := cmp.Compare(a.edit.File, b.edit.File); c != 0 {
			return c
		}
		return cmp.Compare(b.edit.Line, a.edit.Line)
	})

	count := 0
	for _, ref := range refs {
		applied, err := applyExactLineEdit(ref.edit)
		if err != nil {
			return count, err
		}
		if applied {
			count++
			printAppliedEdit(ref.finding, ref.edit)
		}
	}
	return count, nil
}

func printAppliedEdit(finding finding, edit sourceEdit) {
	checkTag := ""
	if finding.check != "" {
		checkTag = color.New(color.Bold).Sprintf("[%s]", finding.check)
	}
	if finding.Message != "" {
		fmt.Fprintf(os.Stderr, "%s%s: %s\n", cSuccess.Sprint("fixed"), checkTag, finding.Message)
	} else {
		fmt.Fprintf(os.Stderr, "%s%s:\n", cSuccess.Sprint("fixed"), checkTag)
	}
	printEdit(edit)
}

func isExactLineEdit(edit sourceEdit) bool {
	return edit.File != "" && edit.Line > 0 && edit.Old != "" && edit.New != "" && !strings.ContainsAny(edit.Old, "\r\n")
}

func applyExactLineEdit(edit sourceEdit) (bool, error) {
	data, err := os.ReadFile(edit.File)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", edit.File, err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	if edit.Line > len(lines) {
		return false, nil
	}
	idx := edit.Line - 1
	line := strings.TrimSuffix(strings.TrimSuffix(lines[idx], "\n"), "\r")
	if line != edit.Old {
		return false, nil
	}
	ending := ""
	if strings.HasSuffix(lines[idx], "\r\n") {
		ending = "\r\n"
	} else if strings.HasSuffix(lines[idx], "\n") {
		ending = "\n"
	}
	lines[idx] = edit.New + ending
	if err := os.WriteFile(edit.File, []byte(strings.Join(lines, "")), 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", edit.File, err)
	}
	return true, nil
}
