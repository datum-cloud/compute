// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"
	"time"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

const (
	testSessionName     = "web-0-abcde"
	testSessionUID      = types.UID("session-uid-1")
	testSessionInstance = "web-0"
	testInstanceUID     = types.UID("project-instance-uid")
	testCellWDUID       = "cell-wd-uid"
	testRuntimeClass    = "general-purpose"
)

var testSessionCreated = time.Date(2026, time.September, 29, 18, 0, 0, 0, time.UTC)

type sessionTestEnv struct {
	project    client.Client
	hub        client.Client
	reconciler *InstanceConsoleSessionReconciler
	now        time.Time

	// beforeHubStatusUpdate, when set, runs once just before the next status
	// update reaches the hub, so a test can land a competing write first.
	beforeHubStatusUpdate func()
}

func newSessionTestEnv(t *testing.T, projectObjs []client.Object, hubObjs []client.Object) *sessionTestEnv {
	t.Helper()

	projectScheme := newProjectScheme()
	require.NoError(t, eventsv1.AddToScheme(projectScheme))
	project := fake.NewClientBuilder().
		WithScheme(projectScheme).
		WithObjects(projectObjs...).
		WithStatusSubresource(&computev1alpha.InstanceConsoleSession{}).
		Build()

	env := &sessionTestEnv{
		project: project,
		now:     testSessionCreated,
	}
	env.hub = fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(hubObjs...).
		WithStatusSubresource(&computev1alpha.InstanceConsoleSession{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if hook := env.beforeHubStatusUpdate; hook != nil {
					env.beforeHubStatusUpdate = nil
					hook()
				}
				return c.SubResource(subResource).Update(ctx, obj, opts...)
			},
		}).
		Build()

	env.reconciler = &InstanceConsoleSessionReconciler{
		mgr:               newFakeMCManager(testCluster, newFakeCluster(project)),
		FederationClient:  env.hub,
		ReportingInstance: "compute-manager-0",
		Now:               func() time.Time { return env.now },
	}
	return env
}

func (e *sessionTestEnv) reconcile(t *testing.T) ctrl.Result {
	t.Helper()
	result, err := e.reconciler.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: testCluster,
		Request: ctrl.Request{NamespacedName: types.NamespacedName{
			Namespace: testProjNS,
			Name:      testSessionName,
		}},
	})
	require.NoError(t, err)
	return result
}

func (e *sessionTestEnv) projectSession(t *testing.T) (*computev1alpha.InstanceConsoleSession, bool) {
	t.Helper()
	var session computev1alpha.InstanceConsoleSession
	err := e.project.Get(context.Background(), types.NamespacedName{Namespace: testProjNS, Name: testSessionName}, &session)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)
	return &session, true
}

func (e *sessionTestEnv) hubSession(t *testing.T) (*computev1alpha.InstanceConsoleSession, bool) {
	t.Helper()
	var session computev1alpha.InstanceConsoleSession
	err := e.hub.Get(context.Background(), types.NamespacedName{Namespace: testKarmadaNSStr, Name: testSessionName}, &session)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)
	return &session, true
}

func (e *sessionTestEnv) events(t *testing.T) map[string]eventsv1.Event {
	t.Helper()
	var list eventsv1.EventList
	require.NoError(t, e.project.List(context.Background(), &list, client.InNamespace(testProjNS)))
	byReason := map[string]eventsv1.Event{}
	for _, event := range list.Items {
		_, dup := byReason[event.Reason]
		require.False(t, dup, "more than one %s event", event.Reason)
		byReason[event.Reason] = event
	}
	return byReason
}

// setHubStatus writes status onto the hub copy the way the shell agent does.
func (e *sessionTestEnv) setHubStatus(t *testing.T, mutate func(*computev1alpha.InstanceConsoleSessionStatus)) {
	t.Helper()
	hubCopy, ok := e.hubSession(t)
	require.True(t, ok)
	mutate(&hubCopy.Status)
	require.NoError(t, e.hub.Status().Update(context.Background(), hubCopy))
}

