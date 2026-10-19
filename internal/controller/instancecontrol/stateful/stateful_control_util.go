package stateful

import (
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

func needsUpdate(instance *v1alpha.Instance, instanceTemplateHash string) bool {
	return instance.Spec.Controller == nil ||
		instance.Spec.Controller.TemplateHash != instanceTemplateHash
}

// getInstanceOrdinal returns the ordinal recorded in the instance's
// InstanceIndexLabel, or -1 if the label is absent or not a non-negative
// integer.
func getInstanceOrdinal(obj metav1.Object) int {
	value, ok := obj.GetLabels()[v1alpha.InstanceIndexLabel]
	if !ok {
		return -1
	}

	ordinal, err := strconv.Atoi(value)
	if err != nil || ordinal < 0 {
		return -1
	}

	return ordinal
}

func ascendingOrdinal(a, b instancecontrol.Action) int {
	if getInstanceOrdinal(a.Object) < getInstanceOrdinal(b.Object) {
		return -1
	} else {
		return 1
	}
}

func descendingOrdinal(a, b instancecontrol.Action) int {
	if getInstanceOrdinal(a.Object) > getInstanceOrdinal(b.Object) {
		return -1
	} else {
		return 1
	}
}
