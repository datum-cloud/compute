// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	karmadaworkv1alpha2 "github.com/karmada-io/api/work/v1alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	testMemberCluster   = "cell-dfw-1"
	testRelayURL        = "https://relay.example.com"
)

var testSessionCreated = time.Date(2026, time.September, 29, 18, 0, 0, 0, time.UTC)

type sessionTestEnv struct {
	project    client.Client
	hub        client.Client
	reconciler *InstanceConsoleSessionReconciler
	now        time.Time

	// aggregating is set while a test writes hub status the way Karmada's
	// status aggregation does. Any other hub status write fails the test's
	// reconcile, because the control plane must never write hub status.
	aggregating bool
}

func newSessionTestEnv(t *testing.T, projectObjs []client.Object, hubObjs []client.Object) *sessionTestEnv {
	t.Helper()

	projectScheme := newProjectScheme()
	require.NoError(t, eventsv1.AddToScheme(projectScheme))
	project := fake.NewClientBuilder().
		WithScheme(projectScheme).
		WithObjects(projectObjs...).
		WithStatusSubresource(&computev1alpha.InstanceConsoleSession{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: createLikeMiloEvents}).
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
				if !env.aggregating {
					return errors.New("the control plane wrote hub status, which aggregation owns")
				}
				return c.SubResource(subResource).Update(ctx, obj, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, subResource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if !env.aggregating {
					return errors.New("the control plane wrote hub status, which aggregation owns")
				}
				return c.SubResource(subResource).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	for _, obj := range hubObjs {
		if wd, ok := obj.(*computev1alpha.WorkloadDeployment); ok {
			env.scheduleHubDeployment(t, wd, testMemberCluster)
		}
	}

	env.reconciler = &InstanceConsoleSessionReconciler{
		mgr:               newFakeMCManager(testCluster, newFakeCluster(project)),
		FederationClient:  env.hub,
		ReportingInstance: "compute-manager-0",
		Now:               func() time.Time { return env.now },
	}
	return env
}

// createLikeMiloEvents stores events the way Milo's project events store does,
// which accepts a second event with a name already taken, so a test sees every
// event the controller records rather than an AlreadyExists that hides it.
func createLikeMiloEvents(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
	err := c.Create(ctx, obj, opts...)
	event, ok := obj.(*eventsv1.Event)
	if !ok || !apierrors.IsAlreadyExists(err) {
		return err
	}
	var list eventsv1.EventList
	if err := c.List(ctx, &list, client.InNamespace(event.Namespace)); err != nil {
		return err
	}
	event.Name = fmt.Sprintf("%s-%d", event.Name, len(list.Items))
	event.ResourceVersion = ""
	return c.Create(ctx, event, opts...)
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

// setHubStatus writes status onto the hub copy the way Karmada's status
// aggregation does after the shell agent in the member cluster writes the cell
// copy, and records that the member reports it.
func (e *sessionTestEnv) setHubStatus(t *testing.T, mutate func(*computev1alpha.InstanceConsoleSessionStatus)) {
	t.Helper()
	hubCopy, ok := e.hubSession(t)
	require.True(t, ok)
	mutate(&hubCopy.Status)
	e.aggregating = true
	defer func() { e.aggregating = false }()
	require.NoError(t, e.hub.Status().Update(context.Background(), hubCopy))
	e.setMemberReports(t, true)
}

// scheduleHubDeployment records where Karmada's scheduler placed a hub
// WorkloadDeployment.
func (e *sessionTestEnv) scheduleHubDeployment(t *testing.T, wd *computev1alpha.WorkloadDeployment, members ...string) {
	t.Helper()
	ctx := context.Background()
	binding := &karmadaworkv1alpha2.ResourceBinding{ObjectMeta: metav1.ObjectMeta{
		Namespace: wd.Namespace,
		Name:      bindingName(kindWorkloadDeployment, wd.Name),
	}}
	_ = e.hub.Delete(ctx, binding)
	for _, m := range members {
		binding.Spec.Clusters = append(binding.Spec.Clusters, karmadaworkv1alpha2.TargetCluster{Name: m})
	}
	require.NoError(t, e.hub.Create(ctx, binding))
}

// setMemberReports records whether the member cluster reports the session's
// status in the session's ResourceBinding, as Karmada does while the cell's
// Work exists.
func (e *sessionTestEnv) setMemberReports(t *testing.T, reports bool) {
	t.Helper()
	ctx := context.Background()
	binding := &karmadaworkv1alpha2.ResourceBinding{ObjectMeta: metav1.ObjectMeta{
		Namespace: testKarmadaNSStr,
		Name:      bindingName(kindInstanceConsoleSession, testSessionName),
	}}
	_ = e.hub.Delete(ctx, binding)
	if reports {
		binding.Status.AggregatedStatus = []karmadaworkv1alpha2.AggregatedStatusItem{{
			ClusterName: testMemberCluster,
			Status:      &runtime.RawExtension{Raw: []byte(`{}`)},
			Applied:     true,
		}}
	}
	require.NoError(t, e.hub.Create(ctx, binding))
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
		sessionMemberClusterLabel:                              testMemberCluster,
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

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonPending, sessionReadyReason(session),
		"a fresh copy's empty status does not replace the session's")

	startedAt := metav1.NewTime(env.now.Truncate(time.Second))
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		status.Connection = &computev1alpha.InstanceConsoleSessionConnection{
			EndpointID: "abcdef",
			RelayURLs:  []string{testRelayURL},
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

	session, ok = env.projectSession(t)
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

// claimOnHub writes a claim onto the hub copy the way aggregation does once
// the shell agent claims the cell copy.
func claimOnHub(t *testing.T, env *sessionTestEnv) {
	t.Helper()
	connectBefore := metav1.NewTime(env.now.Add(time.Minute).Truncate(time.Second))
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		status.Connection = &computev1alpha.InstanceConsoleSessionConnection{
			EndpointID: "abcdef",
			RelayURLs:  []string{testRelayURL},
			Target:     "exec-agent-0.exec-agent.compute-shell-system.svc.cluster.local:7777",
		}
		status.ConnectBefore = &connectBefore
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceConsoleSessionReady,
			Status:             metav1.ConditionTrue,
			Reason:             computev1alpha.InstanceConsoleSessionReasonSessionReady,
			Message:            "Ready for a client",
			LastTransitionTime: connectBefore,
		})
	})
}

