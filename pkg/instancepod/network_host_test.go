// SPDX-License-Identifier: AGPL-3.0-only

package instancepod

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const testHostNode = "host-a"

func TestBuildPodRecordsNetworkHost(t *testing.T) {
	instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName, Image: testNginxImage})

	pod, err := BuildPod(instance, Options{Capabilities: sandboxCapabilities, NetworkHost: testHostNode})
	if err != nil {
		t.Fatalf("BuildPod() returned an unexpected error: %v", err)
	}
	if got := pod.Labels[computev1alpha.NetworkHostLabel]; got != testHostNode {
		t.Errorf("network host label = %q, want %q", got, testHostNode)
	}

	pod, err = BuildPod(instance, Options{
		Capabilities: sandboxCapabilities,
		NetworkHost:  testHostNode,
		PodLabels:    map[string]string{computev1alpha.NetworkHostLabel: "stale"},
	})
	if err != nil {
		t.Fatalf("BuildPod() returned an unexpected error: %v", err)
	}
	if got := pod.Labels[computev1alpha.NetworkHostLabel]; got != testHostNode {
		t.Errorf("network host label = %q, want NetworkHost %q to win over PodLabels", got, testHostNode)
	}
}

func TestBuildPodWithoutNetworkHostLeavesLabelUnset(t *testing.T) {
	instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName, Image: testNginxImage})

	pod, err := BuildPod(instance, Options{Capabilities: sandboxCapabilities})
	if err != nil {
		t.Fatalf("BuildPod() returned an unexpected error: %v", err)
	}
	if got, ok := pod.Labels[computev1alpha.NetworkHostLabel]; ok {
		t.Errorf("network host label = %q, want it unset until the host is known", got)
	}
}

func TestSetNetworkHost(t *testing.T) {
	pod := &corev1.Pod{}

	if SetNetworkHost(pod, "") {
		t.Fatal("SetNetworkHost() with no Node reported a change")
	}
	if _, ok := pod.Labels[computev1alpha.NetworkHostLabel]; ok {
		t.Fatal("SetNetworkHost() with no Node set the label")
	}

	if !SetNetworkHost(pod, testHostNode) {
		t.Fatal("SetNetworkHost() on an unlabelled Pod reported no change")
	}
	if got := pod.Labels[computev1alpha.NetworkHostLabel]; got != testHostNode {
		t.Fatalf("network host label = %q, want %q", got, testHostNode)
	}
	if SetNetworkHost(pod, testHostNode) {
		t.Error("SetNetworkHost() with the recorded host reported a change")
	}

	if !SetNetworkHost(pod, "host-b") {
		t.Error("SetNetworkHost() with a different host reported no change")
	}
	if !computev1alpha.NetworkHostSelector("host-b").Matches(labels.Set(pod.Labels)) {
		t.Error("the Pod is not selected by its new host's selector")
	}
}
