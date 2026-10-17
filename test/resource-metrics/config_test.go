package resourcemetrics

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const nodeQueryKind = "node"

// TestShippedQueries exercises the query templates from the installed ConfigMap.
func TestShippedQueries(t *testing.T) {
	promtool, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool is required to run resource metric query regressions")
	}
	contents, err := os.ReadFile("../../config/components/prometheus-adapter/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type queryPair struct {
		ContainerQuery string `json:"containerQuery"`
		NodeQuery      string `json:"nodeQuery"`
	}
	var config struct {
		ResourceRules struct {
			CPU    queryPair `json:"cpu"`
			Memory queryPair `json:"memory"`
		} `json:"resourceRules"`
	}
	if err := yaml.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	var rules []map[string]string
	for name, queries := range map[string]queryPair{
		"cpu":    config.ResourceRules.CPU,
		"memory": config.ResourceRules.Memory,
	} {
		for kind, query := range map[string]string{"container": queries.ContainerQuery, nodeQueryKind: queries.NodeQuery} {
			group := "namespace,pod,container"
			if kind == nodeQueryKind {
				group = nodeQueryKind
			}
			query = strings.NewReplacer("<<.LabelMatchers>>", `node="worker"`, "<<.GroupBy>>", group).Replace(query)
			rules = append(rules, map[string]string{"record": "test_" + name + "_" + kind, "expr": query})
		}
	}
	generated, err := yaml.Marshal(map[string]any{
		"groups": []any{map[string]any{"name": "adapter", "interval": "30s", "rules": rules}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rules.yaml"), generated, 0600); err != nil {
		t.Fatal(err)
	}
	tests, err := os.ReadFile("queries.test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests.yaml"), tests, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(promtool, "test", "rules", "tests.yaml")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("query regressions: %v\n%s", err, output)
	}
}