// startOnHub writes the connected status onto the hub copy the way
// aggregation does once a client connects.
func startOnHub(t *testing.T, env *sessionTestEnv) {
	t.Helper()
	startedAt := metav1.NewTime(env.now.Truncate(time.Second))
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		status.StartedAt = &startedAt
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceConsoleSessionReady,
			Status:             metav1.ConditionTrue,
			Reason:             computev1alpha.InstanceConsoleSessionReasonConnected,
			Message:            "Connected",
			LastTransitionTime: startedAt,
		})
	})
}

// endOnHub writes a terminal status onto the hub copy the way aggregation
// does once the shell agent has stopped the session's processes.
func endOnHub(t *testing.T, env *sessionTestEnv, reason string, exitCode *int32) {
	t.Helper()
	endedAt := metav1.NewTime(env.now.Truncate(time.Second))
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		status.EndedAt = &endedAt
		status.ExitCode = exitCode
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceConsoleSessionReady,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            "The session ended",
			LastTransitionTime: endedAt,
		})
	})
}

// endOnHubUnconfirmed writes the end the agent records before it has
// confirmed the session's processes stopped: the reason without endedAt.
func endOnHubUnconfirmed(t *testing.T, env *sessionTestEnv, reason string) {
	t.Helper()
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		status.EndedAt = nil
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceConsoleSessionReady,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            "The session ended",
			LastTransitionTime: metav1.NewTime(env.now.Truncate(time.Second)),
		})
	})
}

func (e *sessionTestEnv) reconcileErr() error {
	_, err := e.reconciler.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: testCluster,
		Request:     ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testProjNS, Name: testSessionName}},
	})
	return err
}

func (e *sessionTestEnv) deleteProjectSession(t *testing.T) *computev1alpha.InstanceConsoleSession {
	t.Helper()
	session, ok := e.projectSession(t)
	require.True(t, ok)
	require.NoError(t, e.project.Delete(context.Background(), session))
	session, ok = e.projectSession(t)
	require.True(t, ok, "the controller's finalizer holds the session")
	return session
}