// addAgentFinalizer claims the hub copy the way the shell agent does.
func (e *sessionTestEnv) addAgentFinalizer(t *testing.T) {
	t.Helper()
	hubCopy, ok := e.hubSession(t)
	require.True(t, ok)
	controllerutil.AddFinalizer(hubCopy, computev1alpha.InstanceConsoleSessionAgentFinalizer)
	require.NoError(t, e.hub.Update(context.Background(), hubCopy))
}

// removeAgentFinalizer confirms cleanup the way the shell agent does.
func (e *sessionTestEnv) removeAgentFinalizer(t *testing.T) {
	t.Helper()
	hubCopy, ok := e.hubSession(t)
	require.True(t, ok)
	controllerutil.RemoveFinalizer(hubCopy, computev1alpha.InstanceConsoleSessionAgentFinalizer)
	require.NoError(t, e.hub.Update(context.Background(), hubCopy))
}

func testSession() *computev1alpha.InstanceConsoleSession {
	return &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testSessionName,
			Namespace:         testProjNS,
			UID:               testSessionUID,
			CreationTimestamp: metav1.NewTime(testSessionCreated),
			Annotations: map[string]string{
				computev1alpha.InstanceConsoleSessionRequesterAnnotation: "alice@example.com",
			},
		},
		Spec: computev1alpha.InstanceConsoleSessionSpec{
			InstanceRef:     computev1alpha.InstanceConsoleSessionInstanceRef{Name: testSessionInstance, UID: testInstanceUID},
			ContainerName:   testContainerName,
			Command:         []string{"sh"},
			Terminal:        true,
			Stdin:           true,
			ClientPublicKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		Status: computev1alpha.InstanceConsoleSessionStatus{
			Conditions: []metav1.Condition{pendingCondition()},
		},
	}
}

func pendingCondition() metav1.Condition {
	return metav1.Condition{
		Type:               computev1alpha.InstanceConsoleSessionReady,
		Status:             metav1.ConditionUnknown,
		Reason:             computev1alpha.InstanceConsoleSessionReasonPending,
		Message:            "Waiting for the session to be prepared",
		LastTransitionTime: metav1.NewTime(time.Unix(0, 0).UTC()),
	}
}

func testSessionInstanceObj() *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSessionInstance,
			Namespace: testProjNS,
			UID:       testInstanceUID,
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentNameLabel: testWDName,
				computev1alpha.WorkloadDeploymentUIDLabel:  testCellWDUID,
			},
		},
	}
}

func testFederatedProjectWD() *computev1alpha.WorkloadDeployment {
	return testWorkloadDeployment(func(wd *computev1alpha.WorkloadDeployment) {
		wd.Annotations = map[string]string{computev1alpha.FederationNamespaceAnnotation: testKarmadaNSStr}
	})
}

func testHubWD(runtimeClass string) *computev1alpha.WorkloadDeployment {
	labels := map[string]string{
		locationLabel: testFederatorLocation,
		downstreamclient.UpstreamOwnerNamespaceLabel: testProjNS,
	}
	if runtimeClass != "" {
		labels[computev1alpha.RuntimeClassLabel] = runtimeClass
	}
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWDName,
			Namespace: testKarmadaNSStr,
			UID:       "hub-wd-uid",
			Labels:    labels,
		},
	}
}

func newDeliverableSessionEnv(t *testing.T, runtimeClass string) *sessionTestEnv {
	t.Helper()
	return newSessionTestEnv(t,
		[]client.Object{testSession(), testSessionInstanceObj(), testFederatedProjectWD()},
		[]client.Object{testHubWD(runtimeClass)},
	)
}

