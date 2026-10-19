// SPDX-License-Identifier: AGPL-3.0-only

package instancepod

import (
	"testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/pkg/runtimeclass"
)

func TestExecCapablePodsReceiveReadOnlyShellHelper(t *testing.T) {
	instance := newInstance(computev1alpha.SandboxContainer{Name: "app"})
	opts := Options{Capabilities: runtimeclass.Capabilities{Features: []runtimeclass.Feature{
		runtimeclass.FeatureSandboxRuntime, runtimeclass.FeatureExec,
	}}, ShellReaperImage: "ghcr.io/datum-cloud/compute@sha256:test"}
	pod, err := BuildPod(instance, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.InitContainers) != 1 || len(pod.Spec.Volumes) != 1 {
		t.Fatalf("helper installer and volume missing: %#v", pod.Spec)
	}
	if pod.Spec.InitContainers[0].Image != opts.ShellReaperImage ||
		pod.Spec.InitContainers[0].SecurityContext.RunAsUser != nil {
		t.Fatal("helper must use the pinned image and remain non-root")
	}
	mounts := pod.Spec.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].MountPath != "/run/datum-shell" || !mounts[0].ReadOnly {
		t.Fatalf("helper mount = %#v", mounts)
	}
}
