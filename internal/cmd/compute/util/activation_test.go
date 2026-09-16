package util

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
	"go.miloapis.com/service-catalog/pkg/activation"
	"sigs.k8s.io/yaml"
)

// TestComputeServiceResolvesFromPublishedManifest checks the gate's lookup
// against the Service manifest compute publishes. If the name drifts from the
// manifest, every gated command fails. If the manifest loses its enablement
// policy, the gate treats compute as self-service and submits access requests
// without asking.
func TestComputeServiceResolvesFromPublishedManifest(t *testing.T) {
	raw, err := os.ReadFile("../../../../config/components/service-catalog/service.yaml")
	if err != nil {
		t.Fatalf("reading service manifest: %v", err)
	}
	var svc servicesv1alpha1.Service
	if err := yaml.UnmarshalStrict(raw, &svc); err != nil {
		t.Fatalf("decoding service manifest: %v", err)
	}

	services := &servicesv1alpha1.ServiceList{Items: []servicesv1alpha1.Service{svc}}
	info, err := activation.FindService(services, ComputeServiceName)
	if err != nil {
		t.Fatalf("FindService(%q) = %v", ComputeServiceName, err)
	}
	if info.ObjectName != "compute" {
		t.Errorf("ObjectName = %q, want %q", info.ObjectName, "compute")
	}
	if info.EnablementMode != servicesv1alpha1.EnablementModeGatedByProvider {
		t.Errorf("EnablementMode = %q, want %q", info.EnablementMode, servicesv1alpha1.EnablementModeGatedByProvider)
	}
}

func TestGateExempt(t *testing.T) {
	root := &cobra.Command{Use: "compute"}

	access := &cobra.Command{Use: "access"}
	MarkGateExempt(access)
	request := &cobra.Command{Use: "request"}
	access.AddCommand(request)

	instances := &cobra.Command{Use: "instances"}
	completion := &cobra.Command{Use: "completion"}
	bash := &cobra.Command{Use: "bash"}
	completion.AddCommand(bash)
	help := &cobra.Command{Use: "help"}
	complete := &cobra.Command{Use: cobra.ShellCompRequestCmd}

	root.AddCommand(access, instances, completion, help, complete)

	tests := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{"access is exempt", access, true},
		{"access request inherits exemption", request, true},
		{"completion is exempt", completion, true},
		{"completion subcommand is exempt", bash, true},
		{"help is exempt", help, true},
		{"__complete is exempt", complete, true},
		{"a data command is gated", instances, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := GateExempt(tc.cmd); got != tc.want {
				t.Fatalf("GateExempt(%s) = %v, want %v", tc.cmd.Name(), got, tc.want)
			}
		})
	}
}