func TestInstanceConsoleSessionDelivery(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Contains(t, session.Finalizers, computev1alpha.InstanceConsoleSessionFinalizer)
	assert.Equal(t, testKarmadaNSStr, session.Annotations[computev1alpha.FederationNamespaceAnnotation])

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok, "session should be copied into the deployment's hub namespace")
	assert.Equal(t, map[string]string{
		locationLabel:                                          testFederatorLocation,
		computev1alpha.RuntimeClassLabel:                       testRuntimeClass,
		computev1alpha.InstanceConsoleSessionUIDLabel:          string(testSessionUID),
		computev1alpha.InstanceConsoleSessionInstanceNameLabel: testSessionInstance,
		computev1alpha.WorkloadDeploymentUIDLabel:              testCellWDUID,
		downstreamclient.UpstreamOwnerClusterNameLabel:         EncodeClusterName(testCluster),
		downstreamclient.UpstreamOwnerNamespaceLabel:           testProjNS,
	}, hubCopy.Labels)
	assert.Equal(t, session.Spec, hubCopy.Spec)

	owner := metav1.GetControllerOf(hubCopy)
	require.NotNil(t, owner, "hub copy should be owned by the hub deployment")
	assert.Equal(t, kindWorkloadDeployment, owner.Kind)
	assert.Equal(t, types.UID("hub-wd-uid"), owner.UID)

	assert.Empty(t, env.events(t), "a pending session records no events")
}

func TestInstanceConsoleSessionDeliveryWithoutRuntimeClass(t *testing.T) {
	env := newDeliverableSessionEnv(t, "")
	env.reconcile(t)

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	assert.NotContains(t, hubCopy.Labels, computev1alpha.RuntimeClassLabel,
		"the copy carries only the labels the hub deployment has, so the same policy selects both")
	assert.Equal(t, testFederatorLocation, hubCopy.Labels[locationLabel])
}

func TestInstanceConsoleSessionReplacedInstanceEndsSession(t *testing.T) {
	instance := testSessionInstanceObj()
	instance.UID = "replacement-uid"
	env := newSessionTestEnv(t,
		[]client.Object{testSession(), instance, testFederatedProjectWD()},
		[]client.Object{testHubWD(testRuntimeClass)},
	)
	env.reconcile(t)

	_, delivered := env.hubSession(t)
	assert.False(t, delivered, "a session for a replaced instance must not reach the cell")

	events := env.events(t)
	ended, ok := events[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonInstanceNotFound, ended.Annotations[sessionEventReasonAnnotation])
	assert.NotContains(t, events, EventReasonSessionStarted)

	session, ok := env.projectSession(t)
	require.True(t, ok, "the finalizer holds the session until it is finalized")
	assert.False(t, session.DeletionTimestamp.IsZero(), "an ended session is deleted at once")

	env.reconcile(t)
	_, ok = env.projectSession(t)
	assert.False(t, ok, "a session that never reached the cell needs no cleanup confirmation")
}

