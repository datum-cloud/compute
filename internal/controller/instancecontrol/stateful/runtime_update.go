package stateful

import (
	"fmt"
	"maps"
	"math"

	"k8s.io/apimachinery/pkg/api/equality"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

// runtimeUpdate deliberately supports only changes that leave placement, quota,
// and interface requests intact. Those changes need their own coordinated
// migration; falling back to deleting the Instance would release its addresses.
func runtimeUpdate(instance *v1alpha.Instance, deployment *v1alpha.WorkloadDeployment, hash string) (*v1alpha.Instance, error) {
	desired := &deployment.Spec.Template
	if !equality.Semantic.DeepEqual(instance.Spec.NetworkInterfaces, desired.Spec.NetworkInterfaces) ||
		!equality.Semantic.DeepEqual(instance.Spec.Location, &deployment.Spec.LocationRef) ||
		instance.Spec.Runtime.Class != desired.Spec.Runtime.Class ||
		!equality.Semantic.DeepEqual(instance.Spec.Runtime.Resources, desired.Spec.Runtime.Resources) {
		return nil, fmt.Errorf("instance %s: changing networking, location, runtime class or instance sizing during a runtime update is not supported", instance.Name)
	}
	if instance.Spec.Runtime.Sandbox == nil || desired.Spec.Runtime.Sandbox == nil {
		return nil, fmt.Errorf("instance %s: runtime updates currently require a sandbox", instance.Name)
	}
	if instance.Spec.Controller == nil || instance.Spec.Controller.RuntimeRevision == math.MaxInt64 {
		return nil, fmt.Errorf("instance %s: cannot advance runtime revision", instance.Name)
	}
	updated := instance.DeepCopy()
	desired.Spec.Runtime.DeepCopyInto(&updated.Spec.Runtime)
	updated.Spec.Volumes = desired.DeepCopy().Spec.Volumes
	updated.Spec.Controller.TemplateHash = hash
	updated.Spec.Controller.RuntimeRevision++
	// Keep controller/provider metadata and the stable owner reference. Template
	// metadata is merged; removing previously copied keys needs a separate
	// ownership contract and is not part of this initial runtime-update path.
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	maps.Copy(updated.Labels, desired.Labels)
	maps.Copy(updated.Annotations, desired.Annotations)
	return updated, nil
}

func runtimeUpdatePending(instance *v1alpha.Instance) bool {
	if instance.Spec.Controller == nil || instance.Spec.Controller.RuntimeRevision == 0 {
		return false
	}
	return instance.Status.Controller == nil ||
		instance.Status.Controller.ObservedRuntimeRevision != instance.Spec.Controller.RuntimeRevision ||
		instance.Status.Controller.ObservedTemplateHash != instance.Spec.Controller.TemplateHash
}

func (c *statefulControl) templateUpdateAction(instance *v1alpha.Instance, deployment *v1alpha.WorkloadDeployment, hash string) (instancecontrol.Action, error) {
	if c.opts.PreserveInstances || instance.Spec.Controller != nil && instance.Spec.Controller.RuntimeRevision > 0 {
		updated, err := runtimeUpdate(instance, deployment, hash)
		if err != nil {
			return instancecontrol.Action{}, err
		}
		return instancecontrol.NewUpdateAction(updated), nil
	}
	return instancecontrol.NewDeleteAction(instance), nil
}
