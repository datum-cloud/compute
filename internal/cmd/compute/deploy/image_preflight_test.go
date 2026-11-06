// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"context"
	"errors"
	"fmt"
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

func manifestWorkload(class string, images ...string) *computev1alpha.Workload {
	w := &computev1alpha.Workload{ObjectMeta: metav1.ObjectMeta{Name: testWorkload}}
	w.Spec.Template.Spec.Runtime.Class = class
	sandbox := &computev1alpha.SandboxRuntime{}
	for i, image := range images {
		sandbox.Containers = append(sandbox.Containers, computev1alpha.SandboxContainer{Name: fmt.Sprintf("c%d", i), Image: image})
	}
	w.Spec.Template.Spec.Runtime.Sandbox = sandbox
	return w
}

func TestCheckManifestImages(t *testing.T) {
	const (
		containerImage = "ghcr.io/acme/api:1"
		computeImage   = "ghcr.io/acme/api-compute:1"
	)
	catalog := []client.Object{
		runtimeClass(classGeneralPurpose, "virtual-machine", false),
		runtimeClass(classUnikernel, "unikernel", true),
	}
	lookup := func(_ context.Context, image string) ([]string, error) {
		if image == computeImage {
			return []string{platformCompute}, nil
		}
		return []string{platformLinux}, nil
	}

	tests := []struct {
		name     string
		manifest *computev1alpha.Workload
		existing *computev1alpha.Workload
		wantErr  []string
	}{
		{
			name:     "container image with no class is checked against the default",
			manifest: manifestWorkload("", containerImage),
			wantErr: []string{
				containerImage, "container image", classUnikernel,
				"datumctl compute build --push --output <ref>",
				"spec.template.spec.runtime.class: " + classGeneralPurpose,
			},
		},
		{
			name:     "container image in the unikernel class is refused",
			manifest: manifestWorkload(classUnikernel, containerImage),
			wantErr:  []string{containerImage, classUnikernel},
		},
		{
			name:     "every container is checked",
			manifest: manifestWorkload(classUnikernel, computeImage, containerImage),
			wantErr:  []string{containerImage, classUnikernel},
		},
		{
			name:     "an update with no class is checked against the workload's class",
			manifest: manifestWorkload("", computeImage),
			existing: manifestWorkload(classGeneralPurpose, containerImage),
			wantErr:  []string{computeImage, "Compute image", classGeneralPurpose, "spec.template.spec.runtime.class: " + classUnikernel},
		},
		{
			name:     "Compute image in the unikernel class proceeds",
			manifest: manifestWorkload(classUnikernel, computeImage),
		},
		{
			name:     "Compute image with no class proceeds on a unikernel default",
			manifest: manifestWorkload("", computeImage),
		},
		{
			name:     "container image in the general-purpose class proceeds",
			manifest: manifestWorkload(classGeneralPurpose, containerImage),
		},
		{
			name:     "a manifest without a sandbox is left to admission",
			manifest: &computev1alpha.Workload{ObjectMeta: metav1.ObjectMeta{Name: "vm"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, 0, len(catalog))
			for _, c := range catalog {
				objs = append(objs, c.DeepCopyObject().(client.Object))
			}
			err := checkManifestImages(context.Background(), newFakeClient(t, objs...), tt.manifest, tt.existing, lookup)
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

func TestPlanRuntimeClass(t *testing.T) {
	catalog := newFakeClient(t,
		runtimeClass(classGeneralPurpose, "virtual-machine", false),
		runtimeClass(classUnikernel, "unikernel", true),
	)
	tests := []struct {
		name     string
		kube     client.Client
		class    string
		existing bool
		want     string
	}{
		{name: "a named class is shown as is", kube: catalog, class: classGeneralPurpose, want: classGeneralPurpose},
		{name: "no class names the catalog default", kube: catalog, want: classUnikernel + " (platform default)"},
		{name: "no class and no catalog says so", kube: newFakeClient(t), want: "platform default"},
		{name: "an existing workload with no class is left unnamed", kube: catalog, existing: true, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planRuntimeClass(context.Background(), tt.kube, tt.class, !tt.existing); got != tt.want {
				t.Errorf("planRuntimeClass() = %q, want %q", got, tt.want)
			}
		})
	}
}