func revokeRequested(hubCopy *computev1alpha.InstanceConsoleSession) bool {
	_, ok := hubCopy.Annotations[computev1alpha.InstanceConsoleSessionRevokeAnnotation]
	return ok
}

func TestInstanceConsoleSessionUnclaimedEndsUnavailable(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)

	result := env.reconcile(t)
	assert.Equal(t, DefaultSessionClaimTimeout, result.RequeueAfter, "delivery waits for the claim deadline")

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout - time.Second)
	result = env.reconcile(t)
	assert.Equal(t, time.Second, result.RequeueAfter)
	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.False(t, sessionEnded(session), "a session is not ended before its deadline")

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)

	session, ok = env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(session),
		"the decision is made on the project session")
	require.NotNil(t, session.Status.EndedAt)
	assert.False(t, session.DeletionTimestamp.IsZero(), "an ended session is deleted at once")

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	assert.Empty(t, hubCopy.Status.Conditions, "the control plane never writes hub status")

	events := env.events(t)
	ended, ok := events[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, ended.Annotations[sessionEventReasonAnnotation])
	assert.NotContains(t, events, EventReasonSessionStarted)

	env.reconcile(t)
	_, ok = env.hubSession(t)
	assert.False(t, ok, "an unclaimed copy has nothing to clean up, so it is deleted at once")
	_, ok = env.projectSession(t)
	assert.False(t, ok)
	assert.NotContains(t, env.events(t), EventReasonCleanupUnconfirmed)
}

func TestInstanceConsoleSessionClaimedIsNotUnavailable(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	claimOnHub(t, env)

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout + time.Minute)
	result := env.reconcile(t)
	assert.Zero(t, result.RequeueAfter, "a claimed session is the agent's to end")

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonSessionReady, sessionReadyReason(session))
	assert.True(t, session.DeletionTimestamp.IsZero())
}

func TestInstanceConsoleSessionClaimReflectedAfterUnavailableIsRevoked(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)
	session, ok := env.projectSession(t)
	require.True(t, ok)
	require.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(session))

	claimOnHub(t, env)
	result := env.reconcile(t)
	assert.Positive(t, result.RequeueAfter, "the late claim holds cleanup until the cell reports the end")

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	assert.True(t, revokeRequested(hubCopy), "the cell is asked to end the claim it made too late")
	session, ok = env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(session),
		"the project session keeps the end the control plane decided")
	assert.Nil(t, session.Status.Connection, "no client ever learns where to connect")

	endOnHub(t, env, computev1alpha.InstanceConsoleSessionReasonRevoked, nil)
	env.reconcile(t)

	_, ok = env.hubSession(t)
	assert.False(t, ok)
	_, ok = env.projectSession(t)
	assert.False(t, ok)
	events := env.events(t)
	assert.NotContains(t, events, EventReasonCleanupUnconfirmed)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable,
		events[EventReasonSessionEnded].Annotations[sessionEventReasonAnnotation])
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

// deleteHubCopy deletes the hub copy the way a cascade from its hub deployment
// does, and returns it as it is while terminating, or nil once it is gone.
func (e *sessionTestEnv) deleteHubCopy(t *testing.T) *computev1alpha.InstanceConsoleSession {
	t.Helper()
	hubCopy, ok := e.hubSession(t)
	require.True(t, ok)
	require.NoError(t, e.hub.Delete(context.Background(), hubCopy))
	hubCopy, _ = e.hubSession(t)
	return hubCopy
}

// holdHubCopy puts a finalizer on the hub copy, as a foreground cascade from
// its hub deployment briefly does, so a deletion leaves it terminating.
func (e *sessionTestEnv) holdHubCopy(t *testing.T) {
	t.Helper()
	hubCopy, ok := e.hubSession(t)
	require.True(t, ok)
	controllerutil.AddFinalizer(hubCopy, metav1.FinalizerDeleteDependents)
	require.NoError(t, e.hub.Update(context.Background(), hubCopy))
}

