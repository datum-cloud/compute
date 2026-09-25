package stateful

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

func TestGetInstanceOrdinal(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   int
	}{
		{
			name:   "ordinal 0",
			labels: map[string]string{v1alpha.InstanceIndexLabel: "0"},
			want:   0,
		},
		{
			name:   "ordinal 12",
			labels: map[string]string{v1alpha.InstanceIndexLabel: "12"},
			want:   12,
		},
		{
			name:   "missing label",
			labels: map[string]string{},
			want:   -1,
		},
		{
			name:   "non-numeric label",
			labels: map[string]string{v1alpha.InstanceIndexLabel: "foo"},
			want:   -1,
		},
		{
			name:   "negative label",
			labels: map[string]string{v1alpha.InstanceIndexLabel: "-1"},
			want:   -1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance := &v1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "checkout-api-7c1e04b9a2-3",
					Labels: test.labels,
				},
			}
			got := getInstanceOrdinal(instance)
			if got != test.want {
				t.Errorf("getInstanceOrdinal(%v) = %d, want %d", test.labels, got, test.want)
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
					Name:   fmt.Sprintf("my-instance-%d", perm[i]),
					Labels: map[string]string{v1alpha.InstanceIndexLabel: strconv.Itoa(perm[i])},
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