func TestInstanceConsoleSessionStatusCopy(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)

	startedAt := metav1.NewTime(env.now.Truncate(time.Second))
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		status.Connection = &computev1alpha.InstanceConsoleSessionConnection{
			EndpointID: "abcdef",
			RelayURLs:  []string{"https://relay.example.com"},
			Target:     "exec-agent-0.exec-agent.compute-shell-system.svc.cluster.local:7777",
		}
		status.StartedAt = &startedAt
		status.ExpiresAt = ptr.To(metav1.NewTime(startedAt.Add(15 * time.Minute)))
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceConsoleSessionReady,
			Status:             metav1.ConditionTrue,
			Reason:             computev1alpha.InstanceConsoleSessionReasonConnected,
			Message:            "Connected",
			LastTransitionTime: startedAt,
		})
	})
	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	hubCopy, _ := env.hubSession(t)
	assert.Equal(t, hubCopy.Status, session.Status)
	assert.True(t, session.DeletionTimestamp.IsZero())

	events := env.events(t)
	started, ok := events[EventReasonSessionStarted]
	require.True(t, ok)
	assert.NotContains(t, events, EventReasonSessionEnded)

	assert.Equal(t, instanceShellReportingController, started.ReportingController)
	assert.Equal(t, "compute-manager-0", started.ReportingInstance)
	assert.Equal(t, EventReasonSessionStarted, started.Action)
	assert.Equal(t, corev1.EventTypeNormal, started.Type)
	assert.Equal(t, corev1.ObjectReference{
		APIVersion: computev1alpha.GroupVersion.String(),
		Kind:       "InstanceConsoleSession",
		Namespace:  testProjNS,
		Name:       testSessionName,
		UID:        testSessionUID,
	}, started.Regarding)
	require.NotNil(t, started.Related)
	assert.Equal(t, "Instance", started.Related.Kind)
	assert.Equal(t, testSessionInstance, started.Related.Name)
	assert.Equal(t, testInstanceUID, started.Related.UID)
	assert.Equal(t, map[string]string{
		computev1alpha.InstanceConsoleSessionRequesterAnnotation: "alice@example.com",
		sessionEventInstanceAnnotation:                           testSessionInstance,
		sessionEventContainerAnnotation:                          testContainerName,
	}, started.Annotations)

	env.reconcile(t)
	assert.Len(t, env.events(t), 1, "events are recorded once however often the session reconciles")
}

// claimOnHub claims the hub copy the way the shell agent does.
func claimOnHub(env *sessionTestEnv, hubCopy *computev1alpha.InstanceConsoleSession) error {
	connectBefore := metav1.NewTime(env.now.Add(time.Minute).Truncate(time.Second))
	hubCopy.Status.Connection = &computev1alpha.InstanceConsoleSessionConnection{
		EndpointID: "abcdef",
		RelayURLs:  []string{"https://relay.example.com"},
		Target:     "exec-agent-0.exec-agent.compute-shell-system.svc.cluster.local:7777",
	}
	hubCopy.Status.ConnectBefore = &connectBefore
	apimeta.SetStatusCondition(&hubCopy.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.InstanceConsoleSessionReady,
		Status:             metav1.ConditionTrue,
		Reason:             computev1alpha.InstanceConsoleSessionReasonSessionReady,
		Message:            "Ready for a client",
		LastTransitionTime: connectBefore,
	})
	return env.hub.Status().Update(context.Background(), hubCopy)
}

func (e *sessionTestEnv) reconcileErr() error {
	_, err := e.reconciler.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: testCluster,
		Request:     ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testProjNS, Name: testSessionName}},
	})
	return err
}

func TestInstanceConsoleSessionUnclaimedEndsUnavailable(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)

	result := env.reconcile(t)
	assert.Equal(t, DefaultSessionClaimTimeout, result.RequeueAfter, "delivery waits for the claim deadline")

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout - time.Second)
	result = env.reconcile(t)
	assert.Equal(t, time.Second, result.RequeueAfter)
	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	assert.False(t, sessionEnded(hubCopy), "a copy is not ended before its deadline")

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)

	hubCopy, ok = env.hubSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(hubCopy),
		"the decision is made on the hub copy, where the agent claims")
	require.NotNil(t, hubCopy.Status.EndedAt)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(session))
	assert.False(t, session.DeletionTimestamp.IsZero(), "an ended session is deleted at once")

	events := env.events(t)
	ended, ok := events[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, ended.Annotations[sessionEventReasonAnnotation])
	assert.NotContains(t, events, EventReasonSessionStarted)

	env.reconcile(t)
	_, ok = env.hubSession(t)
	assert.False(t, ok, "an unclaimed copy has no agent finalizer holding it")
	_, ok = env.projectSession(t)
	assert.False(t, ok)
}