// newClaimedSessionEnv delivers a session, lets a cell claim it, and mirrors
// the claim onto the project session.
func newClaimedSessionEnv(t *testing.T) *sessionTestEnv {
	t.Helper()
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	claimOnHub(t, env)
	env.reconcile(t)
	session, ok := env.projectSession(t)
	require.True(t, ok)
	require.NotNil(t, session.Status.Connection)
	return env
}

func TestInstanceConsoleSessionTerminatingClaimedHubCopyEndsAgentLost(t *testing.T) {
	env := newClaimedSessionEnv(t)
	env.holdHubCopy(t)
	require.NotNil(t, env.deleteHubCopy(t))

	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonAgentLost, sessionReadyReason(session),
		"Karmada removes the cell copy with the hub copy, so nothing can report the end")
	assert.False(t, session.DeletionTimestamp.IsZero())
	ended, ok := env.events(t)[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonAgentLost, ended.Annotations[sessionEventReasonAnnotation])
}

func TestInstanceConsoleSessionTerminatingUnclaimedHubCopyEndsAtClaimDeadline(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	env.holdHubCopy(t)
	env.deleteHubCopy(t)

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout - time.Second)
	result := env.reconcile(t)
	assert.Equal(t, time.Second, result.RequeueAfter, "the terminating copy's name is taken until it goes")

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(session))
	ended, ok := env.events(t)[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, ended.Annotations[sessionEventReasonAnnotation])
}

func TestInstanceConsoleSessionVanishedHubCopyEndsClaimedSession(t *testing.T) {
	env := newClaimedSessionEnv(t)
	require.Nil(t, env.deleteHubCopy(t))

	env.reconcile(t)

	_, redelivered := env.hubSession(t)
	assert.False(t, redelivered, "a claimed session is never delivered twice")
	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonAgentLost, sessionReadyReason(session))
	assert.False(t, session.DeletionTimestamp.IsZero())
	ended, ok := env.events(t)[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonAgentLost, ended.Annotations[sessionEventReasonAnnotation])
}

func TestInstanceConsoleSessionVanishedUnclaimedHubCopyIsRedelivered(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	require.Nil(t, env.deleteHubCopy(t))

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout / 2)
	result := env.reconcile(t)
	assert.Equal(t, DefaultSessionClaimTimeout/2, result.RequeueAfter, "the claim deadline still counts from creation")
	_, ok := env.hubSession(t)
	assert.True(t, ok)
}

func TestInstanceConsoleSessionReplacedInstanceEndsDeliveredSession(t *testing.T) {
	env := newClaimedSessionEnv(t)

	instance := testSessionInstanceObj()
	require.NoError(t, env.project.Delete(context.Background(), instance))
	instance.ResourceVersion = ""
	instance.UID = "replacement-uid"
	require.NoError(t, env.project.Create(context.Background(), instance))

	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonInstanceNotFound, sessionReadyReason(session))
	assert.False(t, session.DeletionTimestamp.IsZero())
	ended, ok := env.events(t)[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonInstanceNotFound, ended.Annotations[sessionEventReasonAnnotation])

	env.reconcile(t)
	hubCopy, ok := env.hubSession(t)
	require.True(t, ok, "the copy stays until the cell reports the end")
	assert.True(t, revokeRequested(hubCopy))
}

func TestInstanceConsoleSessionWithoutLocationEndsUnavailable(t *testing.T) {
	hubWD := testHubWD(testRuntimeClass)
	delete(hubWD.Labels, locationLabel)
	env := newSessionTestEnv(t,
		[]client.Object{testSession(), testSessionInstanceObj(), testFederatedProjectWD()},
		[]client.Object{hubWD},
	)

	require.Error(t, env.reconcileErr(), "a copy without a location no agent could write is never delivered")
	_, delivered := env.hubSession(t)
	assert.False(t, delivered)

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)
	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(session))
}

