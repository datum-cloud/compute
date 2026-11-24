// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"reflect"
	"testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestSandboxRuntime(t *testing.T) {
	secrets := []computev1alpha.LocalSecretReference{{Name: "registry-creds"}}
	sysctls := []computev1alpha.SandboxSysctl{{Name: "net.core.somaxconn", Value: "1024"}}
	container := computev1alpha.SandboxContainer{Name: containerName, Image: "ghcr.io/acme/api:2"}

	stored := workloadWithPorts()
	stored.Spec.Template.Spec.Runtime.Sandbox.ImagePullSecrets = secrets
	stored.Spec.Template.Spec.Runtime.Sandbox.Sysctls = sysctls

	tests := []struct {
		name        string
		existing    *computev1alpha.Workload
		creating    bool
		wantSecrets []computev1alpha.LocalSecretReference
		wantSysctls []computev1alpha.SandboxSysctl
	}{
		{name: "redeploy keeps pull secrets and sysctls", existing: &stored, wantSecrets: secrets, wantSysctls: sysctls},
		{name: "create starts empty", existing: workload(), creating: true},
		{name: "redeploy of a workload without a sandbox", existing: workload()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sandboxRuntime(tt.existing, tt.creating, container)
			if !reflect.DeepEqual(got.Containers, []computev1alpha.SandboxContainer{container}) {
				t.Errorf("containers = %+v, want only the flag-built container", got.Containers)
			}
			if !reflect.DeepEqual(got.ImagePullSecrets, tt.wantSecrets) {
				t.Errorf("imagePullSecrets = %+v, want %+v", got.ImagePullSecrets, tt.wantSecrets)
			}
			if !reflect.DeepEqual(got.Sysctls, tt.wantSysctls) {
				t.Errorf("sysctls = %+v, want %+v", got.Sysctls, tt.wantSysctls)
			}
		})
	}
}
