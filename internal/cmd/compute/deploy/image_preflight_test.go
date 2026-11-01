// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func runtimeClass(name, boundary string, isDefault bool) *computev1alpha.RuntimeClass {
	return &computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: computev1alpha.RuntimeClassSpec{
			Default:   isDefault,
			Isolation: computev1alpha.RuntimeClassIsolation{Boundary: boundary},
		},
	}
}

const (
	platformLinux   = "linux/amd64"
	platformCompute = "datum/x86_64"
)

func TestCheckImageRuntime(t *testing.T) {
	const image = "ghcr.io/acme/api:1"
	catalog := []*computev1alpha.RuntimeClass{
		runtimeClass(classGeneralPurpose, "virtual-machine", true),
		runtimeClass(classUnikernel, "unikernel", false),
	}

	tests := []struct {
		name      string
		class     string
		platforms []string
		lookupErr error
		wantErr   []string
	}{
		{
			name:      "container image into the unikernel class is refused",
			class:     classUnikernel,
			platforms: []string{platformLinux, "linux/arm64"},
			wantErr: []string{
				image, "container image", platformLinux, classUnikernel,
				"datumctl compute build --push --output <ref>", "--runtime-class=" + classGeneralPurpose,
			},
		},
		{
			name:      "Compute image into a container class is refused",
			class:     classGeneralPurpose,
			platforms: []string{platformCompute},
			wantErr:   []string{image, "Compute image", classGeneralPurpose, "--runtime-class=" + classUnikernel},
		},
		{
			name:      "no class checks against the catalog default",
			platforms: []string{platformCompute},
			wantErr:   []string{classGeneralPurpose},
		},
		{
			name:      "Compute image into the unikernel class proceeds",
			class:     classUnikernel,
			platforms: []string{platformCompute},
		},
		{
			name:      "older Compute image into the unikernel class proceeds",
			class:     classUnikernel,
			platforms: []string{"kraftcloud/x86_64"},
		},
		{
			name:      "container image into a container class proceeds",
			class:     classGeneralPurpose,
			platforms: []string{platformLinux},
		},
		{
			name:      "an index with both variants runs anywhere",
			class:     classUnikernel,
			platforms: []string{platformLinux, platformCompute},
		},
		{
			name:      "a class the catalog does not list is left to admission",
			class:     "gpu",
			platforms: []string{platformLinux},
		},
		{
			name:      "an image the registry will not show is left to the runtime",
			class:     classUnikernel,
			lookupErr: errors.New("401 Unauthorized"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, 0, len(catalog))
			for _, c := range catalog {
				objs = append(objs, c.DeepCopy())
			}
			kube := newFakeClient(t, objs...)
			lookup := func(context.Context, string) ([]string, error) { return tt.platforms, tt.lookupErr }

			err := checkImageRuntime(context.Background(), kube, image, tt.class, lookup)

			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestCheckImageRuntimeWithoutACatalog(t *testing.T) {
	kube := newFakeClient(t)
	lookup := func(context.Context, string) ([]string, error) { return []string{platformLinux}, nil }
	if err := checkImageRuntime(context.Background(), kube, "ghcr.io/acme/api:1", classUnikernel, lookup); err != nil {
		t.Fatalf("a missing catalog must not block the deploy: %v", err)
	}
}
