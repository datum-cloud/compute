// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const networkBackend = "backend"

// storedWorkloadOnNetworks is a stored workload whose interfaces attach to the
// given networks, with the interface defaults admission records.
func storedWorkloadOnNetworks(networks ...string) *computev1alpha.Workload {
	w := storedWorkload(classGeneralPurpose)
	template := w.Spec.Template.Spec.NetworkInterfaces[0]
	w.Spec.Template.Spec.NetworkInterfaces = nil
	for i, name := range networks {
		iface := template
		iface.Name = fmt.Sprintf("eth%d", i)
		iface.Network = networkingv1alpha.NetworkRef{Name: name}
		w.Spec.Template.Spec.NetworkInterfaces = append(w.Spec.Template.Spec.NetworkInterfaces, iface)
	}
	return w
}

// TestResolveNetworkInterfaces covers the rule that a workload stays on its
// network. A redeploy that names no network keeps the existing interfaces, and
// one that names a different network is refused before anything is prompted
// for or created. The network check must target the network the workload ends
// up on, never "default" for a workload attached elsewhere.
func TestResolveNetworkInterfaces(t *testing.T) {
	tests := []struct {
		name         string
		existing     *computev1alpha.Workload
		creating     bool
		requested    string
		wantNetworks []string
		wantErr      []string
	}{
		{
			name:         "create without a network uses default",
			existing:     workload(),
			creating:     true,
			wantNetworks: []string{defaultNetworkName},
		},
		{
			name:         "create with a network uses it",
			existing:     workload(),
			creating:     true,
			requested:    networkBackend,
			wantNetworks: []string{networkBackend},
		},
		{
			name:         "update without a network keeps the existing one",
			existing:     storedWorkloadOnNetworks(networkBackend),
			wantNetworks: []string{networkBackend},
		},
		{
			name:         "update with the same network proceeds",
			existing:     storedWorkloadOnNetworks(networkBackend),
			requested:    networkBackend,
			wantNetworks: []string{networkBackend},
		},
		{
			name:      "update with a different network is refused",
			existing:  storedWorkloadOnNetworks(networkBackend),
			requested: defaultNetworkName,
			wantErr: []string{
				`"` + networkBackend + `"`, `"` + defaultNetworkName + `"`,
				"cannot move", "datumctl compute destroy " + testWorkload, "new name",
			},
		},
		{
			name:         "update of a multi-network workload without a network keeps them",
			existing:     storedWorkloadOnNetworks(networkBackend, defaultNetworkName),
			wantNetworks: []string{networkBackend, defaultNetworkName},
		},
		{
			name:      "update of a multi-network workload with a network is refused",
			existing:  storedWorkloadOnNetworks(networkBackend, defaultNetworkName),
			requested: networkBackend,
			wantErr:   []string{"more than one network", "manifest", "-f"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveNetworkInterfaces(tc.existing, tc.creating, tc.requested)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("want an error, got interfaces %+v", got)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not mention %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if names := networkNames(got); !reflect.DeepEqual(names, tc.wantNetworks) {
				t.Errorf("networks checked = %v, want %v", names, tc.wantNetworks)
			}
			if !tc.creating && !reflect.DeepEqual(got, tc.existing.Spec.Template.Spec.NetworkInterfaces) {
				t.Errorf("interfaces = %+v, want the stored ones %+v", got, tc.existing.Spec.Template.Spec.NetworkInterfaces)
			}
		})
	}
}

// TestNetworkNamesDeduplicates checks that a network shared by several
// interfaces is checked once.
func TestNetworkNamesDeduplicates(t *testing.T) {
	w := storedWorkloadOnNetworks(networkBackend, defaultNetworkName, networkBackend)
	want := []string{networkBackend, defaultNetworkName}
	if got := networkNames(w.Spec.Template.Spec.NetworkInterfaces); !reflect.DeepEqual(got, want) {
		t.Errorf("networkNames = %v, want %v", got, want)
	}
}

// TestNetworkFlag checks that the flag is registered and follows the -f
// convention of the other spec-shaping flags, where a manifest declares its
// own interfaces.
func TestNetworkFlag(t *testing.T) {
	cmd, opts := command()
	if cmd.Flags().Lookup("network") == nil {
		t.Fatal("--network must be registered")
	}
	if err := cmd.Flags().Parse([]string{"--file=manifest.yaml", "--network=" + networkBackend}); err != nil {
		t.Fatalf("parsing flags: %v", err)
	}
	err := validateFlags(cmd, opts)
	if err == nil || !strings.Contains(err.Error(), "--network cannot be combined with -f") ||
		!strings.Contains(err.Error(), "in the manifest") {
		t.Fatalf("want the -f conflict error, got %v", err)
	}
}
