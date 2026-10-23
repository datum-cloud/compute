package util

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
	"go.miloapis.com/service-catalog/pkg/activation"
	"sigs.k8s.io/yaml"
)

// TestComputeServiceResolvesFromPublishedManifest checks the gate's lookup
// against the Service manifest compute publishes. If the name drifts from the
// manifest, every gated command fails. Keep the explicit self-service policy
// in sync with the service we publish.
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
	if svc.Spec.EnablementPolicy == nil || svc.Spec.EnablementPolicy.Mode != servicesv1alpha1.EnablementModeSelfService {
		t.Fatalf("published Compute service must explicitly enable self-service")
	}
	if info.EnablementMode != servicesv1alpha1.EnablementModeSelfService {
		t.Errorf("EnablementMode = %q, want %q", info.EnablementMode, servicesv1alpha1.EnablementModeSelfService)
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

type recordingEntitlements struct {
	activation.EntitlementClient
	creates int
}

func (r *recordingEntitlements) List(context.Context) (*servicesv1alpha1.ServiceEntitlementList, error) {
	return &servicesv1alpha1.ServiceEntitlementList{}, nil
}

func (r *recordingEntitlements) Create(context.Context, *servicesv1alpha1.ServiceEntitlement) error {
	r.creates++
	return nil
}

func TestCheckActivationNeverRequestsAccess(t *testing.T) {
	service := activation.ServiceInfo{
		CanonicalName:  ComputeServiceName,
		DisplayName:    "Compute",
		EnablementMode: servicesv1alpha1.EnablementModeSelfService,
	}
	ec := &recordingEntitlements{}
	var stderr bytes.Buffer
	streams := activation.IOStreams{In: strings.NewReader(""), Out: io.Discard, Err: &stderr}

	err := checkActivation(context.Background(), service, ec, streams, "test-project", "api.staging.example.net")

	if ec.creates != 0 {
		t.Fatalf("checkActivation created %d entitlements, want none", ec.creates)
	}
	if got := activation.ExitCodeOf(err); got != activation.ExitNotEnabled {
		t.Fatalf("exit code = %d, want %d (err %v)", got, activation.ExitNotEnabled, err)
	}
	for _, want := range []string{`"test-project"`, "api.staging.example.net", "datumctl compute access request"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestGateCheckOnly(t *testing.T) {
	root := &cobra.Command{Use: rootUse}
	exec := &cobra.Command{Use: "exec"}
	MarkGateCheckOnly(exec)
	child := &cobra.Command{Use: "child"}
	exec.AddCommand(child)
	deploy := &cobra.Command{Use: "deploy"}
	root.AddCommand(exec, deploy)

	if !GateCheckOnly(exec) {
		t.Error("exec should be check-only")
	}
	if !GateCheckOnly(child) {
		t.Error("a check-only command's subcommand should inherit it")
	}
	if GateCheckOnly(deploy) {
		t.Error("deploy should keep enabling on first use")
	}
}
