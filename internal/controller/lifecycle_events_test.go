// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.miloapis.com/milo/pkg/downstreamclient"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

const (
	lifecycleTestNamespace   = "ns-1234"
	lifecycleTestProjectNS   = "default"
	lifecycleTestDeployment  = "web-default-us-central-1"
	lifecycleTestWorkload    = "web"
	lifecycleTestWorkloadUID = "project-workload-uid"
)

func newLifecycleTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, eventsv1.AddToScheme(scheme))
	require.NoError(t, computev1alpha.AddToScheme(scheme))
	return scheme
}

// newLifecycleTestCluster holds a cell namespace that stands for the project's
// default namespace, as Karmada propagates it.
func newLifecycleTestCluster(t *testing.T) *fakeCluster {
	t.Helper()
	cellNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   lifecycleTestNamespace,
		Labels: map[string]string{downstreamclient.UpstreamOwnerNamespaceLabel: lifecycleTestProjectNS},
	}}
	return newFakeCluster(fake.NewClientBuilder().WithScheme(newLifecycleTestScheme(t)).WithObjects(cellNamespace).Build())
}

func newTestLifecycleWriter() *lifecycleEventWriter {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	return &lifecycleEventWriter{
		reportingController: instanceLifecycleReportingController,
		reportingInstance:   "compute-manager-test",
		now: func() time.Time {
			now = now.Add(time.Millisecond)
			return now
		},
	}
}

func lifecycleTestInstance(index, readyReason, message string) *computev1alpha.Instance {
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lifecycleTestDeployment + "-" + index,
			Namespace: lifecycleTestNamespace,
			UID:       "instance-uid",
			Labels: map[string]string{
				computev1alpha.WorkloadNameLabel:  lifecycleTestWorkload,
				computev1alpha.WorkloadUIDLabel:   lifecycleTestWorkloadUID,
				computev1alpha.InstanceIndexLabel: index,
			},
		},
	}
	if readyReason != "" {
		instance.Status.Conditions = []metav1.Condition{{
			Type:    computev1alpha.InstanceReady,
			Status:  metav1.ConditionFalse,
			Reason:  readyReason,
			Message: message,
		}}
	}
	return instance
}

func lifecycleTestDeploymentObject() *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: lifecycleTestDeployment, Namespace: lifecycleTestNamespace},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			WorkloadRef: computev1alpha.WorkloadReference{Name: lifecycleTestWorkload, UID: lifecycleTestWorkloadUID},
		},
	}
}

func listLifecycleEvents(t *testing.T, c client.Client) []eventsv1.Event {
	t.Helper()
	var list eventsv1.EventList
	require.NoError(t, c.List(context.Background(), &list))
	return list.Items
}

func requireRelatedProjectWorkload(t *testing.T, e eventsv1.Event) {
	t.Helper()
	require.NotNil(t, e.Related)
	require.Equal(t, corev1.ObjectReference{
		APIVersion: computev1alpha.GroupVersion.String(),
		Kind:       "Workload",
		Namespace:  lifecycleTestProjectNS,
		Name:       lifecycleTestWorkload,
		UID:        lifecycleTestWorkloadUID,
	}, *e.Related)
}

func TestInstanceReadyTransitionRecordsLifecycleReasons(t *testing.T) {
	cl := newLifecycleTestCluster(t)
	instance := lifecycleTestInstance("0", computev1alpha.InstanceReadyReasonImageUnavailable, "pull access denied")

	newTestLifecycleWriter().recordInstanceReadyTransition(context.Background(), cl, instance,
		computev1alpha.InstanceReadyReasonProvisioning)

	events := listLifecycleEvents(t, cl.GetClient())
	require.Len(t, events, 1)
	e := events[0]
	require.Equal(t, "ImageUnavailable", e.Reason)
	require.Equal(t, corev1.EventTypeWarning, e.Type)
	require.Equal(t, "pull access denied", e.Note)
	require.Equal(t, lifecycleTestNamespace, e.Namespace)
	require.Equal(t, "Instance", e.Regarding.Kind)
	require.Equal(t, instance.Name, e.Regarding.Name)
	require.Equal(t, instanceLifecycleReportingController, e.ReportingController)
	requireRelatedProjectWorkload(t, e)
}

func TestRelatedWorkloadKeepsTheNamespaceOutsideACell(t *testing.T) {
	cl := newFakeCluster(fake.NewClientBuilder().WithScheme(newLifecycleTestScheme(t)).Build())

	newTestLifecycleWriter().recordInstanceReadyTransition(context.Background(), cl,
		lifecycleTestInstance("0", computev1alpha.InstanceReadyReasonAvailable, ""), "")

	events := listLifecycleEvents(t, cl.GetClient())
	require.Len(t, events, 1)
	require.Equal(t, lifecycleTestNamespace, events[0].Related.Namespace)
}

