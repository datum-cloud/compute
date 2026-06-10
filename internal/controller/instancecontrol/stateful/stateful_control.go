package stateful

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

// Options controls optional behaviours of the stateful instance control strategy.
type Options struct {
	// NetworkingEnabled controls whether the Network scheduling gate is added to
	// newly created Instances. Set to false when the networking integration is
	// disabled so that Instances are not blocked waiting for a NetworkBinding.
	// Defaults to true.
	NetworkingEnabled bool
}

// Behavior inspired by https://github.com/kubernetes/kubernetes/tree/master/pkg/controller/statefulset
// Does not currently implement exact behavior.
type statefulControl struct {
	opts Options
}

// New returns a stateful instance control strategy with networking enabled.
func New() instancecontrol.Strategy {
	return NewWithOptions(Options{NetworkingEnabled: true})
}

// NewWithOptions returns a stateful instance control strategy with the given
// options.
func NewWithOptions(opts Options) instancecontrol.Strategy {
	return &statefulControl{opts: opts}
}

func (c *statefulControl) GetActions(
	ctx context.Context,
	scheme *runtime.Scheme,
	deployment *v1alpha.WorkloadDeployment,
	currentInstances []v1alpha.Instance,
) ([]instancecontrol.Action, error) {
	instanceTemplateHash := instancecontrol.ComputeHash(deployment.Spec.Template)

	// lowest -> highest
	var createActions []instancecontrol.Action
	var waitActions []instancecontrol.Action

	// highest -> lowest
	var updateActions []instancecontrol.Action

	// highest -> lowest
	var deleteActions []instancecontrol.Action

	// Instances that are desired to exist. We do not currently support the
	// concept of a partition, so will fill the entire slice.
	desiredInstances := make([]*v1alpha.Instance, deployment.Spec.ScaleSettings.MinReplicas)

	for _, instance := range currentInstances {
		instanceIndex := getInstanceOrdinal(instance.Name)
		if instanceIndex >= len(desiredInstances) {
			deleteActions = append(deleteActions, instancecontrol.NewDeleteAction(&instance))
		} else {
			desiredInstances[instanceIndex] = &instance
		}
	}

	// It's possible that the incoming currentInstances will have gaps in
	// instances, so fill them in.
	for i := range deployment.Spec.ScaleSettings.MinReplicas {
		if desiredInstances[i] == nil {
			desiredInstances[i] = &v1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      deployment.Spec.Template.Labels,
					Annotations: deployment.Spec.Template.Annotations,
					Name:        fmt.Sprintf("%s-%d", deployment.Name, i),
					Namespace:   deployment.Namespace,
				},
				Spec: deployment.Spec.Template.Spec,
			}
			// Set Location best-effort: when Status.Location is nil (no matching
			// Location object for the city code) Instance.Spec.Location stays nil and
			// instance creation proceeds normally — this must not block scheduling.
			desiredInstances[i].Spec.Location = deployment.Status.Location

			// TODO(jreese) consider adding scheduling gates via mutating webhooks
			gates := []v1alpha.SchedulingGate{
				{Name: instancecontrol.QuotaSchedulingGate.String()},
			}
			if c.opts.NetworkingEnabled {
				// Prepend the Network gate so it is cleared first; quota is
				// independent and evaluated in parallel by InstanceReconciler.
				gates = append([]v1alpha.SchedulingGate{
					{Name: instancecontrol.NetworkSchedulingGate.String()},
				}, gates...)
			}
			desiredInstances[i].Spec.Controller = &v1alpha.InstanceController{
				TemplateHash:    instanceTemplateHash,
				SchedulingGates: gates,
			}

			addInstanceControllerLabels(desiredInstances[i], getInstanceOrdinal(desiredInstances[i].Name), deployment)

			if err := controllerutil.SetControllerReference(deployment, desiredInstances[i], scheme); err != nil {
				return nil, fmt.Errorf("failed to set controller reference: %w", err)
			}
		}
	}

	for _, instance := range desiredInstances {
		if instance.CreationTimestamp.IsZero() {
			action := instancecontrol.NewCreateAction(instance)

			createActions = append(createActions, action)
		} else if !instance.DeletionTimestamp.IsZero() {
			// Wait for graceful deletion before continuing processing additional
			// instances.
			waitActions = append(waitActions, instancecontrol.NewWaitAction(instance))

		} else if instance.DeletionTimestamp.IsZero() {
			// Wait for the instance to be ready before continuing processing
			if !apimeta.IsStatusConditionTrue(instance.Status.Conditions, v1alpha.InstanceReady) {
				waitActions = append(waitActions, instancecontrol.NewWaitAction(instance))
			} else if needsUpdate(instance, instanceTemplateHash) {
				updatedInstance := instance.DeepCopy()
				updatedInstance.Annotations = deployment.Spec.Template.Annotations
				updatedInstance.Labels = deployment.Spec.Template.Labels

				addInstanceControllerLabels(updatedInstance, getInstanceOrdinal(updatedInstance.Name), deployment)

				updatedInstance.Spec = deployment.Spec.Template.Spec
				updateActions = append(updateActions, instancecontrol.NewUpdateAction(updatedInstance))
			}
		}
	}

	// Backfill controller-managed labels on every existing instance, regardless
	// of Ready state or template hash. This ensures newly-introduced labels
	// (e.g. city-code, workload-name) are applied to pre-existing instances that
	// were never touched by a rolling update. The patch is metadata-only and is
	// emitted outside the ordered rollout decision so it never gates or reorders
	// instance creation/updates.
	var patchLabelActions []instancecontrol.Action
	for _, instance := range desiredInstances {
		if instance.CreationTimestamp.IsZero() || !instance.DeletionTimestamp.IsZero() {
			// Skip instances that don't exist yet or are being deleted.
			continue
		}

		desiredLabels := desiredControllerLabels(getInstanceOrdinal(instance.Name), deployment)
		if labelsNeedBackfill(instance.Labels, desiredLabels) {
			base := instance.DeepCopy()
			patched := instance.DeepCopy()
			for k, v := range desiredLabels {
				if patched.Labels == nil {
					patched.Labels = make(map[string]string)
				}
				patched.Labels[k] = v
			}
			patchLabelActions = append(patchLabelActions, instancecontrol.NewPatchLabelsAction(patched, base))
		}
	}

	slices.SortFunc(updateActions, descendingOrdinal)
	slices.SortFunc(deleteActions, descendingOrdinal)

	actions := make([]instancecontrol.Action, 0, len(createActions)+len(waitActions)+len(updateActions)+len(deleteActions)+len(patchLabelActions))

	switch deployment.Spec.ScaleSettings.InstanceManagementPolicy {
	case v1alpha.OrderedReadyInstanceManagementPolicyType:

		// Add create and wait actions, and sort by ordinal. This allows us to wait
		// for instances to be processed in the correct order.
		//
		// For instance, we may have instance 0 that needs to wait to be ready, but
		// instance 1 wants to be created.
		actions = append(actions, createActions...)
		actions = append(actions, waitActions...)

		slices.SortFunc(actions, ascendingOrdinal)

		actions = append(actions, updateActions...)
		actions = append(actions, deleteActions...)

		// Skip all actions except the first one.
		for i := range actions {
			if i > 0 {
				actions[i].SkipExecution()
			}
		}

	}

	actions = append(actions, patchLabelActions...)

	return actions, nil
}