func TestInstanceConsoleSessionDeletedBeforeFirstReconcileRecordsEnd(t *testing.T) {
	session := testSession()
	session.Finalizers = []string{computev1alpha.InstanceConsoleSessionFinalizer}
	env := newSessionTestEnv(t,
		[]client.Object{session, testSessionInstanceObj(), testFederatedProjectWD()},
		[]client.Object{testHubWD(testRuntimeClass)},
	)
	env.deleteProjectSession(t)

	env.reconcile(t)

	ended, ok := env.events(t)[EventReasonSessionEnded]
	require.True(t, ok, "the finalizer added at admission holds the session until its end is recorded")
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonRevoked, ended.Annotations[sessionEventReasonAnnotation])
	_, ok = env.projectSession(t)
	assert.False(t, ok)
	_, delivered := env.hubSession(t)
	assert.False(t, delivered)
}

func TestInstanceConsoleSessionEndedOnCellIsReleasedAtOnce(t *testing.T) {
	env := newClaimedSessionEnv(t)
	endOnHub(t, env, computev1alpha.InstanceConsoleSessionReasonCompleted, ptr.To(int32(3)))

	env.reconcile(t)

	events := env.events(t)
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
	assert.Zero(t, result.RequeueAfter)
	_, ok = env.hubSession(t)
	assert.False(t, ok, "the cell reported the end after stopping the command, so the copy goes at once")
	_, ok = env.projectSession(t)
	assert.False(t, ok)
	assert.NotContains(t, env.events(t), EventReasonCleanupUnconfirmed)
}

func TestInstanceConsoleSessionUnclaimedDeletedSessionIsReleasedAtOnce(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	env.deleteProjectSession(t)

	result := env.reconcile(t)
	assert.Zero(t, result.RequeueAfter)
	_, ok := env.hubSession(t)
	assert.False(t, ok)
	_, ok = env.projectSession(t)
	assert.False(t, ok)
	events := env.events(t)
	assert.NotContains(t, events, EventReasonCleanupUnconfirmed)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonRevoked,
		events[EventReasonSessionEnded].Annotations[sessionEventReasonAnnotation])
}

func TestInstanceConsoleSessionRevokeWaitsForTheCell(t *testing.T) {
	env := newClaimedSessionEnv(t)
	session := env.deleteProjectSession(t)

	env.now = session.DeletionTimestamp.Add(time.Second)
	result := env.reconcile(t)
	assert.Equal(t, defaultSessionCleanupTimeout-time.Second, result.RequeueAfter)

	hubCopy, ok := env.hubSession(t)
	require.True(t, ok)
	assert.True(t, revokeRequested(hubCopy), "the revoke reaches the cell through propagation")
	assert.True(t, hubCopy.DeletionTimestamp.IsZero(),
		"the copy stays so the cell's report can come back through it")
	_, ok = env.projectSession(t)
	assert.True(t, ok)
	assert.NotContains(t, env.events(t), EventReasonSessionEnded)

	endOnHub(t, env, computev1alpha.InstanceConsoleSessionReasonRevoked, nil)
	env.reconcile(t)

	_, ok = env.hubSession(t)
	assert.False(t, ok)
	_, ok = env.projectSession(t)
	assert.False(t, ok, "the cell's report confirms cleanup")
	events := env.events(t)
	assert.NotContains(t, events, EventReasonCleanupUnconfirmed)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonRevoked,
		events[EventReasonSessionEnded].Annotations[sessionEventReasonAnnotation])
}

func TestInstanceConsoleSessionRevokeRecordsTheCellsOwnEnd(t *testing.T) {
	env := newClaimedSessionEnv(t)
	env.deleteProjectSession(t)
	env.reconcile(t)

	endOnHub(t, env, computev1alpha.InstanceConsoleSessionReasonCompleted, ptr.To(int32(0)))
	env.reconcile(t)

	_, ok := env.projectSession(t)
	assert.False(t, ok)
	ended := env.events(t)[EventReasonSessionEnded]
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonCompleted, ended.Annotations[sessionEventReasonAnnotation],
		"a command that exited as the client deleted its session is recorded as completed")
	assert.Equal(t, "0", ended.Annotations[sessionEventExitCodeAnnotation])
}

func TestInstanceConsoleSessionCleanupTimeout(t *testing.T) {
	env := newClaimedSessionEnv(t)
	session := env.deleteProjectSession(t)

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
	assert.False(t, ok, "the copy is deleted, which ends the session on the cell and leaves the rest to its sweep")
}