func TestInstanceConsoleSessionClaimedIsNotUnavailable(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	require.NoError(t, claimOnHub(env, hubCopy))

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout + time.Minute)
	result := env.reconcile(t)
	assert.Zero(t, result.RequeueAfter, "a claimed session is the agent's to end")

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonSessionReady, sessionReadyReason(session))
	assert.True(t, session.DeletionTimestamp.IsZero())
}

func TestInstanceConsoleSessionClaimWinsRaceWithUnavailable(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.beforeHubStatusUpdate = func() {
		hubCopy, ok := env.hubSession(t)
		require.True(t, ok)
		require.NoError(t, claimOnHub(env, hubCopy))
	}
	err := env.reconcileErr()
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), "the claim landed first, so ending the copy conflicts: %v", err)

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonSessionReady, sessionReadyReason(hubCopy))

	env.reconcile(t)
	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonSessionReady, sessionReadyReason(session),
		"the retry sees the claim and mirrors it")
	assert.True(t, session.DeletionTimestamp.IsZero())
	assert.NotContains(t, env.events(t), EventReasonSessionEnded)
}

func TestInstanceConsoleSessionUnavailableWinsRaceWithClaim(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)

	agentView, ok := env.hubSession(t)
	require.True(t, ok)

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)

	err := claimOnHub(env, agentView)
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), "a claim read before the copy ended must not overwrite it: %v", err)

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(hubCopy))
	assert.Nil(t, hubCopy.Status.Connection)
}

func TestInstanceConsoleSessionUndeliverableEndsUnavailable(t *testing.T) {
	unfederated := testWorkloadDeployment()
	env := newSessionTestEnv(t,
		[]client.Object{testSession(), testSessionInstanceObj(), unfederated},
		[]client.Object{testHubWD(testRuntimeClass)},
	)

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout - time.Second)
	require.Error(t, env.reconcileErr(), "delivery retries until the deadline")
	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonPending, sessionReadyReason(session))

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)

	_, delivered := env.hubSession(t)
	assert.False(t, delivered)
	session, ok = env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(session))
	assert.False(t, session.DeletionTimestamp.IsZero())

	ended, ok := env.events(t)[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, ended.Annotations[sessionEventReasonAnnotation])

	env.reconcile(t)
	_, ok = env.projectSession(t)
	assert.False(t, ok)
}

// endSessionOnHub moves the hub copy through a connection to a normal exit.
func endSessionOnHub(t *testing.T, env *sessionTestEnv, exitCode int32) {
	t.Helper()
	startedAt := metav1.NewTime(env.now.Truncate(time.Second))
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		status.StartedAt = &startedAt
		status.EndedAt = &startedAt
		status.ExitCode = ptr.To(exitCode)
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceConsoleSessionReady,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceConsoleSessionReasonCompleted,
			Message:            "The command exited",
			LastTransitionTime: startedAt,
		})
	})
}

func TestInstanceConsoleSessionImmediateCleanupWithConfirmation(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	env.addAgentFinalizer(t)
	endSessionOnHub(t, env, 3)

	env.reconcile(t)

	events := env.events(t)
	require.Contains(t, events, EventReasonSessionStarted)
	ended, ok := events[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonCompleted, ended.Annotations[sessionEventReasonAnnotation])
	assert.Equal(t, "3", ended.Annotations[sessionEventExitCodeAnnotation])
	assert.Equal(t, "alice@example.com", ended.Annotations[computev1alpha.InstanceConsoleSessionRequesterAnnotation])

	session, ok := env.projectSession(t)
	require.True(t, ok)
	require.False(t, session.DeletionTimestamp.IsZero(), "an ended session is deleted once its end is on record")
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonCompleted, sessionReadyReason(session))

	env.now = session.DeletionTimestamp.Time
	result := env.reconcile(t)
	hubCopy, ok := env.hubSession(t)
	require.True(t, ok, "the agent's finalizer holds the hub copy")
	assert.False(t, hubCopy.DeletionTimestamp.IsZero(), "finalization deletes the hub copy")
	assert.Positive(t, result.RequeueAfter, "finalization waits for the cell")
	assert.LessOrEqual(t, result.RequeueAfter, defaultSessionCleanupTimeout)
	_, ok = env.projectSession(t)
	assert.True(t, ok, "the session waits for the cell to confirm cleanup")

	env.removeAgentFinalizer(t)
	_, ok = env.hubSession(t)
	require.False(t, ok)

	env.reconcile(t)
	_, ok = env.projectSession(t)
	assert.False(t, ok, "a confirmed cleanup releases the session")
	assert.NotContains(t, env.events(t), EventReasonCleanupUnconfirmed)
}

