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

// Behavior inspired by https://github.com/kubernetes/kubernetes/tree/master/pkg/controller/statefulset
// Does not currently implement exact behavior.
type statefulControl struct {
}

func New() instancecontrol.Strategy {
	return &statefulControl{}
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
			desiredInstances[i].Spec.Location = deployment.Status.Location

			// TODO(jreese) consider adding scheduling gates via mutating webhooks
			desiredInstances[i].Spec.Controller = &v1alpha.InstanceController{
				TemplateHash: instanceTemplateHash,
				SchedulingGates: []v1alpha.SchedulingGate{
					{
						Name: instancecontrol.NetworkSchedulingGate.String(),
					},
				},
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

	slices.SortFunc(updateActions, descendingOrdinal)
	slices.SortFunc(deleteActions, descendingOrdinal)

	actions := make([]instancecontrol.Action, 0, len(createActions)+len(waitActions)+len(updateActions)+len(deleteActions))

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

	return actions, nil
}

func addInstanceControllerLabels(instance *v1alpha.Instance, index int, deployment *v1alpha.WorkloadDeployment) {
	if instance.Labels == nil {
		instance.Labels = map[string]string{}
	}

	instance.Labels[v1alpha.InstanceIndexLabel] = strconv.Itoa(index)
	instance.Labels[v1alpha.WorkloadUIDLabel] = string(deployment.Spec.WorkloadRef.UID)
	instance.Labels[v1alpha.WorkloadDeploymentUIDLabel] = string(deployment.GetUID())
}