func TestInstanceReadyTransitionSkipsUnchangedAndProgressReasons(t *testing.T) {
	for name, tc := range map[string]struct{ prior, current string }{
		"unchanged":     {computev1alpha.InstanceReadyReasonAvailable, computev1alpha.InstanceReadyReasonAvailable},
		"provisioning":  {"", computev1alpha.InstanceReadyReasonProvisioning},
		"pending quota": {computev1alpha.InstanceReadyReasonProvisioning, computev1alpha.InstanceProgrammedReasonPendingQuota},
		"suspended":     {computev1alpha.InstanceReadyReasonAvailable, computev1alpha.InstanceReadyReasonSuspended},
		"no condition":  {computev1alpha.InstanceReadyReasonAvailable, ""},
	} {
		t.Run(name, func(t *testing.T) {
			cl := newLifecycleTestCluster(t)
			newTestLifecycleWriter().recordInstanceReadyTransition(context.Background(), cl,
				lifecycleTestInstance("0", tc.current, ""), tc.prior)
			require.Empty(t, listLifecycleEvents(t, cl.GetClient()))
		})
	}
}

func TestInstanceTerminatingNamesTheCause(t *testing.T) {
	for name, tc := range map[string]struct{ index, wantNote string }{
		"index beyond desired replicas": {"2", "Scaling down to 2 replicas"},
		"index within desired replicas": {"0", "Replacing it with the deployment's current template"},
	} {
		t.Run(name, func(t *testing.T) {
			cl := newLifecycleTestCluster(t)
			newTestLifecycleWriter().recordInstanceTerminating(context.Background(), cl,
				instancecontrol.NewDeleteAction(lifecycleTestInstance(tc.index, "", "")), 2)

			events := listLifecycleEvents(t, cl.GetClient())
			require.Len(t, events, 1)
			require.Equal(t, eventReasonInstanceTerminating, events[0].Reason)
			require.Equal(t, tc.wantNote, events[0].Note)
			requireRelatedProjectWorkload(t, events[0])
		})
	}
}

func TestInstanceTerminatingSkipsOtherActionsAndRepeatDeletes(t *testing.T) {
	deleting := lifecycleTestInstance("2", "", "")
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	for name, action := range map[string]instancecontrol.Action{
		"create":           instancecontrol.NewCreateAction(lifecycleTestInstance("2", "", "")),
		"already deleting": instancecontrol.NewDeleteAction(deleting),
	} {
		t.Run(name, func(t *testing.T) {
			cl := newLifecycleTestCluster(t)
			newTestLifecycleWriter().recordInstanceTerminating(context.Background(), cl, action, 1)
			require.Empty(t, listLifecycleEvents(t, cl.GetClient()))
		})
	}
}

func TestDeploymentScaledRecordsReplicaChange(t *testing.T) {
	deployment := lifecycleTestDeploymentObject()
	deployment.Status.DesiredReplicas = 3
	cl := newLifecycleTestCluster(t)

	newTestLifecycleWriter().recordDeploymentScaled(context.Background(), cl, deployment,
		computev1alpha.WorkloadDeploymentStatus{DesiredReplicas: 2, ObservedGeneration: 1})

	events := listLifecycleEvents(t, cl.GetClient())
	require.Len(t, events, 1)
	require.Equal(t, eventReasonDeploymentScaled, events[0].Reason)
	require.Equal(t, "Scaled from 2 to 3 replicas", events[0].Note)
	requireRelatedProjectWorkload(t, events[0])
}

func TestDeploymentScaledSkipsFirstReconcileAndSuspension(t *testing.T) {
	for name, tc := range map[string]struct {
		prior     computev1alpha.WorkloadDeploymentStatus
		suspended bool
	}{
		"first reconcile": {prior: computev1alpha.WorkloadDeploymentStatus{DesiredReplicas: 0}},
		"suspending":      {prior: computev1alpha.WorkloadDeploymentStatus{DesiredReplicas: 2, ObservedGeneration: 1}, suspended: true},
		"resuming":        {prior: computev1alpha.WorkloadDeploymentStatus{DesiredReplicas: 0, ObservedGeneration: 1, Suspended: true}},
		"unchanged":       {prior: computev1alpha.WorkloadDeploymentStatus{DesiredReplicas: 3, ObservedGeneration: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			deployment := lifecycleTestDeploymentObject()
			deployment.Status.DesiredReplicas = 3
			deployment.Status.Suspended = tc.suspended
			cl := newLifecycleTestCluster(t)

			newTestLifecycleWriter().recordDeploymentScaled(context.Background(), cl, deployment, tc.prior)

			require.Empty(t, listLifecycleEvents(t, cl.GetClient()))
		})
	}
}

func TestNilLifecycleWriterRecordsNothing(t *testing.T) {
	var w *lifecycleEventWriter
	cl := newLifecycleTestCluster(t)

	w.recordInstanceReadyTransition(context.Background(), cl,
		lifecycleTestInstance("0", computev1alpha.InstanceReadyReasonImageUnavailable, ""), "")

	require.Empty(t, listLifecycleEvents(t, cl.GetClient()))
}

func TestLongEventNamesAreTrimmedToTheAPILimit(t *testing.T) {
	cl := newLifecycleTestCluster(t)
	instance := lifecycleTestInstance("0", computev1alpha.InstanceReadyReasonAvailable, "")
	instance.Name = strings.Repeat("w", 250) + "-0"

	newTestLifecycleWriter().recordInstanceReadyTransition(context.Background(), cl, instance, "")

	events := listLifecycleEvents(t, cl.GetClient())
	require.Len(t, events, 1)
	require.Len(t, events[0].Name, maxEventNameLen)
}
