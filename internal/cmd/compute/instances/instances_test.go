package instances

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestDeploymentNameForInstance(t *testing.T) {
	const deploymentName = "checkout-api-7c1e04b9a2"

	ownerRef := func(apiVersion, kind, name string) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name}
	}

	tests := []struct {
		name   string
		labels map[string]string
		owners []metav1.OwnerReference
		want   string
	}{
		{
			name:   "label",
			labels: map[string]string{computev1alpha.WorkloadDeploymentNameLabel: deploymentName},
			owners: []metav1.OwnerReference{ownerRef(computev1alpha.GroupVersion.String(), "WorkloadDeployment", "other")},
			want:   deploymentName,
		},
		{
			name:   "owner reference",
			owners: []metav1.OwnerReference{ownerRef(computev1alpha.GroupVersion.String(), "WorkloadDeployment", deploymentName)},
			want:   deploymentName,
		},
		{
			name:   "owner of another kind",
			owners: []metav1.OwnerReference{ownerRef("apps/v1", "WorkloadDeployment", deploymentName)},
			want:   "",
		},
		{
			name: "name is never parsed",
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inst := &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:            deploymentName + "-0",
					Labels:          test.labels,
					OwnerReferences: test.owners,
				},
			}
			if got := deploymentNameForInstance(inst); got != test.want {
				t.Errorf("deploymentNameForInstance() = %q, want %q", got, test.want)
			}
		})
	}
}
