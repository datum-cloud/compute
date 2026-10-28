package stateful

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

func TestGetInstanceOrdinal(t *testing.T) {
	tests := []struct {
		name       string
		objectName string
		want       int
	}{
		{
			name:       "instance with ordinal 0",
			objectName: "my-instance-0",
			want:       0,
		},
		{
			name:       "instance with ordinal 1",
			objectName: "my-instance-1",
			want:       1,
		},
		{
			name:       "instance with unexpected suffix",
			objectName: "my-instance-foo",
			want:       -1,
		},
		{
			name:       "instance with no dash in name",
			objectName: "myinstance",
			want:       -1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := getInstanceOrdinal(test.objectName)
			if got != test.want {
				t.Errorf("getInstanceOrdinal(%q) = %d, want %d", test.objectName, got, test.want)
			}
		})
	}
}

func TestDescendingOrdinal(t *testing.T) {
	actions := make([]instancecontrol.Action, 0, 4)

	perm := rand.Perm(4)
	for i := range perm {
		actions = append(actions, instancecontrol.NewWaitAction(
			&v1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("my-instance-%d", perm[i]),
				},
			},
		))
	}

	slices.SortFunc(actions, descendingOrdinal)

	assert.Equal(t, actions[0].Object.GetName(), "my-instance-3")
	assert.Equal(t, actions[1].Object.GetName(), "my-instance-2")
	assert.Equal(t, actions[2].Object.GetName(), "my-instance-1")
	assert.Equal(t, actions[3].Object.GetName(), "my-instance-0")
}

func TestNeedsUpdate(t *testing.T) {
	deployment := getWorkloadDeployment("test-needs-update", 1)
	template := deployment.Spec.Template
	changed := *template.DeepCopy()
	changed.Spec.Runtime.Sandbox.Containers[0].Image = "changed"

	tests := []struct {
		name          string
		storedHash    *string
		desired       v1alpha.InstanceTemplateSpec
		want          bool
		wantLegacyFix bool
	}{
		{
			name:       "no controller state",
			storedHash: nil,
			desired:    template,
			want:       true,
		},
		{
			name:       "current hash of unchanged template",
			storedHash: ptr.To(instancecontrol.ComputeHash(template)),
			desired:    template,
			want:       false,
		},
		{
			name:          "legacy hash of unchanged template",
			storedHash:    ptr.To(instancecontrol.LegacyComputeHash(template)),
			desired:       template,
			want:          false,
			wantLegacyFix: true,
		},
		{
			name:       "current hash of changed template",
			storedHash: ptr.To(instancecontrol.ComputeHash(template)),
			desired:    changed,
			want:       true,
		},
		{
			name:       "legacy hash of changed template",
			storedHash: ptr.To(instancecontrol.LegacyComputeHash(template)),
			desired:    changed,
			want:       true,
		},
		{
			name:       "unrecognised hash",
			storedHash: ptr.To("stale-hash"),
			desired:    template,
			want:       true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance := &v1alpha.Instance{}
			if test.storedHash != nil {
				instance.Spec.Controller = &v1alpha.InstanceController{TemplateHash: *test.storedHash}
			}
			hashes := instancecontrol.HashTemplate(test.desired)

			assert.Equal(t, test.want, needsUpdate(instance, hashes))
			assert.Equal(t, test.wantLegacyFix, hasLegacyTemplateHash(instance, hashes))
		})
	}
}