func TestInstanceConsoleSessionCleanupTimeout(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	env.addAgentFinalizer(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	require.NoError(t, env.project.Delete(context.Background(), session))
	session, _ = env.projectSession(t)

	env.now = session.DeletionTimestamp.Add(defaultSessionCleanupTimeout - time.Second)
	result := env.reconcile(t)
	assert.Equal(t, time.Second, result.RequeueAfter)
	assert.NotContains(t, env.events(t), EventReasonCleanupUnconfirmed)

	env.now = session.DeletionTimestamp.Add(defaultSessionCleanupTimeout)
	env.reconcile(t)

	events := env.events(t)
	unconfirmed, ok := events[EventReasonCleanupUnconfirmed]
	require.True(t, ok)
	assert.Equal(t, corev1.EventTypeWarning, unconfirmed.Type)
	assert.Equal(t, instanceShellReportingController, unconfirmed.ReportingController)

	ended, ok := events[EventReasonSessionEnded]
	require.True(t, ok, "a session revoked before it ended still records its end")
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonRevoked, ended.Annotations[sessionEventReasonAnnotation])
	assert.NotContains(t, ended.Annotations, sessionEventExitCodeAnnotation)

	_, ok = env.projectSession(t)
	assert.False(t, ok, "the finalizer is removed after the timeout")
	_, ok = env.hubSession(t)
	assert.False(t, ok, "an unconfirmed hub copy is released so it does not outlive the session")
}

func TestInstanceConsoleSessionIgnoresAnotherSessionsHubCopy(t *testing.T) {
	stale := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testSessionName,
			Namespace:  testKarmadaNSStr,
			Labels:     map[string]string{computev1alpha.InstanceConsoleSessionUIDLabel: "earlier-session"},
			Finalizers: []string{computev1alpha.InstanceConsoleSessionAgentFinalizer},
		},
	}
	session := testSession()
	session.Annotations[computev1alpha.FederationNamespaceAnnotation] = testKarmadaNSStr
	session.Finalizers = []string{computev1alpha.InstanceConsoleSessionFinalizer}

	env := newSessionTestEnv(t,
		[]client.Object{session, testSessionInstanceObj(), testFederatedProjectWD()},
		[]client.Object{testHubWD(testRuntimeClass), stale},
	)
	_, err := env.reconciler.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: testCluster,
		Request:     ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testProjNS, Name: testSessionName}},
	})
	require.Error(t, err, "delivery waits for the earlier copy to go")

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	assert.Equal(t, "earlier-session", hubCopy.Labels[computev1alpha.InstanceConsoleSessionUIDLabel])
	assert.True(t, hubCopy.Status.Conditions == nil, "the earlier copy's status is not this session's")
}