func TestInstanceConsoleSessionNeverUnclaimsTheProjectSession(t *testing.T) {
	env := newClaimedSessionEnv(t)
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		*status = computev1alpha.InstanceConsoleSessionStatus{Conditions: []metav1.Condition{pendingCondition()}}
	})

	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.NotNil(t, session.Status.Connection)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonSessionReady, sessionReadyReason(session))
}

func TestInstanceConsoleSessionIgnoresAnotherSessionsHubCopy(t *testing.T) {
	stale := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSessionName,
			Namespace: testKarmadaNSStr,
			Labels:    map[string]string{computev1alpha.InstanceConsoleSessionUIDLabel: "earlier-session"},
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

func TestInstanceConsoleSessionRefusalIsCopied(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	endOnHub(t, env, computev1alpha.InstanceConsoleSessionReasonNoShell, nil)

	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonNoShell, sessionReadyReason(session),
		"an end replaces the Pending status the session was created with")
	ended, ok := env.events(t)[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonNoShell, ended.Annotations[sessionEventReasonAnnotation])

	env.reconcile(t)
	_, ok = env.hubSession(t)
	assert.False(t, ok, "a refused session has nothing to clean up on the cell")
	_, ok = env.projectSession(t)
	assert.False(t, ok)
}

func TestInstanceConsoleSessionIsNotDeliveredToAnAmbiguousPlacement(t *testing.T) {
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.scheduleHubDeployment(t, testHubWD(testRuntimeClass), testMemberCluster, "cell-dfw-2")

	err := env.reconcileErr()
	require.Error(t, err)
	assert.ErrorIs(t, err, errNoSingleMember, "no single cell can be trusted with the session")
	_, delivered := env.hubSession(t)
	assert.False(t, delivered)

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)
	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonUnavailable, sessionReadyReason(session))
}

func TestInstanceConsoleSessionKeepsItsFirstClaim(t *testing.T) {
	env := newClaimedSessionEnv(t)
	env.setHubStatus(t, func(status *computev1alpha.InstanceConsoleSessionStatus) {
		status.Connection = &computev1alpha.InstanceConsoleSessionConnection{
			EndpointID: "deadbeef",
			RelayURLs:  []string{"https://evil.example.test"},
			Target:     "evil.example.test:443",
		}
	})

	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, "abcdef", session.Status.Connection.EndpointID, "a claim never moves to another endpoint")
	assert.Equal(t, []string{testRelayURL}, session.Status.Connection.RelayURLs)
}

func TestInstanceConsoleSessionEndsWhenTheClaimingCellStopsReporting(t *testing.T) {
	env := newClaimedSessionEnv(t)
	env.setMemberReports(t, false)

	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonAgentLost, sessionReadyReason(session),
		"the hub copy keeps the last claim forever once its cell stops reporting")
	assert.False(t, session.DeletionTimestamp.IsZero(), "the ended session is deleted, releasing its quota")
	ended, ok := env.events(t)[EventReasonSessionEnded]
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonAgentLost, ended.Annotations[sessionEventReasonAnnotation])

	result := env.reconcile(t)
	assert.Zero(t, result.RequeueAfter, "nothing can report back, so cleanup does not wait")
	_, ok = env.projectSession(t)
	assert.False(t, ok)
	_, ok = env.hubSession(t)
	assert.False(t, ok)
	assert.Contains(t, env.events(t), EventReasonCleanupUnconfirmed)
}

func TestInstanceConsoleSessionEndWithoutStoppedProcessesIsNotConfirmed(t *testing.T) {
	env := newClaimedSessionEnv(t)
	endOnHubUnconfirmed(t, env, computev1alpha.InstanceConsoleSessionReasonCompleted)

	env.reconcile(t)
	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, computev1alpha.InstanceConsoleSessionReasonCompleted, sessionReadyReason(session),
		"the client learns of the end at once")
	require.False(t, session.DeletionTimestamp.IsZero())
	assert.Contains(t, env.events(t), EventReasonSessionEnded)

	env.now = session.DeletionTimestamp.Add(time.Minute)
	result := env.reconcile(t)
	assert.Equal(t, defaultSessionCleanupTimeout-time.Minute, result.RequeueAfter)
	hubCopy, ok := env.hubSession(t)
	require.True(t, ok, "the copy stays until the cell confirms its processes stopped")
	assert.True(t, hubCopy.DeletionTimestamp.IsZero())

	endOnHub(t, env, computev1alpha.InstanceConsoleSessionReasonCompleted, nil)
	env.reconcile(t)
	_, ok = env.hubSession(t)
	assert.False(t, ok)
	_, ok = env.projectSession(t)
	assert.False(t, ok)
	assert.NotContains(t, env.events(t), EventReasonCleanupUnconfirmed)
}

