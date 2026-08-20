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
	"go.datum.net/compute/internal/referenceddata"
)

const (
	labelServiceKey   = "services.miloapis.com/service-name"
	labelServiceValue = "compute.datumapis.com"
)

// Options controls optional behaviours of the stateful instance control strategy.
type Options struct {
	// NetworkingEnabled controls whether the Network scheduling gate is added to
	// newly created Instances. Set to false when the networking integration is
	// disabled so that Instances are not blocked waiting for their addresses.
	// Defaults to true.
	NetworkingEnabled bool

	// EnableReferencedDataGate controls whether new Instances receive the
	// ReferencedData scheduling gate when the workload template references
	// ConfigMaps or Secrets. Defaults to false. See FeatureFlagsConfig for the
	// full safety rationale.
	EnableReferencedDataGate bool
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
	desiredReplicas int32,
	currentInstances []v1alpha.Instance,
) ([]instancecontrol.Action, error) {
	instanceTemplateHash := instancecontrol.ComputeHash(deployment.Spec.Template)

	// lowest -> highest
	var createActions []instancecontrol.Action
	var waitActions []instancecontrol.Action

	// highest -> lowest. Instances whose template hash has drifted from the
	// desired template are deleted and recreated (not updated in place) so the
	// change actually rolls the backing pod — see the recreate branch below.
	var recreateActions []instancecontrol.Action

	// highest -> lowest
	var deleteActions []instancecontrol.Action

	// Instances that are desired to exist. We do not currently support the
	// concept of a partition, so will fill the entire slice.
	desiredInstances := make([]*v1alpha.Instance, desiredReplicas)

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
	for i := range desiredReplicas {
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

			// Stamp the ReferencedData gate only when the management-plane feature
			// flag is on AND the template actually references ConfigMaps or Secrets.
			// The gate must not be inserted before the cell gate-clearing reconciler
			// and provider gate-honoring are deployed everywhere — see
			// FeatureFlagsConfig.EnableReferencedDataGate for the full rationale.
			if c.opts.EnableReferencedDataGate && referenceddata.TemplateReferencesData(deployment.Spec.Template) {
				gates = append(gates, v1alpha.SchedulingGate{Name: instancecontrol.ReferencedDataSchedulingGate.String()})
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
				// The instance's template hash no longer matches the desired
				// template — e.g. an image change, or a restart requested via the
				// RestartedAtAnnotation, which is part of the template hash. The
				// unikraft provider bakes the pod's runtime, rootfs, and file
				// mounts at pod-creation time and never reconciles an existing
				// pod's spec, so an in-place Instance update would silently fail to
				// roll the running workload. Delete the instance instead; the next
				// reconcile recreates it from the current template via the create
				// path above, and the provider tears down the old pod
				// (finalizer-gated) and boots a fresh one. Ordered, one-at-a-time
				// pacing is preserved by the descending-ordinal sort, the
				// skip-all-but-first logic, and the DeletionTimestamp WaitAction.
				recreateActions = append(recreateActions, instancecontrol.NewDeleteAction(instance))
			}
		}
	}

	// Converge controller-managed labels on every existing instance, regardless
	// of Ready state or template hash. Labels are stamped only at instance
	// creation and rollout is recreate-only, so when the label schema evolves —
	// a label is added or its value derivation changes — this pass is the only
	// mechanism that updates live instances; without it, any instance alive at
	// the time of the change would never receive it. The patch is metadata-only
	// and is emitted outside the ordered rollout decision so it never gates or
	// reorders instance creation/updates.
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

	slices.SortFunc(recreateActions, descendingOrdinal)
	slices.SortFunc(deleteActions, descendingOrdinal)

	actions := make([]instancecontrol.Action, 0, len(createActions)+len(waitActions)+len(recreateActions)+len(deleteActions)+len(patchLabelActions))

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

		actions = append(actions, recreateActions...)
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
// that every instance should carry. Used both when stamping a new instance
// and when checking whether an existing instance needs a backfill patch.
func desiredControllerLabels(index int, deployment *v1alpha.WorkloadDeployment) map[string]string {
	return map[string]string{
		v1alpha.InstanceIndexLabel:         strconv.Itoa(index),
		v1alpha.WorkloadUIDLabel:           string(deployment.Spec.WorkloadRef.UID),
		v1alpha.WorkloadDeploymentUIDLabel: string(deployment.GetUID()),
		// Self-describing labels for routing, filtering, and observability.
		v1alpha.WorkloadDeploymentNameLabel: deployment.GetName(),
		v1alpha.CityCodeLabel:               deployment.Spec.CityCode,
		v1alpha.WorkloadNameLabel:           deployment.Spec.WorkloadRef.Name,
		v1alpha.PlacementNameLabel:          deployment.Spec.PlacementName,
		// Scopes consumer-project cleanup: the consumer provider deletes
		// resources by this label when a project's ServiceConsumer is revoked.
		labelServiceKey: labelServiceValue,
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
