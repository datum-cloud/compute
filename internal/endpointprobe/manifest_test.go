// SPDX-License-Identifier: AGPL-3.0-only

package endpointprobe

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const shellAgentComponent = "../../config/components/shell-agent/"

func readStatefulSet(t *testing.T, file string) *appsv1.StatefulSet {
	t.Helper()
	raw, err := os.ReadFile(shellAgentComponent + file)
	if err != nil {
		t.Fatal(err)
	}
	var sts appsv1.StatefulSet
	if err := yaml.UnmarshalStrict(raw, &sts); err != nil {
		t.Fatal(err)
	}
	return &sts
}

func container(t *testing.T, sts *appsv1.StatefulSet, match func(corev1.Container) bool) corev1.Container {
	t.Helper()
	for _, c := range sts.Spec.Template.Spec.Containers {
		if match(c) {
			return c
		}
	}
	t.Fatalf("%s has no matching container", sts.Name)
	return corev1.Container{}
}

func flag(c corev1.Container, name string) string {
	for _, arg := range c.Args {
		if v, ok := strings.CutPrefix(arg, "--"+name+"="); ok {
			return v
		}
	}
	return ""
}

func relaySources(c corev1.Container) []string {
	var out []string
	for _, from := range c.EnvFrom {
		if from.ConfigMapRef != nil {
			out = append(out, from.ConfigMapRef.Name)
		}
	}
	return out
}

// The endpoint's pod must be ready only while the probe reaches the endpoint
// through the relays its agent publishes, because the agent claims sessions
// only while that pod is ready.
func TestEndpointReadinessFollowsRelayReachability(t *testing.T) {
	endpoints := readStatefulSet(t, "endpoint.yaml")
	agents := readStatefulSet(t, "agent.yaml")

	serve := container(t, endpoints, func(c corev1.Container) bool { return slices.Contains(c.Args, "serve") })
	probe := container(t, endpoints, func(c corev1.Container) bool {
		return slices.Equal(c.Command, []string{"/shell-endpoint-probe"})
	})
	agent := container(t, agents, func(c corev1.Container) bool {
		return slices.Equal(c.Command, []string{"/shell-agent"})
	})

	if probe.ReadinessProbe == nil || probe.ReadinessProbe.HTTPGet == nil ||
		probe.ReadinessProbe.HTTPGet.Path != "/readyz" {
		t.Fatalf("the probe container gates no readiness: %+v", probe.ReadinessProbe)
	}
	port := probe.ReadinessProbe.HTTPGet.Port.String()
	if !slices.ContainsFunc(probe.Ports, func(p corev1.ContainerPort) bool {
		return p.Name == port && flag(probe, "listen") == ":"+strconv.Itoa(int(p.ContainerPort))
	}) {
		t.Fatalf("readiness port %q is not the port the probe listens on (%s)", port, flag(probe, "listen"))
	}
	if got, want := flag(probe, "key-file"), flag(serve, "listen-key-file"); got == "" || got != want {
		t.Fatalf("probe key file %q, want the endpoint's %q", got, want)
	}
	if got, want := flag(probe, "target"), flag(serve, "tcp-proxy"); got == "" || got != want {
		t.Fatalf("probe target %q, want the endpoint's proxy target %q", got, want)
	}
	if got, want := relaySources(probe), relaySources(agent); !slices.Equal(got, want) || len(got) == 0 {
		t.Fatalf("probe relays come from %v, want the agent's published relays from %v", got, want)
	}
}