func TestInstanceConsoleSessionEndWithoutStoppedProcessesTimesOut(t *testing.T) {
	env := newClaimedSessionEnv(t)
	endOnHubUnconfirmed(t, env, computev1alpha.InstanceConsoleSessionReasonDisconnected)
	env.reconcile(t)
	session, ok := env.projectSession(t)
	require.True(t, ok)

	env.now = session.DeletionTimestamp.Add(defaultSessionCleanupTimeout)
	env.reconcile(t)

	assert.Contains(t, env.events(t), EventReasonCleanupUnconfirmed)
	_, ok = env.hubSession(t)
	assert.False(t, ok)
	_, ok = env.projectSession(t)
	assert.False(t, ok)
}

func TestInstanceConsoleSessionRecordsEachEventOnce(t *testing.T) {
	env := newClaimedSessionEnv(t)
	startOnHub(t, env)
	env.reconcile(t)
	env.reconcile(t)
	endOnHub(t, env, computev1alpha.InstanceConsoleSessionReasonCompleted, ptr.To(int32(0)))

	for range 4 {
		env.reconcile(t)
	}

	events := env.events(t)
	assert.Contains(t, events, EventReasonSessionStarted)
	assert.Contains(t, events, EventReasonSessionEnded)
	_, ok := env.projectSession(t)
	assert.False(t, ok)
}

func TestInstanceConsoleSessionStaleReconcileDoesNotRecordAgain(t *testing.T) {
	env := newClaimedSessionEnv(t)
	stale := endProjectSession(t, env)

	require.NoError(t, env.reconciler.recordLifecycleEvents(context.Background(), env.project, stale.DeepCopy(), ""))
	require.Contains(t, env.events(t), EventReasonSessionEnded)

	err := env.reconciler.recordLifecycleEvents(context.Background(), env.project, stale, "")
	assert.True(t, apierrors.IsConflict(err), "a reconcile working from a stale session must not record the end again, got %v", err)
	env.events(t)
}

func TestInstanceConsoleSessionRetriesAnEventThatFailedToRecord(t *testing.T) {
	env := newClaimedSessionEnv(t)
	session := endProjectSession(t, env)

	failing := interceptor.NewClient(env.project.(client.WithWatch), interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return errors.New("events store unavailable")
		},
	})
	require.Error(t, env.reconciler.recordLifecycleEvents(context.Background(), failing, session, ""))
	require.NotContains(t, env.events(t), EventReasonSessionEnded)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	require.NoError(t, env.reconciler.recordLifecycleEvents(context.Background(), env.project, session, ""))
	assert.Contains(t, env.events(t), EventReasonSessionEnded)
	session, ok = env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, sessionEventRecorded, session.Annotations[sessionEventMarkerAnnotation(EventReasonSessionEnded)])
}

// endProjectSession ends the project session the way copying the cell's end
// does, without reconciling, and returns it as a reconcile would read it.
func endProjectSession(t *testing.T, env *sessionTestEnv) *computev1alpha.InstanceConsoleSession {
	t.Helper()
	session, ok := env.projectSession(t)
	require.True(t, ok)
	apimeta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.InstanceConsoleSessionReady,
		Status:             metav1.ConditionFalse,
		Reason:             computev1alpha.InstanceConsoleSessionReasonCompleted,
		Message:            "The command exited",
		LastTransitionTime: metav1.NewTime(env.now.Truncate(time.Second)),
	})
	require.NoError(t, env.project.Status().Update(context.Background(), session))
	require.NotContains(t, env.events(t), EventReasonSessionEnded)
	return session
}