func TestMapHubSessionToRequest(t *testing.T) {
	env := newSessionTestEnv(t, nil, nil)

	hubCopy := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSessionName,
			Namespace: testKarmadaNSStr,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: EncodeClusterName("org/" + testCluster),
				downstreamclient.UpstreamOwnerNamespaceLabel:   testProjNS,
			},
		},
	}
	assert.Equal(t, []mcreconcile.Request{{
		ClusterName: testCluster,
		Request:     ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testProjNS, Name: testSessionName}},
	}}, env.reconciler.mapHubCopyToRequest(context.Background(), hubCopy))

	unlabelled := hubCopy.DeepCopy()
	unlabelled.Labels = nil
	assert.Nil(t, env.reconciler.mapHubCopyToRequest(context.Background(), unlabelled))

	unengaged := hubCopy.DeepCopy()
	unengaged.Labels[downstreamclient.UpstreamOwnerClusterNameLabel] = EncodeClusterName("other-project")
	assert.Nil(t, env.reconciler.mapHubCopyToRequest(context.Background(), unengaged))
}

// TestPropagationPolicySelectsConsoleSessions verifies that a session's hub copy,
// labelled from its hub deployment, is selected by the same policy that places
// the deployment, and that no session selector exists while the gate is off.
func TestPropagationPolicySelectsConsoleSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		sessionsEnabled bool
		specClass       string
		wantPolicyName  string
		wantLabels      map[string]string
	}{
		{
			name:           "gate off — no session selector",
			wantPolicyName: testLocationPolicy,
		},
		{
			name:            "gate on, no class — selects by location",
			sessionsEnabled: true,
			wantPolicyName:  testLocationPolicy,
			wantLabels:      map[string]string{locationLabel: testFederatorLocation},
		},
		{
			name:            "gate on, class selected — selects by location and class",
			sessionsEnabled: true,
			specClass:       testClassBasalt,
			wantPolicyName:  propagationPolicyNameFor(testFederatorLocation, testClassBasalt),
			wantLabels: map[string]string{
				locationLabel:                    testFederatorLocation,
				computev1alpha.RuntimeClassLabel: testClassBasalt,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wd := testWorkloadDeployment(withFinalizer, withRuntimeClass(tt.specClass))
			karmadaClient := newKarmadaFakeClient(testCell("lax-cell", testFederatorLocation, testClassBasalt))
			r := newTestFederator(newProjectFakeClient(testProjectNamespace(), wd), karmadaClient)
			r.RuntimeClassesEnabled = true
			r.ConsoleSessionsEnabled = tt.sessionsEnabled

			ctx := context.Background()
			_, err := r.Reconcile(ctx, reconcileRequest())
			require.NoError(t, err)

			var pp karmadapolicyv1alpha1.PropagationPolicy
			require.NoError(t, karmadaClient.Get(ctx, types.NamespacedName{
				Name:      tt.wantPolicyName,
				Namespace: testKarmadaNSStr,
			}, &pp))

			var sessionSelectors []karmadapolicyv1alpha1.ResourceSelector
			for _, sel := range pp.Spec.ResourceSelectors {
				if sel.Kind == kindInstanceConsoleSession {
					sessionSelectors = append(sessionSelectors, sel)
				}
			}
			if !tt.sessionsEnabled {
				assert.Empty(t, sessionSelectors)
				return
			}
			require.Len(t, sessionSelectors, 1)
			assert.Equal(t, computev1alpha.GroupVersion.String(), sessionSelectors[0].APIVersion)
			require.NotNil(t, sessionSelectors[0].LabelSelector)
			assert.Equal(t, tt.wantLabels, sessionSelectors[0].LabelSelector.MatchLabels)
			assert.Equal(t, pp.Spec.ResourceSelectors[0].LabelSelector.MatchLabels, sessionSelectors[0].LabelSelector.MatchLabels,
				"sessions and deployments are selected by the same labels")

			var hubWD computev1alpha.WorkloadDeployment
			require.NoError(t, karmadaClient.Get(ctx, types.NamespacedName{Name: testWDName, Namespace: testKarmadaNSStr}, &hubWD))
			for k, v := range tt.wantLabels {
				assert.Equal(t, v, hubWD.Labels[k], "a copy labelled from the hub deployment matches the selector")
			}
		})
	}
}