func addInstanceControllerLabels(instance *v1alpha.Instance, index int, deployment *v1alpha.WorkloadDeployment) {
	if instance.Labels == nil {
		instance.Labels = map[string]string{}
	}

	for k, v := range desiredControllerLabels(index, deployment) {
		instance.Labels[k] = v
	}
}

// desiredControllerLabels returns the full set of controller-managed labels
// that every instance should carry. Used both when stamping a new/updated
// instance and when checking whether an existing instance needs a backfill
// patch.
func desiredControllerLabels(index int, deployment *v1alpha.WorkloadDeployment) map[string]string {
	return map[string]string{
		v1alpha.InstanceIndexLabel:         strconv.Itoa(index),
		v1alpha.WorkloadUIDLabel:           string(deployment.Spec.WorkloadRef.UID),
		v1alpha.WorkloadDeploymentUIDLabel: string(deployment.GetUID()),
		// Self-describing labels for routing, filtering, and observability.
		// Backfilled on every reconcile so they stay accurate even for instances
		// that pre-date the labels or that were not reached by a rolling update.
		v1alpha.WorkloadDeploymentNameLabel: deployment.GetName(),
		v1alpha.CityCodeLabel:               deployment.Spec.CityCode,
		v1alpha.WorkloadNameLabel:           deployment.Spec.WorkloadRef.Name,
		v1alpha.PlacementNameLabel:          deployment.Spec.PlacementName,
	}
}

// labelsNeedBackfill reports whether any of the desired controller-managed
// label key/value pairs are absent or incorrect on the current instance labels.
func labelsNeedBackfill(current map[string]string, desired map[string]string) bool {
	for k, v := range desired {
		if current[k] != v {
			return true
		}
	}
	return false
}
