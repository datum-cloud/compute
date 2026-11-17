// SPDX-License-Identifier: AGPL-3.0-only

package activitypolicy_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The owner the activity processor reads from a policy's labels to route the
// alerts of a failing policy. The platform's service catalog lists this
// repository under the compute service, team build.
var policyOwner = map[string]string{
	"meta.datumapis.com/service": "compute",
	"meta.datumapis.com/team":    "build",
}

type policyKustomization struct {
	Resources []string `json:"resources"`
	Labels    []struct {
		Pairs map[string]string `json:"pairs"`
	} `json:"labels"`
}

func readPolicyKustomization(t *testing.T) policyKustomization {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(policyDir, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var k policyKustomization
	if err := yaml.Unmarshal(raw, &k); err != nil {
		t.Fatal(err)
	}
	return k
}

// declaredLabels returns the labels the kustomization sets on its policies,
// by label name.
func declaredLabels(k policyKustomization) map[string]string {
	labels := map[string]string{}
	for _, l := range k.Labels {
		for name, value := range l.Pairs {
			labels[name] = value
		}
	}
	return labels
}

// policyFiles returns the YAML files in the policy directory other than its
// kustomization.
func policyFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(policyDir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") && e.Name() != "kustomization.yaml" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
}

// TestEveryPolicyDeclaresItsOwner fails when the policies' kustomization sets
// no owner on its policies, or when a policy file sits outside its resources,
// where the owner labels would not reach it.
func TestEveryPolicyDeclaresItsOwner(t *testing.T) {
	k := readPolicyKustomization(t)

	declared := declaredLabels(k)
	for name, want := range policyOwner {
		if got := declared[name]; got != want {
			t.Errorf("the policies' kustomization sets %s to %q on its policies, want %q", name, got, want)
		}
	}

	files := policyFiles(t)
	if len(files) == 0 {
		t.Fatalf("%s holds no policy files", policyDir)
	}
	listed := map[string]bool{}
	for _, r := range k.Resources {
		listed[r] = true
	}
	for _, f := range files {
		if !listed[f] {
			t.Errorf("%s: not in the policies' resources, so the owner labels never reach it", f)
		}
	}
}
