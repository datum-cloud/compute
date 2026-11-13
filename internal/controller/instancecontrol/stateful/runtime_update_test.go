package stateful

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

func TestRuntimeUpdateKeepsIdentityAndWaitsForAppliedRevision(t *testing.T) {
	deployment := getWorkloadDeployment("stable", 2)
	deployment.Spec.Template.Spec.NetworkInterfaces = []v1alpha.InstanceNetworkInterface{{Name: "eth0"}}
	control := NewWithOptions(Options{PreserveInstances: true})
	instances := []v1alpha.Instance{*getInstanceForDeployment(deployment, 0), *getInstanceForDeployment(deployment, 1)}
	for i := range instances {
		instances[i].UID = types.UID(instances[i].Name)
		instances[i].Spec.Location = &deployment.Spec.LocationRef
		instances[i].Spec.NetworkInterfaces = deployment.Spec.Template.Spec.NetworkInterfaces
		instances[i].OwnerReferences = []metav1.OwnerReference{{UID: deployment.UID, Kind: "WorkloadDeployment"}}
		instances[i].Status.Controller = &v1alpha.InstanceControllerStatus{ObservedTemplateHash: instances[i].Spec.Controller.TemplateHash}
		instances[i].Status.NetworkInterfaces = []v1alpha.InstanceNetworkInterfaceStatus{{Name: "eth0", Addresses: []v1alpha.InstanceNetworkInterfaceAddress{{Address: "10.0.0.2/32"}}, ExternalAddresses: []v1alpha.InstanceNetworkInterfaceExternalAddress{{Address: "203.0.113.2"}}}}
	}
	original := instances[1].DeepCopy()
	deployment.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "updated-image"
	actions, err := control.GetActions(context.Background(), scheme, deployment, 2, instances)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	require.Equal(t, instancecontrol.ActionTypeUpdate, actions[0].ActionType())
	require.False(t, actions[0].IsSkipped())
	require.True(t, actions[1].IsSkipped())
	updated := actions[0].Object.(*v1alpha.Instance)
	require.Equal(t, original.UID, updated.UID)
	require.Equal(t, original.Spec.NetworkInterfaces, updated.Spec.NetworkInterfaces)
	require.Equal(t, original.Status.NetworkInterfaces, updated.Status.NetworkInterfaces)
	require.Equal(t, original.OwnerReferences, updated.OwnerReferences)
	require.EqualValues(t, 1, updated.Spec.Controller.RuntimeRevision)
	// A stale Ready condition and even an echoed hash cannot finish this update.
	updated.Status.Controller.ObservedTemplateHash = updated.Spec.Controller.TemplateHash
	instances[1] = *updated
	actions, err = control.GetActions(context.Background(), scheme, deployment, 2, instances)
	require.NoError(t, err)
	require.Equal(t, instancecontrol.ActionTypeWait, actions[0].ActionType())
	require.True(t, actions[1].IsSkipped())
	instances[1].Status.Controller.ObservedRuntimeRevision = 1
	actions, err = control.GetActions(context.Background(), scheme, deployment, 2, instances)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Equal(t, instances[0].Name, actions[0].Object.GetName())
	// Permanent removal still uses the deletion lifecycle.
	actions, err = control.GetActions(context.Background(), scheme, deployment, 0, instances)
	require.NoError(t, err)
	require.Equal(t, instancecontrol.ActionTypeDelete, actions[0].ActionType())
}

func TestRuntimeUpdateConflictsInsteadOfOverwritingConcurrentChanges(t *testing.T) {
	deployment := getWorkloadDeployment("conflict", 1)
	instance := getInstanceForDeployment(deployment, 0)
	instance.Spec.Location = &deployment.Spec.LocationRef
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	current := &v1alpha.Instance{}
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Namespace: instance.Namespace, Name: instance.Name}, current))
	updated, err := runtimeUpdate(current, deployment, "new-hash")
	require.NoError(t, err)
	current.Annotations = map[string]string{"concurrent": "change"}
	require.NoError(t, cl.Update(context.Background(), current))
	action := instancecontrol.NewUpdateAction(updated)
	require.Error(t, action.Execute(context.Background(), cl))
}

func TestRuntimeUpdateHashMigrationAndRapidChanges(t *testing.T) {
	deployment := getWorkloadDeployment("migration", 1)
	instance := getInstanceForDeployment(deployment, 0)
	instance.Spec.Location = &deployment.Spec.LocationRef
	hashes := instancecontrol.HashTemplate(deployment.Spec.Template)
	instance.Spec.Controller.TemplateHash = hashes.Legacy
	control := NewWithOptions(Options{PreserveInstances: true})
	actions, err := control.GetActions(context.Background(), scheme, deployment, 1, []v1alpha.Instance{*instance})
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Equal(t, instancecontrol.ActionTypePatchTemplateHash, actions[0].ActionType())
	require.Zero(t, actions[0].Object.(*v1alpha.Instance).Spec.Controller.RuntimeRevision)
	instance.Spec.Controller.TemplateHash = hashes.Current
	deployment.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "first"
	actions, err = control.GetActions(context.Background(), scheme, deployment, 1, []v1alpha.Instance{*instance})
	require.NoError(t, err)
	first := actions[0].Object.(*v1alpha.Instance)
	first.Status.Conditions = []metav1.Condition{{Type: v1alpha.InstanceReady, Status: metav1.ConditionFalse}}
	deployment.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "second"
	// A restart of the controller, or turning the gate off, must not return an
	// already opted-in Instance to the delete/recreate lifecycle.
	actions, err = New().GetActions(context.Background(), scheme, deployment, 1, []v1alpha.Instance{*first})
	require.NoError(t, err)
	require.Equal(t, instancecontrol.ActionTypeUpdate, actions[0].ActionType())
	require.EqualValues(t, 2, actions[0].Object.(*v1alpha.Instance).Spec.Controller.RuntimeRevision)
	deployment.Spec.Template.Spec.Runtime.Class = "different"
	_, err = control.GetActions(context.Background(), scheme, deployment, 1, []v1alpha.Instance{*first})
	require.ErrorContains(t, err, "not supported")
}
