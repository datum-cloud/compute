package stateful

import (
	"strconv"
	"strings"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

func needsUpdate(instance *v1alpha.Instance, instanceTemplateHash string) bool {
	return instance.Spec.Controller == nil ||
		instance.Spec.Controller.TemplateHash != instanceTemplateHash
}

// getInstanceOrdinal returns the ordinal of the instance, or -1 if the instance
// does not have an ordinal.
func getInstanceOrdinal(name string) int {
	lastDash := strings.LastIndex(name, "-")
	if lastDash == -1 {
		return -1
	}

	ordinal := -1
	if i, err := strconv.Atoi(name[lastDash+1:]); err == nil {
		ordinal = i
	}

	return ordinal
}

func ascendingOrdinal(a, b instancecontrol.Action) int {
	if getInstanceOrdinal(a.Object.GetName()) < getInstanceOrdinal(b.Object.GetName()) {
		return -1
	} else {
		return 1
	}
}

func descendingOrdinal(a, b instancecontrol.Action) int {
	if getInstanceOrdinal(a.Object.GetName()) > getInstanceOrdinal(b.Object.GetName()) {
		return -1
	} else {
		return 1
	}
}
