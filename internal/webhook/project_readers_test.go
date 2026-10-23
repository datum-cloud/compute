// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// TestProjectReadersReadTheNamedProject verifies that a reader lists from the
// named project's control plane, and that kind discovery goes to the mapper
// project once, not to each project read.
func TestProjectReadersReadTheNamedProject(t *testing.T) {
	const controlPlane = "/apis/resourcemanager.miloapis.com/v1alpha1/projects/%s/control-plane"
	providerPrefix := strings.Replace(controlPlane, "%s", "provider", 1)

	var mu sync.Mutex
	var listPaths []string
	discoveryCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == providerPrefix+"/api":
			discoveryCalls++
			writeJSON(t, w, &metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions"}, Versions: []string{"v1"}})
		case r.URL.Path == providerPrefix+"/apis":
			discoveryCalls++
			writeJSON(t, w, &metav1.APIGroupList{
				TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"},
				Groups: []metav1.APIGroup{{
					Name:             computev1alpha.GroupVersion.Group,
					Versions:         []metav1.GroupVersionForDiscovery{{GroupVersion: computev1alpha.GroupVersion.String(), Version: computev1alpha.GroupVersion.Version}},
					PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: computev1alpha.GroupVersion.String(), Version: computev1alpha.GroupVersion.Version},
				}},
			})
		case r.URL.Path == providerPrefix+"/apis/"+computev1alpha.GroupVersion.String():
			discoveryCalls++
			writeJSON(t, w, &metav1.APIResourceList{
				TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
				GroupVersion: computev1alpha.GroupVersion.String(),
				APIResources: []metav1.APIResource{{Name: "instancetypes", Kind: "InstanceType", Verbs: []string{"get", "list"}}},
			})
		case strings.HasSuffix(r.URL.Path, "/apis/"+computev1alpha.GroupVersion.String()+"/instancetypes"):
			listPaths = append(listPaths, r.URL.Path)
			writeJSON(t, w, &computev1alpha.InstanceTypeList{
				TypeMeta: metav1.TypeMeta{Kind: "InstanceTypeList", APIVersion: computev1alpha.GroupVersion.String()},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	scheme := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(scheme))

	readers, err := NewProjectReaders(&rest.Config{Host: srv.URL}, "provider", scheme)
	require.NoError(t, err)

	for _, project := range []string{"project-a", "project-b"} {
		reader, err := readers.Reader(project)
		require.NoError(t, err)
		require.NoError(t, reader.List(context.Background(), &computev1alpha.InstanceTypeList{}))
	}

	mu.Lock()
	defer mu.Unlock()
	instanceTypes := "/apis/" + computev1alpha.GroupVersion.String() + "/instancetypes"
	assert.Equal(t, []string{
		strings.Replace(controlPlane, "%s", "project-a", 1) + instanceTypes,
		strings.Replace(controlPlane, "%s", "project-b", 1) + instanceTypes,
	}, listPaths)
	assert.LessOrEqual(t, discoveryCalls, 3, "discovery should run once for the shared mapper")
}

func TestProjectReadersRejectEmptyProject(t *testing.T) {
	readers, err := NewProjectReaders(&rest.Config{Host: "https://milo.example"}, "provider", runtime.NewScheme())
	require.NoError(t, err)
	_, err = readers.Reader("")
	require.Error(t, err)
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
