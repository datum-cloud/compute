// SPDX-License-Identifier: AGPL-3.0-only

package iam_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/yaml"
)

const (
	group  = "compute.datumapis.com"
	crdDir = "../../config/base/crd/bases"
	iamDir = "../../config/components/iam"
)

// kubectl's get, update and patch.
var clientMethods = []string{http.MethodGet, http.MethodPut, http.MethodPatch}

type crd struct {
	Spec struct {
		Group string `json:"group"`
		Names struct {
			Plural string `json:"plural"`
		} `json:"names"`
		Scope    string `json:"scope"`
		Versions []struct {
			Name         string         `json:"name"`
			Subresources map[string]any `json:"subresources"`
		} `json:"versions"`
	} `json:"spec"`
}

type protectedResource struct {
	Spec struct {
		ServiceRef struct {
			Name string `json:"name"`
		} `json:"serviceRef"`
		Plural       string   `json:"plural"`
		Permissions  []string `json:"permissions"`
		Subresources []struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		} `json:"subresources"`
	} `json:"spec"`
}

type role struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		IncludedPermissions []string `json:"includedPermissions"`
	} `json:"spec"`
}

func readYAML(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := yaml.Unmarshal(data, into); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func glob(t *testing.T, pattern string) []string {
	t.Helper()
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no files match %s", pattern)
	}
	return files
}

func crds(t *testing.T) []crd {
	t.Helper()
	var out []crd
	for _, f := range glob(t, filepath.Join(crdDir, "*.yaml")) {
		var c crd
		readYAML(t, f, &c)
		if c.Spec.Group == group {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %s CRDs under %s", group, crdDir)
	}
	return out
}

// Returns permissions as group/resource[/subresource].verb, and declared resources.
func declaredPermissions(t *testing.T) (sets.Set[string], sets.Set[string]) {
	t.Helper()
	declared := sets.New[string]()
	resources := sets.New[string]()
	for _, f := range glob(t, filepath.Join(iamDir, "protected-resources", "*.yaml")) {
		if filepath.Base(f) == "kustomization.yaml" {
			continue
		}
		var pr protectedResource
		readYAML(t, f, &pr)
		if pr.Spec.ServiceRef.Name != group {
			continue
		}
		resources.Insert(pr.Spec.Plural)
		base := group + "/" + pr.Spec.Plural
		for _, verb := range pr.Spec.Permissions {
			declared.Insert(base + "." + verb)
		}
		for _, sub := range pr.Spec.Subresources {
			for _, verb := range sub.Permissions {
				declared.Insert(base + "/" + sub.Name + "." + verb)
			}
		}
	}
	if declared.Len() == 0 {
		t.Fatalf("no %s permissions declared under %s", group, iamDir)
	}
	return declared, resources
}

func roles(t *testing.T) []role {
	t.Helper()
	var out []role
	for _, f := range glob(t, filepath.Join(iamDir, "roles", "*.yaml")) {
		if filepath.Base(f) == "kustomization.yaml" {
			continue
		}
		var r role
		readYAML(t, f, &r)
		out = append(out, r)
	}
	return out
}

func TestCRDSubresourcesAreDeclared(t *testing.T) {
	declared, resources := declaredPermissions(t)
	resolver := &request.RequestInfoFactory{
		APIPrefixes:          sets.NewString("api", "apis"),
		GrouplessAPIPrefixes: sets.NewString("api"),
	}

	checked := 0
	for _, c := range crds(t) {
		plural := c.Spec.Names.Plural
		if !resources.Has(plural) {
			t.Errorf("no ProtectedResource covers %s/%s", group, plural)
			continue
		}
		for _, v := range c.Spec.Versions {
			path := fmt.Sprintf("/apis/%s/%s/", group, v.Name)
			if c.Spec.Scope == "Namespaced" {
				path += "namespaces/default/"
			}
			for sub := range v.Subresources {
				for _, method := range clientMethods {
					url := path + plural + "/example/" + sub
					t.Run(method+" "+url, func(t *testing.T) {
						info, err := resolver.NewRequestInfo(httptest.NewRequest(method, url, nil))
						if err != nil {
							t.Fatalf("resolve: %v", err)
						}
						if info.Resource != plural || info.Subresource != sub {
							t.Fatalf("resolved %s/%s, want %s/%s", info.Resource, info.Subresource, plural, sub)
						}
						permission := group + "/" + info.Resource + "/" + info.Subresource + "." + info.Verb
						if !declared.Has(permission) {
							t.Errorf("%s %s asks for %s, which no ProtectedResource declares", method, url, permission)
						}
					})
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no CRD serves a subresource; the CRD parse found nothing to check")
	}
}

func TestRolesGrantOnlyDeclaredPermissions(t *testing.T) {
	declared, _ := declaredPermissions(t)
	for _, r := range roles(t) {
		for _, permission := range r.Spec.IncludedPermissions {
			if strings.HasPrefix(permission, group+"/") && !declared.Has(permission) {
				t.Errorf("role %s grants %s, which no ProtectedResource declares", r.Metadata.Name, permission)
			}
		}
	}
}

func TestRolesGrantNoSubresourceWrites(t *testing.T) {
	for _, r := range roles(t) {
		for _, permission := range r.Spec.IncludedPermissions {
			resource, verb, _ := strings.Cut(strings.TrimPrefix(permission, group+"/"), ".")
			if strings.HasPrefix(permission, group+"/") && strings.Contains(resource, "/") && verb != "get" {
				t.Errorf("role %s grants %s, a write to a controller-owned subresource", r.Metadata.Name, permission)
			}
		}
	}
}
