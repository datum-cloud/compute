package agent

import (
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	agentdocs "go.datum.net/compute/docs/agent"
)

// The Claude Code plugin under claude-plugin/ offers each published skill as a
// thin SKILL.md that loads the procedure through datum-mcp's compute_guide, so
// the procedure text lives here once. These tests keep the two lists in step: a
// skill published without a plugin entry, or a plugin entry naming a skill that
// is not published, fails.

const claudePluginDir = "../../claude-plugin"

// notPackaged are published skills the plugin deliberately leaves out, each
// with the reason.
var notPackaged = map[string]string{
	"instance-command-check-v3": "the opt-in assistant exec flow; its plan and run tools are not offered outside the in-product assistant",
}

var guideSkillRef = regexp.MustCompile("`compute_guide` with `skill: \"([a-z0-9-]+)\"`")

func pluginSkill(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(path.Join(claudePluginDir, "skills", name, "SKILL.md"))
	if err != nil {
		t.Fatalf("reading the Claude Code plugin's %q skill: %v", name, err)
	}
	return b
}

func TestClaudePluginOffersEveryPublishedSkill(t *testing.T) {
	entries, err := fs.ReadDir(agentdocs.FS, agentdocs.SkillsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", agentdocs.SkillsDir, err)
	}
	published := map[string]bool{}
	for _, e := range entries {
		published[strings.TrimSuffix(e.Name(), ".md")] = true
	}

	dirs, err := os.ReadDir(path.Join(claudePluginDir, "skills"))
	if err != nil {
		t.Fatalf("reading plugin skills: %v", err)
	}
	offered := map[string]bool{}
	for _, d := range dirs {
		for _, m := range guideSkillRef.FindAllStringSubmatch(string(pluginSkill(t, d.Name())), -1) {
			ref := m[1]
			if ref != d.Name() {
				t.Errorf("claude-plugin/skills/%s loads %q; a plugin skill that loads a published one carries its name", d.Name(), ref)
			}
			if !published[ref] {
				t.Errorf("claude-plugin/skills/%s loads %q, which docs/agent/skills does not publish", d.Name(), ref)
			}
			offered[ref] = true
		}
	}
	if len(offered) == 0 {
		t.Fatal("no plugin skill loads a published skill; the plugin or its compute_guide call moved")
	}

	for name := range published {
		if _, skip := notPackaged[name]; skip || offered[name] {
			continue
		}
		t.Errorf("published skill %q is not in the Claude Code plugin\n"+
			"  fix: add claude-plugin/skills/%s/SKILL.md with name and description frontmatter "+
			"that calls `compute_guide` with `skill: %q`, or list it in notPackaged with the reason",
			name, name, name)
	}
	for name := range notPackaged {
		if !published[name] {
			t.Errorf("notPackaged lists %q, which is no longer published; remove it", name)
		}
	}
}

func TestClaudePluginSkillsHaveFrontmatter(t *testing.T) {
	dirs, err := os.ReadDir(path.Join(claudePluginDir, "skills"))
	if err != nil {
		t.Fatalf("reading plugin skills: %v", err)
	}
	if len(dirs) == 0 {
		t.Fatal("the plugin has no skills")
	}
	for _, d := range dirs {
		name := d.Name()
		b := pluginSkill(t, name)
		parts := strings.SplitN(string(b), "---\n", 3)
		if len(parts) != 3 || parts[0] != "" {
			t.Errorf("%s/SKILL.md does not open with --- frontmatter", name)
			continue
		}
		var fm struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
			t.Errorf("%s/SKILL.md frontmatter is not valid YAML: %v", name, err)
			continue
		}
		if fm.Name != name {
			t.Errorf("%s/SKILL.md names itself %q; Claude Code expects the directory name", name, fm.Name)
		}
		if strings.TrimSpace(fm.Description) == "" {
			t.Errorf("%s/SKILL.md has no description, so Claude has nothing to match a request against", name)
		}
	}
}

// TestClaudePluginUsesNoInternalVocabulary holds the plugin's own text to the
// same vocabulary as the published skills.
func TestClaudePluginUsesNoInternalVocabulary(t *testing.T) {
	terms := internalVocabulary()
	dirs, err := os.ReadDir(path.Join(claudePluginDir, "skills"))
	if err != nil {
		t.Fatalf("reading plugin skills: %v", err)
	}
	files := make([]string, 0, 1+len(dirs))
	files = append(files, "README.md")
	for _, d := range dirs {
		files = append(files, path.Join("skills", d.Name(), "SKILL.md"))
	}
	for _, f := range files {
		b, err := os.ReadFile(path.Join(claudePluginDir, f))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			checkCopy(t, "claude-plugin/"+f+":"+itoa(i+1), line, terms)
		}
	}
}
