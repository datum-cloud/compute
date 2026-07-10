package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
	"go.datum.net/compute/internal/quota"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// Test constants for repeated string literals across controller package tests.
const (
	testInstanceName           = "test-instance"
	testReasonString           = "TestReason"
	testMessageString          = "Test message"
	testUIDString              = "test-uid"
	testInstanceType           = "d1-standard-2"
	testDefaultPlacement       = "default"
	testDefaultNamespace       = "default"
	testEdgeClusterName        = "test-edge"
	testComputeAPIVersion      = "compute.datumapis.com/v1alpha"
	testQuotaAPIGroup          = "quota.miloapis.com"
	testQuotaResource          = "resourceclaims"
	kindWorkloadDeploymentTest = "WorkloadDeployment" // mirrors kindWorkloadDeployment

	// testMsgQuotaExceeded is the quota-denied message used across quota tests.
	testMsgQuotaExceeded = "Quota exceeded for project"

	// testMsgConfigMapNotFound is the resolver message for a missing ConfigMap,
	// used across Instance.Ready and WD.Available rollup tests.
	testMsgConfigMapNotFound = `ConfigMap "app-config" not found in namespace "default"`

	// testMsgNetworkCreationFailed is the network-failure message used by the
	// evaluate-all-then-pick blocking-reason tests.
	testMsgNetworkCreationFailed = "Network creation failed: timeout"

	// testPlacementA is the placement key used in Workload status rollup tests.
	testPlacementA = "placement-a"
)

// newTestScheme builds a runtime.Scheme with the types needed for instance reconcile tests.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(s))
	require.NoError(t, quotav1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func TestReconcileInstanceReadyCondition(t *testing.T) {

	tests := []struct {
		name               string
		instance           *computev1alpha.Instance
		networkFailureFunc networkFailureChecker
		expectedChanged    bool
		expectedCondition  *metav1.Condition
	}{
		{
			name: "instance without ready condition should create default",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             computev1alpha.InstanceProgrammedReasonPendingProgramming,
				Message:            msgNotProgrammed,
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance with scheduling gates should set scheduling gates present",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Spec: computev1alpha.InstanceSpec{
					Controller: &computev1alpha.InstanceController{
						SchedulingGates: []computev1alpha.SchedulingGate{
							{Name: "Network"},
						},
					},
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               computev1alpha.InstanceReady,
							Status:             metav1.ConditionFalse,
							Reason:             computev1alpha.InstanceProgrammedReasonPendingProgramming,
							Message:            msgNotProgrammed,
							ObservedGeneration: 1,
							LastTransitionTime: metav1.Now(),
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             computev1alpha.InstanceReadyReasonSchedulingGatesPresent,
				Message:            "Scheduling gates present: Network",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance with scheduling gates and network failure should set network failed",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Spec: computev1alpha.InstanceSpec{
					Controller: &computev1alpha.InstanceController{
						SchedulingGates: []computev1alpha.SchedulingGate{
							{Name: "Network"},
						},
					},
				},
			},
			networkFailureFunc: func(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (bool, string, error) {
				return true, "Network creation failed: timeout", nil
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             reasonNetworkFailedToCreate,
				Message:            "Network creation failed: timeout",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance not programmed should set pending programming",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionFalse,
							Reason:  testReasonString,
							Message: testMessageString,
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             testReasonString,
				Message:            testMessageString,
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance programmed but not available should wait for available",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: msgInstanceProgrammed,
						},
						{
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionFalse,
							Reason:  testReasonString,
							Message: testMessageString,
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             testReasonString,
				Message:            testMessageString,
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance fully ready should set ready condition",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: msgInstanceProgrammed,
						},
						{
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceAvailableReasonAvailable,
							Message: msgInstanceAvailable,
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionTrue,
				Reason:             computev1alpha.InstanceReadyReasonAvailable,
				Message:            msgInstanceReady,
				ObservedGeneration: 1,
			},
		},
		{
			name: "no change when condition already matches",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               computev1alpha.InstanceReady,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceReadyReasonAvailable,
							Message:            msgInstanceReady,
							ObservedGeneration: 1,
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: msgInstanceProgrammed,
						},
						{
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceAvailableReasonAvailable,
							Message: msgInstanceAvailable,
						},
					},
				},
			},
			expectedChanged: false,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionTrue,
				Reason:             computev1alpha.InstanceReadyReasonAvailable,
				Message:            msgInstanceReady,
				ObservedGeneration: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			reconciler := &InstanceReconciler{}

			networkFailureFunc := tt.networkFailureFunc
			if networkFailureFunc == nil {
				networkFailureFunc = func(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (bool, string, error) {
					return false, "", nil
				}
			}

			changed, err := reconciler.reconcileInstanceReadyCondition(
				ctx,
				nil,
				tt.instance,
				networkFailureFunc,
			)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedChanged, changed)

			readyCondition := apimeta.FindStatusCondition(tt.instance.Status.Conditions, computev1alpha.InstanceReady)
			require.NotNil(t, readyCondition)

			assert.Equal(t, tt.expectedCondition.Type, readyCondition.Type)
			assert.Equal(t, tt.expectedCondition.Status, readyCondition.Status)
			assert.Equal(t, tt.expectedCondition.Reason, readyCondition.Reason)
			assert.Equal(t, tt.expectedCondition.Message, readyCondition.Message)
			assert.Equal(t, tt.expectedCondition.ObservedGeneration, readyCondition.ObservedGeneration)
		})
	}
}

func TestReconcileInstanceReadyConditionWithQuota(t *testing.T) {
	tests := []struct {
		name              string
		instance          *computev1alpha.Instance
		expectedChanged   bool
		expectedCondition *metav1.Condition
	}{
		{
			name: "quota denied blocks ready condition",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               computev1alpha.InstanceQuotaGranted,
							Status:             metav1.ConditionFalse,
							Reason:             computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
							Message:            testMsgQuotaExceeded,
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:               computev1alpha.InstanceProgrammed,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceProgrammedReasonProgrammed,
							Message:            msgInstanceProgrammed,
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:               computev1alpha.InstanceAvailable,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceAvailableReasonAvailable,
							Message:            msgInstanceAvailable,
							LastTransitionTime: metav1.Now(),
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:    computev1alpha.InstanceReady,
				Status:  metav1.ConditionFalse,
				Reason:  computev1alpha.InstanceProgrammedReasonPendingQuota,
				Message: testMsgQuotaExceeded,
			},
		},
		{
			name: "quota available does not block ready condition",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               computev1alpha.InstanceQuotaGranted,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable,
							Message:            "Quota allocated",
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:               computev1alpha.InstanceProgrammed,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceProgrammedReasonProgrammed,
							Message:            msgInstanceProgrammed,
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:               computev1alpha.InstanceAvailable,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceAvailableReasonAvailable,
							Message:            msgInstanceAvailable,
							LastTransitionTime: metav1.Now(),
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:    computev1alpha.InstanceReady,
				Status:  metav1.ConditionTrue,
				Reason:  computev1alpha.InstanceReadyReasonAvailable,
				Message: msgInstanceReady,
			},
		},
		{
			name: "quota pending unknown does not block ready condition",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               computev1alpha.InstanceQuotaGranted,
							Status:             metav1.ConditionUnknown,
							Reason:             computev1alpha.InstanceQuotaGrantedReasonPendingEvaluation,
							Message:            "Waiting for quota evaluation",
							LastTransitionTime: metav1.Now(),
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:    computev1alpha.InstanceReady,
				Status:  metav1.ConditionFalse,
				Reason:  computev1alpha.InstanceProgrammedReasonPendingProgramming,
				Message: msgNotProgrammed,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &InstanceReconciler{}

			noNetworkFailure := func(_ context.Context, _ client.Client, _ *computev1alpha.Instance) (bool, string, error) {
				return false, "", nil
			}

			changed, err := reconciler.reconcileInstanceReadyCondition(
				context.Background(),
				nil,
				tt.instance,
				noNetworkFailure,
			)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedChanged, changed)

			readyCondition := apimeta.FindStatusCondition(tt.instance.Status.Conditions, computev1alpha.InstanceReady)
			require.NotNil(t, readyCondition)

			assert.Equal(t, tt.expectedCondition.Type, readyCondition.Type)
			assert.Equal(t, tt.expectedCondition.Status, readyCondition.Status)
			assert.Equal(t, tt.expectedCondition.Reason, readyCondition.Reason)
			assert.Equal(t, tt.expectedCondition.Message, readyCondition.Message)
		})
	}
}

// TestReconcileQuota tests the full Reconcile path for quota-related scenarios
// by wiring up a fake project cluster client and a fake management cluster client.
func TestReconcileQuota(t *testing.T) {
	const (
		clusterName  = "test-project"
		namespace    = "default"
		instanceName = "my-instance"
	)

	claimName := instanceQuotaClaimNamePrefix + instanceName

	const deploymentName = "my-deployment"

	// makeDeployment builds a WorkloadDeployment that owns the test instance.
	makeDeployment := func() *computev1alpha.WorkloadDeployment {
		return &computev1alpha.WorkloadDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: namespace,
				UID:       testUIDString,
			},
		}
	}

	// makeInstance creates a test Instance with an owner reference to the
	// deployment so that checkForNetworkCreationFailure can look it up.
	// Both finalizers are pre-populated so that the finalizer framework does
	// not need to add instanceControllerFinalizer on the first reconcile,
	// which would cause an early return before quota logic runs.
	makeInstance := func(_ *runtime.Scheme, gates ...computev1alpha.SchedulingGate) *computev1alpha.Instance {
		return &computev1alpha.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:       instanceName,
				Namespace:  testDefaultNamespace,
				Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: testComputeAPIVersion,
						Kind:       kindWorkloadDeploymentTest,
						Name:       deploymentName,
						UID:        testUIDString,
						Controller: func() *bool { b := true; return &b }(),
					},
				},
			},
			Spec: computev1alpha.InstanceSpec{
				Controller: &computev1alpha.InstanceController{
					SchedulingGates: gates,
				},
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
				},
				NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
			},
		}
	}

	makeClaim := func(_ *runtime.Scheme, granted metav1.ConditionStatus, reason string) *quotav1alpha1.ResourceClaim {
		return &quotav1alpha1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      claimName,
				Namespace: namespace,
			},
			Spec: quotav1alpha1.ResourceClaimSpec{
				ConsumerRef: quotav1alpha1.ConsumerRef{
					APIGroup: miloProjectAPIGroup,
					Kind:     miloProjectKind,
					Name:     clusterName,
				},
				// ResourceRef points at the Project resource (cluster-scoped), not the
				// Instance. The quota admission plugin validates against the
				// ResourceRegistration's claimingResources, which only allows
				// resourcemanager.miloapis.com/Project.
				ResourceRef: quotav1alpha1.UnversionedObjectReference{
					APIGroup: miloProjectAPIGroup,
					Kind:     miloProjectKind,
					Name:     clusterName,
				},
				Requests: []quotav1alpha1.ResourceRequest{
					{ResourceType: quotaResourceTypeInstances, Amount: 1},
				},
			},
			Status: quotav1alpha1.ResourceClaimStatus{
				Conditions: []metav1.Condition{
					{
						Type:               quotav1alpha1.ResourceClaimGranted,
						Status:             granted,
						Reason:             reason,
						Message:            "test message",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}
	}

	newReconciler := func(t *testing.T, projectObjs []client.Object, quotaObjs []client.Object) (*InstanceReconciler, client.Client, client.Client) {
		t.Helper()
		s := newTestScheme(t)

		projectClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(projectObjs...).
			WithStatusSubresource(&computev1alpha.Instance{}).
			Build()

		quotaClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(quotaObjs...).
			WithStatusSubresource(&quotav1alpha1.ResourceClaim{}).
			Build()

		mgr := &fakeMCManager{
			clusters: map[string]cluster.Cluster{
				clusterName: newFakeCluster(projectClient),
			},
		}

		qm := quota.New(nil)
		qm.StoreClient(clusterName, quotaClient)

		r := &InstanceReconciler{
			mgr:                mgr,
			scheme:             s,
			quotaClientManager: qm,
			edgeClusterName:    testEdgeClusterName,
			// Milo mode: project ID == ClusterName; claim namespace == instance.Namespace.
			projectIDForInstance: func(_ context.Context, cn multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
				return string(cn), nil
			},
			// nil → falls back to instance.Namespace, which is correct for Milo mode.
			projectNamespaceForInstance: nil,
		}

		// Initialize the finalizer registry so that r.finalizers.Finalize is not
		// a nil-pointer dereference. SetupWithManager does this in production; in
		// tests we replicate the same steps manually.
		r.finalizers = finalizer.NewFinalizers()
		require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

		return r, projectClient, quotaClient
	}

	t.Run("quota granted flow: claim granted removes gate and sets QuotaGranted=True in single reconcile", func(t *testing.T) {
		s := newTestScheme(t)
		instance := makeInstance(s,
			computev1alpha.SchedulingGate{Name: instancecontrol.NetworkSchedulingGate.String()},
			computev1alpha.SchedulingGate{Name: instancecontrol.QuotaSchedulingGate.String()},
		)
		claim := makeClaim(s, metav1.ConditionTrue, quotav1alpha1.ResourceClaimGrantedReason)

		r, projectClient, _ := newReconciler(t, []client.Object{instance, makeDeployment()}, []client.Object{claim})

		// Single reconcile: sets QuotaGranted=True in status AND removes the
		// Quota scheduling gate in the same pass. The early-return-before-gate-
		// removal bug required a second reconcile that never arrived because
		// ResourceClaims are immutable and local Instances are not watched.
		_, err := r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated))

		quotaCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, quotaCond)
		assert.Equal(t, metav1.ConditionTrue, quotaCond.Status)
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable, quotaCond.Reason)

		hasQuotaGate := false
		for _, g := range updated.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.QuotaSchedulingGate.String() {
				hasQuotaGate = true
			}
		}
		assert.False(t, hasQuotaGate, "QuotaSchedulingGate must be removed in the same reconcile pass as the status update")
	})

	t.Run("ready-condition reconcile error: quota condition persisted before the error returns", func(t *testing.T) {
		s := newTestScheme(t)
		// A scheduling gate keeps the Ready-condition reconcile on the network
		// failure checker path, and the missing owner reference makes that
		// checker fail.
		instance := makeInstance(s,
			computev1alpha.SchedulingGate{Name: instancecontrol.QuotaSchedulingGate.String()},
		)
		instance.OwnerReferences = nil
		claim := makeClaim(s, metav1.ConditionTrue, quotav1alpha1.ResourceClaimGrantedReason)

		r, projectClient, _ := newReconciler(t, []client.Object{instance, makeDeployment()}, []client.Object{claim})

		_, err := r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.Error(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated))

		quotaCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, quotaCond,
			"QuotaGranted condition must be persisted even when the Ready-condition reconcile fails")
		assert.Equal(t, metav1.ConditionTrue, quotaCond.Status)
	})

	t.Run("quota exceeded flow: conditions cascade to block Programmed/Available/Ready", func(t *testing.T) {
		s := newTestScheme(t)
		instance := makeInstance(s,
			computev1alpha.SchedulingGate{Name: instancecontrol.NetworkSchedulingGate.String()},
			computev1alpha.SchedulingGate{Name: instancecontrol.QuotaSchedulingGate.String()},
		)
		claim := makeClaim(s, metav1.ConditionFalse, quotav1alpha1.ResourceClaimDeniedReason)

		r, projectClient, _ := newReconciler(t, []client.Object{instance, makeDeployment()}, []client.Object{claim})

		_, err := r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated))

		quotaCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, quotaCond)
		assert.Equal(t, metav1.ConditionFalse, quotaCond.Status)
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded, quotaCond.Reason)

		programmedCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceProgrammed)
		require.NotNil(t, programmedCond)
		assert.Equal(t, metav1.ConditionFalse, programmedCond.Status)
		assert.Equal(t, computev1alpha.InstanceProgrammedReasonPendingQuota, programmedCond.Reason)

		availableCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceAvailable)
		require.NotNil(t, availableCond)
		assert.Equal(t, metav1.ConditionFalse, availableCond.Status)
		assert.Equal(t, computev1alpha.InstanceProgrammedReasonPendingQuota, availableCond.Reason)

		readyCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceReady)
		require.NotNil(t, readyCond)
		assert.Equal(t, metav1.ConditionFalse, readyCond.Status)
		assert.Equal(t, computev1alpha.InstanceProgrammedReasonPendingQuota, readyCond.Reason)
	})

	t.Run("quota restored: denied claim updated to granted triggers gate removal", func(t *testing.T) {
		s := newTestScheme(t)
		instance := makeInstance(s,
			computev1alpha.SchedulingGate{Name: instancecontrol.NetworkSchedulingGate.String()},
			computev1alpha.SchedulingGate{Name: instancecontrol.QuotaSchedulingGate.String()},
		)
		claim := makeClaim(s, metav1.ConditionFalse, quotav1alpha1.ResourceClaimDeniedReason)

		r, projectClient, mgmtClient := newReconciler(t, []client.Object{instance, makeDeployment()}, []client.Object{claim})

		// First reconcile with denied claim.
		_, err := r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		var blocked computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &blocked))
		quotaCond := apimeta.FindStatusCondition(blocked.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, quotaCond)
		assert.Equal(t, metav1.ConditionFalse, quotaCond.Status)

		// Update the claim to be granted (simulating quota increase).
		var existingClaim quotav1alpha1.ResourceClaim
		require.NoError(t, mgmtClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: claimName}, &existingClaim))
		existingClaim.Status.Conditions = []metav1.Condition{
			{
				Type:               quotav1alpha1.ResourceClaimGranted,
				Status:             metav1.ConditionTrue,
				Reason:             quotav1alpha1.ResourceClaimGrantedReason,
				Message:            "quota now available",
				LastTransitionTime: metav1.Now(),
			},
		}
		require.NoError(t, mgmtClient.Status().Update(context.Background(), &existingClaim))

		// Second reconcile should see the granted claim, update status to
		// QuotaGranted=True, AND remove the gate in the same pass (no third
		// reconcile required).
		_, err = r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		var recovered computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &recovered))
		quotaCond = apimeta.FindStatusCondition(recovered.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, quotaCond)
		assert.Equal(t, metav1.ConditionTrue, quotaCond.Status)

		hasQuotaGate := false
		for _, g := range recovered.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.QuotaSchedulingGate.String() {
				hasQuotaGate = true
			}
		}
		assert.False(t, hasQuotaGate, "QuotaSchedulingGate should be removed in the same reconcile pass that sets QuotaGranted=True")
	})

	t.Run("deleted before grant: finalizer deletes claim and is removed", func(t *testing.T) {
		s := newTestScheme(t)

		now := metav1.Now()
		// Build the instance directly without instanceControllerFinalizer to
		// represent the state after the Karmada finalizer has already been
		// cleaned up; only the quota finalizer remains to be processed.
		instance := &computev1alpha.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:              instanceName,
				Namespace:         namespace,
				DeletionTimestamp: &now,
				Finalizers:        []string{instanceQuotaFinalizer},
			},
			Spec: computev1alpha.InstanceSpec{
				Controller: &computev1alpha.InstanceController{
					SchedulingGates: []computev1alpha.SchedulingGate{
						{Name: instancecontrol.QuotaSchedulingGate.String()},
					},
				},
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
				},
				NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
			},
		}

		claim := makeClaim(s, metav1.ConditionFalse, quotav1alpha1.ResourceClaimPendingReason)

		r, projectClient, mgmtClient := newReconciler(t, []client.Object{instance}, []client.Object{claim})

		_, err := r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		// Claim should have been deleted from the management cluster.
		var deletedClaim quotav1alpha1.ResourceClaim
		err = mgmtClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: claimName}, &deletedClaim)
		assert.True(t, apierrors.IsNotFound(err), "ResourceClaim should have been deleted")

		// Finalizer should have been removed. The fake client may have garbage
		// collected the object once the last finalizer was cleared and
		// DeletionTimestamp was set, so accept either a clean object or NotFound.
		var updated computev1alpha.Instance
		getErr := projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated)
		if getErr != nil {
			assert.True(t, apierrors.IsNotFound(getErr), "unexpected error getting instance after finalizer removal")
		} else {
			assert.NotContains(t, updated.Finalizers, instanceQuotaFinalizer)
		}
	})
}

// TestQuotaGateRemovedInSingleReconcile is a regression test for the bug where
// the Quota scheduling gate was never removed from an Instance after quota was
// granted. The root cause was an early return in the Reconcile function: when
// reconcileQuotaCondition set QuotaGranted=True (statusChanged=true), the code
// wrote the status update and returned before reaching reconcileSchedulingGates.
// Because ResourceClaims are immutable (no further transitions) and local
// Instances are not watched (WithEngageWithLocalCluster(false)), no requeue ever
// arrived — leaving the Quota gate stranded in spec.controller.schedulingGates
// and the projected Instance stuck "Pending (SchedulingGatesPresent)".
func TestQuotaGateRemovedInSingleReconcile(t *testing.T) {
	const (
		clusterName    = "test-project"
		namespace      = "default"
		instanceName   = "my-instance"
		deploymentName = "my-deployment"
	)

	claimName := instanceQuotaClaimNamePrefix + instanceName

	tests := []struct {
		name           string
		initialGates   []computev1alpha.SchedulingGate
		expectGateGone bool
	}{
		{
			name: "Quota gate only: removed in single reconcile when claim is granted",
			initialGates: []computev1alpha.SchedulingGate{
				{Name: instancecontrol.QuotaSchedulingGate.String()},
			},
			expectGateGone: true,
		},
		{
			name: "Quota gate plus Network gate: Quota removed, Network preserved",
			initialGates: []computev1alpha.SchedulingGate{
				{Name: instancecontrol.NetworkSchedulingGate.String()},
				{Name: instancecontrol.QuotaSchedulingGate.String()},
			},
			expectGateGone: true,
		},
		{
			name:           "No gates: no-op, reconcile completes cleanly",
			initialGates:   []computev1alpha.SchedulingGate{},
			expectGateGone: false, // no gate to begin with
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestScheme(t)

			instance := &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       instanceName,
					Namespace:  namespace,
					Generation: 1,
					Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: testComputeAPIVersion,
							Kind:       kindWorkloadDeploymentTest,
							Name:       deploymentName,
							UID:        testUIDString,
							Controller: func() *bool { b := true; return &b }(),
						},
					},
				},
				Spec: computev1alpha.InstanceSpec{
					Controller: &computev1alpha.InstanceController{
						SchedulingGates: tt.initialGates,
					},
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
					},
					NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
				},
			}

			deployment := &computev1alpha.WorkloadDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: namespace, UID: testUIDString},
			}

			// ResourceClaim already in QuotaAvailable state — simulates the state
			// that triggered the bug: claim already granted but gate still present.
			claim := &quotav1alpha1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: namespace},
				Spec: quotav1alpha1.ResourceClaimSpec{
					ConsumerRef: quotav1alpha1.ConsumerRef{
						APIGroup: miloProjectAPIGroup, Kind: miloProjectKind, Name: clusterName,
					},
					ResourceRef: quotav1alpha1.UnversionedObjectReference{
						APIGroup: miloProjectAPIGroup, Kind: miloProjectKind, Name: clusterName,
					},
					Requests: []quotav1alpha1.ResourceRequest{
						{ResourceType: quotaResourceTypeInstances, Amount: 1},
					},
				},
				Status: quotav1alpha1.ResourceClaimStatus{
					Conditions: []metav1.Condition{
						{
							Type:               quotav1alpha1.ResourceClaimGranted,
							Status:             metav1.ConditionTrue,
							Reason:             quotav1alpha1.ResourceClaimGrantedReason,
							Message:            "quota available",
							LastTransitionTime: metav1.Now(),
						},
					},
				},
			}

			projectClient := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(instance, deployment).
				WithStatusSubresource(&computev1alpha.Instance{}).
				Build()

			quotaClient := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(claim).
				WithStatusSubresource(&quotav1alpha1.ResourceClaim{}).
				Build()

			mgr := &fakeMCManager{
				clusters: map[string]cluster.Cluster{
					clusterName: newFakeCluster(projectClient),
				},
			}

			qm := quota.New(nil)
			qm.StoreClient(clusterName, quotaClient)

			r := &InstanceReconciler{
				mgr:                mgr,
				scheme:             s,
				quotaClientManager: qm,
				edgeClusterName:    testEdgeClusterName,
				projectIDForInstance: func(_ context.Context, cn multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
					return string(cn), nil
				},
			}
			r.finalizers = finalizer.NewFinalizers()
			require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

			// Exactly one reconcile — must be sufficient to both set QuotaGranted=True
			// and remove the Quota gate. No second reconcile should be required.
			_, err := r.Reconcile(context.Background(), mcreconcile.Request{
				Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}},
				ClusterName: clusterName,
			})
			require.NoError(t, err)

			var updated computev1alpha.Instance
			require.NoError(t, projectClient.Get(context.Background(),
				types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated))

			// QuotaGranted condition must be set to True.
			quotaCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
			require.NotNil(t, quotaCond, "QuotaGranted condition must be present")
			assert.Equal(t, metav1.ConditionTrue, quotaCond.Status)
			assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable, quotaCond.Reason)

			// Quota gate must be gone after the single reconcile.
			hasQuotaGate := false
			for _, g := range updated.Spec.Controller.SchedulingGates {
				if g.Name == instancecontrol.QuotaSchedulingGate.String() {
					hasQuotaGate = true
				}
			}
			if tt.expectGateGone {
				assert.False(t, hasQuotaGate,
					"Quota gate must be removed in the same reconcile pass as the QuotaGranted=True status write; "+
						"a stranded gate leaves the projected Instance stuck Pending (SchedulingGatesPresent)")
			}

			// Network gate (if present) must be preserved — only the Quota gate is
			// cleared by InstanceReconciler; NetworkSchedulingGate is owned by
			// WorkloadDeploymentReconciler.
			for _, g := range updated.Spec.Controller.SchedulingGates {
				assert.NotEqual(t, instancecontrol.QuotaSchedulingGate.String(), g.Name,
					"Quota gate must not remain after granted claim")
			}
		})
	}
}

// TestReconcileQuotaSingleMode verifies that in single-cell mode:
//   - the project ID is decoded from the upstream-cluster-name label on the edge
//     namespace (not taken from the always-"single" ClusterName)
//   - the ResourceClaim is created in the in-project namespace (upstream-namespace
//     label, e.g. "default"), not in the edge namespace (ns-abc123)
//   - the ResourceRef points at resourcemanager.miloapis.com/Project, not Instance
func TestReconcileQuotaSingleMode(t *testing.T) {
	const (
		instanceName   = "my-instance"
		edgeNS         = "ns-abc123"   // edge namespace (ns-{uid}) — does NOT exist in project CP
		projectID      = "datum-cloud" // decoded from "cluster-datum-cloud"
		projectNS      = "default"     // upstream-namespace label value — where claims live
		deploymentName = "my-deployment"
	)

	// Claim name is the instance-prefixed Instance name; the claim object itself
	// lives in projectNS (the instance's edge namespace is carried on a label).
	claimName := instanceQuotaClaimNamePrefix + instanceName

	s := newTestScheme(t)

	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       instanceName,
			Namespace:  edgeNS,
			Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: testComputeAPIVersion,
					Kind:       kindWorkloadDeploymentTest,
					Name:       deploymentName,
					UID:        testUIDString,
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{
					{Name: instancecontrol.QuotaSchedulingGate.String()},
				},
			},
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
		},
	}

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: edgeNS, UID: "test-uid"},
	}

	// ResourceClaim lives in projectNS ("default"), not edgeNS ("ns-abc123").
	// ResourceRef points at the Project resource, matching the ResourceRegistration's
	// claimingResources (resourcemanager.miloapis.com/Project only).
	claim := &quotav1alpha1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: projectNS},
		Spec: quotav1alpha1.ResourceClaimSpec{
			ConsumerRef: quotav1alpha1.ConsumerRef{
				APIGroup: miloProjectAPIGroup,
				Kind:     miloProjectKind,
				Name:     projectID,
			},
			ResourceRef: quotav1alpha1.UnversionedObjectReference{
				APIGroup: miloProjectAPIGroup,
				Kind:     miloProjectKind,
				Name:     projectID,
			},
			Requests: []quotav1alpha1.ResourceRequest{
				{ResourceType: quotaResourceTypeInstances, Amount: 1},
			},
		},
		Status: quotav1alpha1.ResourceClaimStatus{
			Conditions: []metav1.Condition{
				{
					Type:               quotav1alpha1.ResourceClaimGranted,
					Status:             metav1.ConditionTrue,
					Reason:             quotav1alpha1.ResourceClaimGrantedReason,
					Message:            "quota granted",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	projectClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(instance, deployment).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	// The quota client is keyed by projectID ("datum-cloud"), matching what
	// projectIDForInstance returns after decoding "cluster-datum-cloud".
	quotaClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(claim).
		WithStatusSubresource(&quotav1alpha1.ResourceClaim{}).
		Build()

	qm := quota.New(nil)
	qm.StoreClient(projectID, quotaClient)

	const singleCluster = "single"

	mgr := &fakeMCManager{
		clusters: map[string]cluster.Cluster{
			singleCluster: newFakeCluster(projectClient),
		},
	}

	r := &InstanceReconciler{
		mgr:                mgr,
		scheme:             s,
		quotaClientManager: qm,
		edgeClusterName:    singleCluster,
		// Single-cell mode: project ID decoded from upstream-cluster-name label.
		// Simulates what cmd/main.go does for "cluster-datum-cloud" → "datum-cloud".
		projectIDForInstance: func(_ context.Context, _ multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
			return projectID, nil
		},
		// Single-cell mode: claim namespace comes from upstream-namespace label.
		// Simulates what cmd/main.go does by reading the edge namespace labels.
		projectNamespaceForInstance: func(_ context.Context, _ multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
			return projectNS, nil
		},
		// Single-cell mode: watch map func must always return "single".
		clusterNameForProject: func(_ string) multicluster.ClusterName {
			return singleCluster
		},
	}

	r.finalizers = finalizer.NewFinalizers()
	require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

	req := mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: edgeNS, Name: instanceName}},
		ClusterName: singleCluster,
	}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: edgeNS, Name: instanceName}, &updated))

	quotaCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	require.NotNil(t, quotaCond, "QuotaGranted condition must be set")
	assert.Equal(t, metav1.ConditionTrue, quotaCond.Status, "quota should be granted in single mode")
	assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable, quotaCond.Reason)

	// Verify clusterNameForProject always returns "single" so the watch map func
	// never enqueues an unknown cluster name.
	assert.Equal(t, multicluster.ClusterName(singleCluster), r.resolveClusterNameForProject(projectID))
	assert.Equal(t, multicluster.ClusterName(singleCluster), r.resolveClusterNameForProject("any-other-project"))

	// Verify resolveProjectNamespace returns the in-project namespace, not the edge namespace.
	resolvedNS, resolveErr := r.resolveProjectNamespace(context.Background(), singleCluster, instance)
	require.NoError(t, resolveErr)
	assert.Equal(t, projectNS, resolvedNS, "claim namespace must be the in-project namespace, not the edge namespace")
}

// TestReconcileQuotaFailureModes verifies that infrastructure failures in the
// quota path set specific QuotaGranted=False conditions (fail-closed) rather
// than silently allowing workloads to schedule.
func TestReconcileQuotaFailureModes(t *testing.T) {
	const (
		testProject    = "test-project"
		testNS         = "default"
		testInstance   = "my-instance"
		testDeployment = "my-deployment"
	)

	makeInstance := func() *computev1alpha.Instance {
		return &computev1alpha.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:       testInstance,
				Namespace:  testNS,
				Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: testComputeAPIVersion,
						Kind:       kindWorkloadDeploymentTest,
						Name:       testDeployment,
						UID:        testUIDString,
						Controller: func() *bool { b := true; return &b }(),
					},
				},
			},
			Spec: computev1alpha.InstanceSpec{
				Controller: &computev1alpha.InstanceController{
					SchedulingGates: []computev1alpha.SchedulingGate{
						{Name: instancecontrol.QuotaSchedulingGate.String()},
					},
				},
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
				},
				NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
			},
		}
	}

	makeDeployment := func() *computev1alpha.WorkloadDeployment {
		return &computev1alpha.WorkloadDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: testDeployment, Namespace: testNS, UID: testUIDString},
		}
	}

	newReconcilerWithInterceptor := func(
		t *testing.T,
		funcs interceptor.Funcs,
		fakeRecorder *capturingEventRecorder,
	) (*InstanceReconciler, client.Client) {
		t.Helper()
		s := newTestScheme(t)

		projectClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(makeInstance(), makeDeployment()).
			WithStatusSubresource(&computev1alpha.Instance{}).
			Build()

		quotaClient := fake.NewClientBuilder().
			WithScheme(s).
			WithInterceptorFuncs(funcs).
			Build()

		mgr := &fakeMCManager{
			clusters: map[string]cluster.Cluster{
				testProject: newFakeCluster(projectClient),
			},
		}

		qm := quota.New(nil)
		qm.StoreClient(testProject, quotaClient)

		r := &InstanceReconciler{
			mgr:                mgr,
			scheme:             s,
			quotaClientManager: qm,
			edgeClusterName:    testEdgeClusterName,
			recorder:           fakeRecorder,
			projectIDForInstance: func(_ context.Context, cn multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
				return string(cn), nil
			},
		}
		r.finalizers = finalizer.NewFinalizers()
		require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))
		return r, projectClient
	}

	reconcileReq := func() mcreconcile.Request {
		return mcreconcile.Request{
			Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testInstance}},
			ClusterName: testProject,
		}
	}

	t.Run("FM-2: backend unreachable sets QuotaBackendUnavailable", func(t *testing.T) {
		fakeRecorder := newCapturingEventRecorder(10)
		r, projectClient := newReconcilerWithInterceptor(t, interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
				return fmt.Errorf("connection refused")
			},
		}, fakeRecorder)

		_, err := r.Reconcile(context.Background(), reconcileReq())
		// Reconcile returns error for transient failures.
		require.Error(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(),
			types.NamespacedName{Namespace: testNS, Name: testInstance}, &updated))

		cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable, cond.Reason)

		// Event should have been emitted.
		select {
		case event := <-fakeRecorder.Events:
			assert.Contains(t, event, computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable)
		default:
			t.Error("expected a Warning event for backend unavailable, got none")
		}

		// The event carries the quota-claim action and references the Instance.
		// The apiserver rejects an events.k8s.io event with an empty action, so a
		// silently-empty action would 422-drop in production while passing CI.
		last := fakeRecorder.LastRecorded()
		require.NotNil(t, last)
		assert.Equal(t, eventActionClaimingQuota, last.Action)
		assert.NotEmpty(t, last.Action)
		assert.NotNil(t, last.Regarding)
	})

	// FM-4/FM-5: 404 on Create maps to NamespaceNotFound when the claim namespace
	// is known (the more common case for project-exists-but-namespace-absent), and
	// to ProjectNotFound when the namespace itself is empty (project CP path missing).
	t.Run("FM-5: 404 on Create with known namespace sets QuotaNamespaceNotFound", func(t *testing.T) {
		fakeRecorder := newCapturingEventRecorder(10)
		notFoundErr := apierrors.NewNotFound(
			schema.GroupResource{Group: testQuotaAPIGroup, Resource: testQuotaResource}, "claim")
		r, projectClient := newReconcilerWithInterceptor(t, interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
				return notFoundErr
			},
			Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
				return notFoundErr
			},
		}, fakeRecorder)

		_, err := r.Reconcile(context.Background(), reconcileReq())
		require.Error(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(),
			types.NamespacedName{Namespace: testNS, Name: testInstance}, &updated))

		cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		// claimNamespace == testNS (non-empty) → NamespaceNotFound, not ProjectNotFound.
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonNamespaceNotFound, cond.Reason,
			"404 on Create with known namespace should map to NamespaceNotFound")

		select {
		case event := <-fakeRecorder.Events:
			assert.Contains(t, event, computev1alpha.InstanceQuotaGrantedReasonNamespaceNotFound)
		default:
			t.Error("expected a Warning event for namespace not found, got none")
		}
	})

	t.Run("FM-6: 403 on Create sets QuotaMisconfigured", func(t *testing.T) {
		fakeRecorder := newCapturingEventRecorder(10)
		forbiddenErr := apierrors.NewForbidden(
			schema.GroupResource{Group: testQuotaAPIGroup, Resource: testQuotaResource}, "claim",
			fmt.Errorf("ResourceRegistration not found"))
		r, projectClient := newReconcilerWithInterceptor(t, interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
				return apierrors.NewNotFound(
					schema.GroupResource{Group: testQuotaAPIGroup, Resource: testQuotaResource}, "claim")
			},
			Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
				return forbiddenErr
			},
		}, fakeRecorder)

		_, err := r.Reconcile(context.Background(), reconcileReq())
		require.Error(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(),
			types.NamespacedName{Namespace: testNS, Name: testInstance}, &updated))

		cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonMisconfigured, cond.Reason,
			"403 on Create should map to Misconfigured")

		select {
		case event := <-fakeRecorder.Events:
			assert.Contains(t, event, computev1alpha.InstanceQuotaGrantedReasonMisconfigured)
		default:
			t.Error("expected a Warning event for misconfigured quota, got none")
		}
	})

	t.Run("FM-7: claim pending with no budget sets QuotaNoBudget", func(t *testing.T) {
		s := newTestScheme(t)
		fakeRecorder := newCapturingEventRecorder(10)

		claimName := instanceQuotaClaimNamePrefix + testInstance
		pendingClaim := &quotav1alpha1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: testNS},
			Spec: quotav1alpha1.ResourceClaimSpec{
				ConsumerRef: quotav1alpha1.ConsumerRef{
					APIGroup: miloProjectAPIGroup,
					Kind:     miloProjectKind,
					Name:     testProject,
				},
				ResourceRef: quotav1alpha1.UnversionedObjectReference{
					APIGroup: miloProjectAPIGroup,
					Kind:     miloProjectKind,
					Name:     testProject,
				},
				Requests: []quotav1alpha1.ResourceRequest{
					{ResourceType: quotaResourceTypeInstances, Amount: 1},
				},
			},
			Status: quotav1alpha1.ResourceClaimStatus{
				Conditions: []metav1.Condition{
					{
						Type:               quotav1alpha1.ResourceClaimGranted,
						Status:             metav1.ConditionFalse,
						Reason:             quotav1alpha1.ResourceClaimPendingReason,
						Message:            "No AllowanceBucket configured",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}

		projectClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(makeInstance(), makeDeployment()).
			WithStatusSubresource(&computev1alpha.Instance{}).
			Build()

		quotaClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(pendingClaim).
			WithStatusSubresource(&quotav1alpha1.ResourceClaim{}).
			Build()

		mgr := &fakeMCManager{
			clusters: map[string]cluster.Cluster{
				testProject: newFakeCluster(projectClient),
			},
		}
		qm := quota.New(nil)
		qm.StoreClient(testProject, quotaClient)

		r := &InstanceReconciler{
			mgr:                mgr,
			scheme:             s,
			quotaClientManager: qm,
			edgeClusterName:    testEdgeClusterName,
			recorder:           fakeRecorder,
			projectIDForInstance: func(_ context.Context, cn multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
				return string(cn), nil
			},
		}
		r.finalizers = finalizer.NewFinalizers()
		require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

		_, err := r.Reconcile(context.Background(), reconcileReq())
		require.NoError(t, err, "pending-no-budget is not a transient error — no requeue needed")

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(),
			types.NamespacedName{Namespace: testNS, Name: testInstance}, &updated))

		cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionUnknown, cond.Status)
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonNoBudget, cond.Reason,
			"pending claim with no budget should use NoBudget reason, not PendingEvaluation")

		select {
		case event := <-fakeRecorder.Events:
			assert.Contains(t, event, computev1alpha.InstanceQuotaGrantedReasonNoBudget)
		default:
			t.Error("expected a Warning event for no budget, got none")
		}
	})

	t.Run("quota disabled: quotaClientManager nil sets QuotaDisabled (not QuotaAvailable)", func(t *testing.T) {
		s := newTestScheme(t)
		instance := makeInstance()

		projectClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, makeDeployment()).
			WithStatusSubresource(&computev1alpha.Instance{}).
			Build()

		mgr := &fakeMCManager{
			clusters: map[string]cluster.Cluster{
				testProject: newFakeCluster(projectClient),
			},
		}

		r := &InstanceReconciler{
			mgr:                mgr,
			scheme:             s,
			quotaClientManager: nil, // explicitly disabled
			edgeClusterName:    testEdgeClusterName,
			recorder:           newCapturingEventRecorder(10),
			projectIDForInstance: func(_ context.Context, cn multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
				return string(cn), nil
			},
		}
		r.finalizers = finalizer.NewFinalizers()
		require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

		_, err := r.Reconcile(context.Background(), reconcileReq())
		require.NoError(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(),
			types.NamespacedName{Namespace: testNS, Name: testInstance}, &updated))

		cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonQuotaDisabled, cond.Reason,
			"intentionally disabled quota should use QuotaDisabled reason")
	})

	t.Run("observedGeneration guard: stale True condition does not remove gate for new generation", func(t *testing.T) {
		s := newTestScheme(t)
		fakeRecorder := newCapturingEventRecorder(10)

		// Instance at generation 2 with a stale QuotaGranted=True from generation 1.
		instance := makeInstance()
		instance.Generation = 2
		instance.Status.Conditions = []metav1.Condition{
			{
				Type:               computev1alpha.InstanceQuotaGranted,
				Status:             metav1.ConditionTrue,
				Reason:             computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable,
				Message:            "quota granted (generation 1)",
				ObservedGeneration: 1, // stale — does not match instance.Generation=2
				LastTransitionTime: metav1.Now(),
			},
		}

		projectClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, makeDeployment()).
			WithStatusSubresource(&computev1alpha.Instance{}).
			Build()

		claimName := instanceQuotaClaimNamePrefix + testInstance
		grantedClaim := &quotav1alpha1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: testNS},
			Spec: quotav1alpha1.ResourceClaimSpec{
				ConsumerRef: quotav1alpha1.ConsumerRef{APIGroup: miloProjectAPIGroup, Kind: miloProjectKind, Name: testProject},
				ResourceRef: quotav1alpha1.UnversionedObjectReference{APIGroup: miloProjectAPIGroup, Kind: miloProjectKind, Name: testProject},
				Requests:    []quotav1alpha1.ResourceRequest{{ResourceType: quotaResourceTypeInstances, Amount: 1}},
			},
			Status: quotav1alpha1.ResourceClaimStatus{
				Conditions: []metav1.Condition{
					{
						Type:               quotav1alpha1.ResourceClaimGranted,
						Status:             metav1.ConditionTrue,
						Reason:             quotav1alpha1.ResourceClaimGrantedReason,
						Message:            "granted",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}

		quotaClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(grantedClaim).
			WithStatusSubresource(&quotav1alpha1.ResourceClaim{}).
			Build()

		mgr := &fakeMCManager{
			clusters: map[string]cluster.Cluster{
				testProject: newFakeCluster(projectClient),
			},
		}
		qm := quota.New(nil)
		qm.StoreClient(testProject, quotaClient)

		r := &InstanceReconciler{
			mgr:                mgr,
			scheme:             s,
			quotaClientManager: qm,
			edgeClusterName:    testEdgeClusterName,
			recorder:           fakeRecorder,
			projectIDForInstance: func(_ context.Context, cn multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
				return string(cn), nil
			},
		}
		r.finalizers = finalizer.NewFinalizers()
		require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

		// Single reconcile: reconcileQuotaCondition writes QuotaGranted=True with
		// ObservedGeneration=2 into the in-memory instance, status is persisted,
		// then reconcileSchedulingGates reads the in-memory condition (gen=2 ==
		// instance.Generation=2) and removes the gate — all in one pass.
		_, err := r.Reconcile(context.Background(), reconcileReq())
		require.NoError(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(),
			types.NamespacedName{Namespace: testNS, Name: testInstance}, &updated))

		hasGate := false
		for _, g := range updated.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.QuotaSchedulingGate.String() {
				hasGate = true
			}
		}
		assert.False(t, hasGate, "gate should be removed in the same reconcile that refreshes the condition to current generation")

		cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, cond)
		assert.Equal(t, int64(2), cond.ObservedGeneration, "condition must reflect current generation")
	})

	t.Run("FM-1: missing identity label sets ProjectIDUnresolvable and errors", func(t *testing.T) {
		s := newTestScheme(t)
		fakeRecorder := newCapturingEventRecorder(10)

		projectClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(makeInstance(), makeDeployment()).
			WithStatusSubresource(&computev1alpha.Instance{}).
			Build()

		mgr := &fakeMCManager{
			clusters: map[string]cluster.Cluster{
				testProject: newFakeCluster(projectClient),
			},
		}
		qm := quota.New(nil)
		qm.StoreClient(testProject, fake.NewClientBuilder().WithScheme(s).Build())

		// Mirrors the single-mode resolver contract: the edge namespace exists
		// but was never stamped with the cluster-name identity label.
		identityErr := fmt.Errorf("edge namespace %q is missing label %q: %w",
			testNS, downstreamclient.UpstreamOwnerClusterNameLabel, errProjectIdentityUnresolvable)

		r := &InstanceReconciler{
			mgr:                mgr,
			scheme:             s,
			quotaClientManager: qm,
			edgeClusterName:    testEdgeClusterName,
			recorder:           fakeRecorder,
			projectIDForInstance: func(_ context.Context, _ multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
				return "", identityErr
			},
		}
		r.finalizers = finalizer.NewFinalizers()
		require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

		_, err := r.Reconcile(context.Background(), reconcileReq())
		require.Error(t, err, "unresolvable identity must surface as an error, not a silent PendingEvaluation park")
		require.ErrorIs(t, err, errProjectIdentityUnresolvable)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(),
			types.NamespacedName{Namespace: testNS, Name: testInstance}, &updated))

		cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonProjectIDUnresolvable, cond.Reason)
		assert.Contains(t, cond.Message, downstreamclient.UpstreamOwnerClusterNameLabel,
			"condition message must name the missing label")
		assert.Contains(t, cond.Message, testNS,
			"condition message must name the edge namespace")

		select {
		case event := <-fakeRecorder.Events:
			assert.Contains(t, event, computev1alpha.InstanceQuotaGrantedReasonProjectIDUnresolvable)
		default:
			t.Error("expected a Warning event for unresolvable project identity, got none")
		}
	})
}

// TestReconcileDeletionProjectIdentity verifies the deletion-path tradeoff for
// project-identity resolution: unresolvable identity (missing namespace labels,
// a misconfiguration no retry fixes) must not wedge deletion — claim cleanup is
// skipped and the claim may leak until Milo GC — while transient resolution
// failures retry rather than risking an orphaned claim.
func TestReconcileDeletionProjectIdentity(t *testing.T) {
	const (
		clusterName  = "test-project"
		namespace    = "default"
		instanceName = "my-instance"
	)
	claimName := instanceQuotaClaimNamePrefix + instanceName

	makeDeletingInstance := func() *computev1alpha.Instance {
		now := metav1.Now()
		return &computev1alpha.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:              instanceName,
				Namespace:         namespace,
				DeletionTimestamp: &now,
				Finalizers:        []string{instanceQuotaFinalizer},
			},
			Spec: computev1alpha.InstanceSpec{
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
				},
			},
		}
	}

	makeClaim := func() *quotav1alpha1.ResourceClaim {
		return &quotav1alpha1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: namespace},
			Spec: quotav1alpha1.ResourceClaimSpec{
				ConsumerRef: quotav1alpha1.ConsumerRef{APIGroup: miloProjectAPIGroup, Kind: miloProjectKind, Name: clusterName},
				ResourceRef: quotav1alpha1.UnversionedObjectReference{APIGroup: miloProjectAPIGroup, Kind: miloProjectKind, Name: clusterName},
				Requests:    []quotav1alpha1.ResourceRequest{{ResourceType: quotaResourceTypeInstances, Amount: 1}},
			},
		}
	}

	newReconciler := func(t *testing.T, projectIDFn InstanceProjectIDFunc, rec events.EventRecorder) (*InstanceReconciler, client.Client, client.Client) {
		t.Helper()
		s := newTestScheme(t)
		projectClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(makeDeletingInstance()).
			WithStatusSubresource(&computev1alpha.Instance{}).
			Build()
		quotaClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(makeClaim()).
			WithStatusSubresource(&quotav1alpha1.ResourceClaim{}).
			Build()
		mgr := &fakeMCManager{
			clusters: map[string]cluster.Cluster{
				clusterName: newFakeCluster(projectClient),
			},
		}
		qm := quota.New(nil)
		qm.StoreClient(clusterName, quotaClient)
		r := &InstanceReconciler{
			mgr:                  mgr,
			scheme:               s,
			quotaClientManager:   qm,
			edgeClusterName:      testEdgeClusterName,
			recorder:             rec,
			projectIDForInstance: projectIDFn,
		}
		r.finalizers = finalizer.NewFinalizers()
		require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))
		return r, projectClient, quotaClient
	}

	req := mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}},
		ClusterName: clusterName,
	}

	t.Run("unresolvable identity: deletion proceeds, claim cleanup skipped", func(t *testing.T) {
		fakeRecorder := newCapturingEventRecorder(10)
		identityErr := fmt.Errorf("edge namespace %q is missing label %q: %w",
			namespace, downstreamclient.UpstreamOwnerClusterNameLabel, errProjectIdentityUnresolvable)
		r, projectClient, quotaClient := newReconciler(t,
			func(_ context.Context, _ multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
				return "", identityErr
			}, fakeRecorder)

		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err, "unresolvable identity must not wedge deletion")

		// Finalizer removed; the fake client garbage collects the object once the
		// last finalizer clears, so accept either a clean object or NotFound.
		var updated computev1alpha.Instance
		getErr := projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated)
		if getErr != nil {
			assert.True(t, apierrors.IsNotFound(getErr), "unexpected error getting instance after finalizer removal")
		} else {
			assert.NotContains(t, updated.Finalizers, instanceQuotaFinalizer)
		}

		// Claim cleanup skipped — the claim leaks until Milo GC removes it.
		var claim quotav1alpha1.ResourceClaim
		require.NoError(t, quotaClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: claimName}, &claim),
			"claim must be left in place when identity is unresolvable")

		select {
		case event := <-fakeRecorder.Events:
			assert.Contains(t, event, "QuotaClaimOrphaned")
		default:
			t.Error("expected a QuotaClaimOrphaned event, got none")
		}

		// The deletion-path event names the quota-release action.
		last := fakeRecorder.LastRecorded()
		require.NotNil(t, last)
		assert.Equal(t, eventActionReleasingQuota, last.Action)
	})

	t.Run("transient resolution failure: reconcile errors and retries", func(t *testing.T) {
		fakeRecorder := newCapturingEventRecorder(10)
		r, projectClient, quotaClient := newReconciler(t,
			func(_ context.Context, _ multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
				return "", fmt.Errorf("connection refused")
			}, fakeRecorder)

		_, err := r.Reconcile(context.Background(), req)
		require.Error(t, err, "transient failures must retry rather than orphan the claim")

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated))
		assert.Contains(t, updated.Finalizers, instanceQuotaFinalizer,
			"finalizer must stay until claim cleanup succeeds")

		var claim quotav1alpha1.ResourceClaim
		require.NoError(t, quotaClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: claimName}, &claim))

		select {
		case event := <-fakeRecorder.Events:
			t.Errorf("no orphan event expected for a transient failure, got %q", event)
		default:
		}
	})
}

// TestQuotaPendingRequeueAfter verifies the backing-off safety-net requeue used
// while an instance's quota claim is still pending: 1s for the first minute, then
// 15s, then 60s after 5m, then 300s after 10m; and no requeue once granted.
func TestQuotaPendingRequeueAfter(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// created is the instance creation time; quota elapsed is measured from it
	// (NOT the condition's LastTransitionTime, which stays at the 1970 default
	// while quota is pending). The condition LastTransitionTime here is
	// deliberately left at the 1970 zero value to mirror that production reality.
	withQuota := func(s metav1.ConditionStatus, created time.Time) *computev1alpha.Instance {
		return &computev1alpha.Instance{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(created),
			},
			Status: computev1alpha.InstanceStatus{
				Conditions: []metav1.Condition{{
					Type:   computev1alpha.InstanceQuotaGranted,
					Status: s,
					Reason: "PendingEvaluation",
				}},
			},
		}
	}

	tests := []struct {
		name string
		inst *computev1alpha.Instance
		now  time.Time
		want time.Duration
	}{
		{"granted -> no requeue", withQuota(metav1.ConditionTrue, base), base.Add(time.Hour), 0},
		{"no quota condition -> no requeue", &computev1alpha.Instance{}, base, 0},
		{"just pending -> 1s", withQuota(metav1.ConditionUnknown, base), base.Add(5 * time.Second), quotaPendingRequeueFast},
		{"59s -> 1s", withQuota(metav1.ConditionUnknown, base), base.Add(59 * time.Second), quotaPendingRequeueFast},
		{"60s boundary -> 15s", withQuota(metav1.ConditionUnknown, base), base.Add(60 * time.Second), quotaPendingRequeueMedium},
		{"3m -> 15s", withQuota(metav1.ConditionUnknown, base), base.Add(3 * time.Minute), quotaPendingRequeueMedium},
		{"5m boundary -> 60s", withQuota(metav1.ConditionUnknown, base), base.Add(5 * time.Minute), quotaPendingRequeueSlow},
		{"8m -> 60s", withQuota(metav1.ConditionUnknown, base), base.Add(8 * time.Minute), quotaPendingRequeueSlow},
		{"10m boundary -> 300s", withQuota(metav1.ConditionUnknown, base), base.Add(10 * time.Minute), quotaPendingRequeueIdle},
		{"1h -> 300s", withQuota(metav1.ConditionUnknown, base), base.Add(time.Hour), quotaPendingRequeueIdle},
		{"denied(False) still polls", withQuota(metav1.ConditionFalse, base), base.Add(2 * time.Minute), quotaPendingRequeueMedium},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, quotaPendingRequeueAfter(tc.inst, tc.now))
		})
	}
}

// Shared literals for the instance-sizing / blocking-reason tests below.
const (
	testContainerName  = "app"
	testContainerImage = "test/image:latest"
)

// TestReconcileInstanceReadyCondition_ProviderSubConditionSurfacing verifies
// that provider-set sub-condition reasons (e.g. ImageUnavailable written by the
// unikraft provider onto the Available condition) surface on Ready with both the
// reason AND the message preserved — even when the sub-condition status is
// Unknown (the normal state for a retriable image-pull failure).
//
// This guards against Ready carrying a generic message that discards the
// actionable provider reason.
func TestReconcileInstanceReadyCondition_ProviderSubConditionSurfacing(t *testing.T) {
	// These messages mirror the exact strings that translateWaitingReason in the
	// unikraft provider writes. Both the reason AND the message must reach Ready.
	const (
		msgImageUnavailable      = "The instance image could not be pulled"
		msgInstanceCrashing      = "The instance is repeatedly failing to start"
		msgConfigError           = "The instance could not be started due to a configuration error"
		msgProvisioning          = "Instance is provisioning"
		msgProgrammingInProgress = "Instance is being programmed"
	)

	noGates := func(inst *computev1alpha.Instance) *computev1alpha.Instance { return inst }
	withQuotaGranted := func(inst *computev1alpha.Instance) *computev1alpha.Instance {
		inst.Status.Conditions = append(inst.Status.Conditions, metav1.Condition{
			Type:    computev1alpha.InstanceQuotaGranted,
			Status:  metav1.ConditionTrue,
			Reason:  computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable,
			Message: "Quota allocated",
		})
		return inst
	}

	tests := []struct {
		name        string
		instance    *computev1alpha.Instance
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
	}{
		{
			// The key scenario from the design: provider writes Available=Unknown/
			// ImageUnavailable while Programmed is still Unknown/ProgrammingInProgress.
			// Ready must carry ImageUnavailable + the actionable message, NOT the
			// generic "Instance has not been programmed".
			name: "image_pull_failure_surfaces_on_ready",
			instance: withQuotaGranted(&computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
							Message: msgProgrammingInProgress,
						},
						{
							// Provider sets Available=Unknown/ImageUnavailable when the
							// container enters an image-pull waiting state.
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceReadyReasonImageUnavailable,
							Message: msgImageUnavailable,
						},
					},
				},
			}),
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: msgImageUnavailable,
		},
		{
			// Even while Programmed is Unknown, Ready must surface the provider
			// sub-condition's reason and message; the generic PendingProgramming/
			// msgNotProgrammed pair is reserved for instances with no more
			// specific signal.
			name: "provider_reason_wins_over_generic_message_while_programmed_unknown",
			instance: withQuotaGranted(&computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
							Message: msgProgrammingInProgress,
						},
						{
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceReadyReasonImageUnavailable,
							Message: msgImageUnavailable,
						},
					},
				},
			}),
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: msgImageUnavailable,
		},
		{
			// When both a transient Provisioning and ImageUnavailable are present,
			// ImageUnavailable (priority 5) must win over Provisioning (priority 1).
			name: "image_unavailable_beats_transient_provisioning",
			instance: noGates(&computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceReadyReasonProvisioning,
							Message: msgProvisioning,
						},
						{
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceReadyReasonImageUnavailable,
							Message: msgImageUnavailable,
						},
					},
				},
			}),
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: msgImageUnavailable,
		},
		{
			// When no specific provider sub-condition exists but Programmed carries
			// a specific reason (ProgrammingInProgress), that reason should
			// pass-through to Ready. The generic msgNotProgrammed fallback is only
			// used when Programmed is absent or carries only a generic "Pending" reason.
			name: "programmed_in_progress_passes_through_when_no_provider_sub_condition",
			instance: noGates(&computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
							Message: msgProgrammingInProgress,
						},
					},
				},
			}),
			// ProgrammingInProgress is more specific than PendingProgramming and
			// passes through from Programmed → Ready.
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
			wantMessage: msgProgrammingInProgress,
		},
		{
			// True generic fallback: no Programmed condition at all. The default
			// PendingProgramming/msgNotProgrammed must be emitted.
			name: "generic_fallback_when_programmed_condition_absent",
			instance: noGates(&computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testInstanceName,
					Namespace: testDefaultNamespace,
				},
			}),
			wantStatus:  metav1.ConditionFalse,
			wantReason:  computev1alpha.InstanceProgrammedReasonPendingProgramming,
			wantMessage: msgNotProgrammed,
		},
		{
			// InstanceCrashing: terminal-ish (not retried indefinitely by the user,
			// they must fix the app). Status=Unknown from provider → Ready=Unknown.
			name: "instance_crashing_surfaces_on_ready",
			instance: withQuotaGranted(&computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
							Message: msgProgrammingInProgress,
						},
						{
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceReadyReasonInstanceCrashing,
							Message: msgInstanceCrashing,
						},
					},
				},
			}),
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  computev1alpha.InstanceReadyReasonInstanceCrashing,
			wantMessage: msgInstanceCrashing,
		},
		{
			// ConfigurationError: provider could not start the container due to a
			// spec/config issue. User must correct the workload.
			name: "configuration_error_surfaces_on_ready",
			instance: withQuotaGranted(&computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
							Message: msgProgrammingInProgress,
						},
						{
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceReadyReasonConfigurationError,
							Message: msgConfigError,
						},
					},
				},
			}),
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  computev1alpha.InstanceReadyReasonConfigurationError,
			wantMessage: msgConfigError,
		},
		{
			// When Programmed=True but Available=Unknown/ImageUnavailable, the
			// available-not-true branch must also propagate the provider reason+message.
			name: "image_unavailable_on_available_condition_programmed_true",
			instance: withQuotaGranted(&computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: msgInstanceProgrammed,
						},
						{
							Type:    computev1alpha.InstanceAvailable,
							Status:  metav1.ConditionUnknown,
							Reason:  computev1alpha.InstanceReadyReasonImageUnavailable,
							Message: msgImageUnavailable,
						},
					},
				},
			}),
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: msgImageUnavailable,
		},
	}

	noNetworkFailure := func(_ context.Context, _ client.Client, _ *computev1alpha.Instance) (bool, string, error) {
		return false, "", nil
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &InstanceReconciler{}
			_, err := r.reconcileInstanceReadyCondition(context.Background(), nil, tt.instance, noNetworkFailure)
			require.NoError(t, err)

			ready := apimeta.FindStatusCondition(tt.instance.Status.Conditions, computev1alpha.InstanceReady)
			require.NotNil(t, ready, "Ready condition must be set")
			assert.Equal(t, tt.wantStatus, ready.Status, "Ready.Status mismatch")
			assert.Equal(t, tt.wantReason, ready.Reason, "Ready.Reason mismatch")
			assert.Equal(t, tt.wantMessage, ready.Message, "Ready.Message mismatch")
		})
	}
}

// TestResolveInstanceResources verifies the three-tier sizing precedence:
// explicit container Limits > instance-level Requests > instanceType catalog.
func TestResolveInstanceResources(t *testing.T) {
	// d1Standard2 is the canonical catalog entry for datumcloud/d1-standard-2
	// (1 vCPU = 1000 millicores, 2 GiB = 2048 MiB) — the platform-declared quota
	// size for the instance type.
	const (
		d1CPUMillicores = int64(1000)
		d1MemMiB        = int64(2048)
	)

	cpu500m := resource.MustParse("500m")
	cpu1 := resource.MustParse("1")
	mem256Mi := resource.MustParse("256Mi")
	mem512Mi := resource.MustParse("512Mi")

	makeContainerResources := func(cpu, mem resource.Quantity) *computev1alpha.ContainerResourceRequirements {
		return &computev1alpha.ContainerResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    cpu,
				corev1.ResourceMemory: mem,
			},
		}
	}

	tests := []struct {
		name         string
		instance     *computev1alpha.Instance
		wantCPU      int64
		wantMem      int64
		wantResolved bool
	}{
		{
			// Common production case: instanceType only, no explicit limits.
			// resolveInstanceResources must consult the catalog and return the
			// d1-standard-2 values so vcpus + memory are included in the claim.
			name: "instanceType only: d1-standard-2 resolves from catalog",
			instance: &computev1alpha.Instance{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							InstanceType: instanceTypeD1Standard2,
						},
					},
				},
			},
			wantCPU:      d1CPUMillicores,
			wantMem:      d1MemMiB,
			wantResolved: true,
		},
		{
			// Explicit container Limits take precedence over the catalog so that
			// a workload with custom sizing is accounted at its actual footprint.
			name: "explicit container limits override catalog",
			instance: &computev1alpha.Instance{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							InstanceType: instanceTypeD1Standard2,
						},
						Sandbox: &computev1alpha.SandboxRuntime{
							Containers: []computev1alpha.SandboxContainer{
								{
									Name:      testContainerName,
									Image:     testContainerImage,
									Resources: makeContainerResources(cpu500m, mem256Mi),
								},
								{
									Name:      "sidecar",
									Image:     "test/sidecar:latest",
									Resources: makeContainerResources(cpu500m, mem256Mi),
								},
							},
						},
					},
				},
			},
			// Two containers each contributing 500m CPU + 256 MiB → 1000m + 512 MiB.
			wantCPU:      1000,
			wantMem:      512,
			wantResolved: true,
		},
		{
			// A single container with full cpu+memory Limits; no instanceType needed.
			name: "single container limits, no instanceType",
			instance: &computev1alpha.Instance{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Sandbox: &computev1alpha.SandboxRuntime{
							Containers: []computev1alpha.SandboxContainer{
								{
									Name:      testContainerName,
									Image:     testContainerImage,
									Resources: makeContainerResources(cpu1, mem512Mi),
								},
							},
						},
					},
				},
			},
			wantCPU:      1000,
			wantMem:      512,
			wantResolved: true,
		},
		{
			// Instance-level Requests (no sandbox, no instanceType) use path 2.
			name: "instance-level resources.requests resolve correctly",
			instance: &computev1alpha.Instance{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    cpu1,
								corev1.ResourceMemory: mem512Mi,
							},
						},
					},
				},
			},
			wantCPU:      1000,
			wantMem:      512,
			wantResolved: true,
		},
		{
			// An unknown instanceType with no explicit sizing must not fabricate
			// values; the caller falls back to claiming instance count only.
			name: "unknown instanceType, no explicit limits: unresolved",
			instance: &computev1alpha.Instance{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							InstanceType: "datumcloud/unknown-type-99",
						},
					},
				},
			},
			wantCPU:      0,
			wantMem:      0,
			wantResolved: false,
		},
		{
			// Empty instanceType and no explicit sizing: unresolved.
			name: "empty instanceType, nothing explicit: unresolved",
			instance: &computev1alpha.Instance{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{},
					},
				},
			},
			wantCPU:      0,
			wantMem:      0,
			wantResolved: false,
		},
		{
			// Sandbox containers without any Limits fall through to the catalog
			// when an instanceType is set — partial container specs must not block
			// catalog resolution.
			name: "sandbox containers without limits fall through to catalog",
			instance: &computev1alpha.Instance{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{
							InstanceType: instanceTypeD1Standard2,
						},
						Sandbox: &computev1alpha.SandboxRuntime{
							Containers: []computev1alpha.SandboxContainer{
								{
									Name:  testContainerName,
									Image: testContainerImage,
									// No Resources.Limits set — common for UKC workloads.
								},
							},
						},
					},
				},
			},
			wantCPU:      d1CPUMillicores,
			wantMem:      d1MemMiB,
			wantResolved: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cpu, mem, resolved := resolveInstanceResources(tt.instance)
			assert.Equal(t, tt.wantResolved, resolved, "resolved mismatch")
			assert.Equal(t, tt.wantCPU, cpu, "cpuMillicores mismatch")
			assert.Equal(t, tt.wantMem, mem, "memMiB mismatch")
		})
	}
}

// TestReconcileQuotaClaim_RequestsIncludeVCPUsAndMemory confirms that when an
// instance is sized by instanceType alone (the typical production shape), the
// ResourceClaim created by reconcileQuotaClaim includes vcpus and memory
// requests in addition to the instance count, so the AllowanceBuckets are fed.
func TestReconcileQuotaClaim_RequestsIncludeVCPUsAndMemory(t *testing.T) {
	const (
		clusterName  = "test-project"
		namespace    = "default"
		instanceName = "claim-resources-test"
	)

	claimName := instanceQuotaClaimNamePrefix + instanceName

	s := newTestScheme(t)

	// Instance sized by instanceType only — no container limits, no explicit
	// instance-level requests. This is the common production workload shape.
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       instanceName,
			Namespace:  namespace,
			Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: testComputeAPIVersion,
					Kind:       kindWorkloadDeploymentTest,
					Name:       "owner-deployment",
					UID:        testUIDString,
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{
					{Name: instancecontrol.QuotaSchedulingGate.String()},
				},
			},
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{
					// No Requests, no container Limits — catalog must supply the values.
					InstanceType: instanceTypeD1Standard2,
				},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
		},
	}

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owner-deployment",
			Namespace: namespace,
			UID:       testUIDString,
		},
	}

	projectClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(instance, deployment).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	quotaClient := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&quotav1alpha1.ResourceClaim{}).
		Build()

	qm := quota.New(nil)
	qm.StoreClient(clusterName, quotaClient)

	r := &InstanceReconciler{
		mgr:                &fakeMCManager{clusters: map[string]cluster.Cluster{clusterName: newFakeCluster(projectClient)}},
		scheme:             s,
		quotaClientManager: qm,
		edgeClusterName:    testEdgeClusterName,
		projectIDForInstance: func(_ context.Context, cn multicluster.ClusterName, _ *computev1alpha.Instance) (string, error) {
			return string(cn), nil
		},
		recorder: &capturingEventRecorder{},
	}
	r.finalizers = finalizer.NewFinalizers()
	require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

	_, err := r.Reconcile(context.Background(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}},
		ClusterName: clusterName,
	})
	require.NoError(t, err)

	// Verify the created ResourceClaim carries vcpus and memory requests.
	var createdClaim quotav1alpha1.ResourceClaim
	require.NoError(t, quotaClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: claimName}, &createdClaim))

	byType := make(map[string]int64, len(createdClaim.Spec.Requests))
	for _, req := range createdClaim.Spec.Requests {
		byType[req.ResourceType] = req.Amount
	}

	assert.Equal(t, int64(1), byType[quotaResourceTypeInstances], "instance count must be 1")
	assert.Equal(t, int64(1000), byType["compute.datumapis.com/vcpus"],
		"d1-standard-2 must claim 1000 millicores (1 vCPU)")
	assert.Equal(t, int64(2048), byType["compute.datumapis.com/memory"],
		"d1-standard-2 must claim 2048 MiB (2 GiB)")
}

// makeInstanceWithRefDataCondition builds an Instance with the ReferencedData
// scheduling gate and a ReferencedDataReady=False condition with the given
// reason and message. Omit reason to skip setting the condition entirely.
func makeInstanceWithRefDataCondition(refDataReason, refDataMessage string) *computev1alpha.Instance {
	inst := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testInstanceName,
			Namespace:  testDefaultNamespace,
			Generation: 1,
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{
					{Name: instancecontrol.ReferencedDataSchedulingGate.String()},
				},
			},
		},
	}
	if refDataReason != "" {
		inst.Status.Conditions = []metav1.Condition{
			{
				Type:               computev1alpha.ReferencedDataReady,
				Status:             metav1.ConditionFalse,
				Reason:             refDataReason,
				Message:            refDataMessage,
				LastTransitionTime: metav1.Now(),
			},
		}
	}
	return inst
}

// TestReconcileInstanceReadyCondition_ReferencedDataEnrichment verifies that
// reconcileInstanceReadyCondition surfaces the most specific blocking reason from
// the ReferencedDataReady sub-condition rather than always falling back to
// SchedulingGatesPresent.
func TestReconcileInstanceReadyCondition_ReferencedDataEnrichment(t *testing.T) {
	noNetworkFailure := func(_ context.Context, _ client.Client, _ *computev1alpha.Instance) (bool, string, error) {
		return false, "", nil
	}

	tests := []struct {
		name            string
		instance        *computev1alpha.Instance
		wantStatus      metav1.ConditionStatus
		wantReason      string
		wantMsgContains string
	}{
		{
			name: "SourceNotFound from ReferencedDataReady propagates to Ready",
			instance: makeInstanceWithRefDataCondition(
				computev1alpha.ReferencedDataReasonSourceNotFound,
				testMsgConfigMapNotFound,
			),
			wantStatus:      metav1.ConditionFalse,
			wantReason:      computev1alpha.ReferencedDataReasonSourceNotFound,
			wantMsgContains: `ConfigMap "app-config" not found`,
		},
		{
			name: "SourceTooLarge from ReferencedDataReady propagates verbatim",
			instance: makeInstanceWithRefDataCondition(
				computev1alpha.ReferencedDataReasonSourceTooLarge,
				"ConfigMap app-config exceeds the 1 MiB limit",
			),
			wantStatus:      metav1.ConditionFalse,
			wantReason:      computev1alpha.ReferencedDataReasonSourceTooLarge,
			wantMsgContains: "exceeds the 1 MiB limit",
		},
		{
			name: "AwaitingPropagation uses cell-side message from ReferencedDataReady",
			instance: makeInstanceWithRefDataCondition(
				computev1alpha.ReferencedDataReasonAwaitingPropagation,
				"Waiting for 1 companion(s) to arrive on cell: ConfigMap/app-config",
			),
			wantStatus:      metav1.ConditionFalse,
			wantReason:      computev1alpha.ReferencedDataReasonAwaitingPropagation,
			wantMsgContains: "ConfigMap/app-config",
		},
		{
			name: "ReferencedData gate with no sub-condition falls back to SchedulingGatesPresent",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       testInstanceName,
					Namespace:  testDefaultNamespace,
					Generation: 1,
				},
				Spec: computev1alpha.InstanceSpec{
					Controller: &computev1alpha.InstanceController{
						SchedulingGates: []computev1alpha.SchedulingGate{
							{Name: instancecontrol.ReferencedDataSchedulingGate.String()},
						},
					},
				},
			},
			wantStatus:      metav1.ConditionFalse,
			wantReason:      computev1alpha.InstanceReadyReasonSchedulingGatesPresent,
			wantMsgContains: "ReferencedData",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &InstanceReconciler{}
			changed, err := r.reconcileInstanceReadyCondition(context.Background(), nil, tt.instance, noNetworkFailure)
			require.NoError(t, err)
			assert.True(t, changed)

			cond := apimeta.FindStatusCondition(tt.instance.Status.Conditions, computev1alpha.InstanceReady)
			require.NotNil(t, cond)
			assert.Equal(t, tt.wantStatus, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
			assert.Contains(t, cond.Message, tt.wantMsgContains)
		})
	}
}

// TestReconcileInstanceReadyCondition_EvaluateAllThenPick verifies that all
// blocking sub-conditions are evaluated before selecting the winner, so a
// higher-priority cause wins even when a lower-priority one is encountered first.
func TestReconcileInstanceReadyCondition_EvaluateAllThenPick(t *testing.T) {
	t.Run("NetworkFailedToCreate wins over AwaitingPropagation (priority 7 > 4)", func(t *testing.T) {
		// priority 7 (NetworkFailedToCreate) must beat priority 4 (AwaitingPropagation).
		instance := makeInstanceWithRefDataCondition(
			computev1alpha.ReferencedDataReasonAwaitingPropagation,
			"Waiting for 1 companion(s) to arrive on cell: ConfigMap/app-config",
		)

		alwaysNetworkFailed := func(_ context.Context, _ client.Client, _ *computev1alpha.Instance) (bool, string, error) {
			return true, testMsgNetworkCreationFailed, nil
		}

		r := &InstanceReconciler{}
		_, err := r.reconcileInstanceReadyCondition(context.Background(), nil, instance, alwaysNetworkFailed)
		require.NoError(t, err)

		cond := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceReady)
		require.NotNil(t, cond)
		assert.Equal(t, reasonNetworkFailedToCreate, cond.Reason,
			"NetworkFailedToCreate (priority 7) should beat AwaitingPropagation (priority 4)")
	})

	t.Run("SourceNotFound wins over AwaitingPropagation (priority 5 > 4)", func(t *testing.T) {
		// Both are referenced-data related, but SourceNotFound is terminal.
		inst := &computev1alpha.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:       testInstanceName,
				Namespace:  testDefaultNamespace,
				Generation: 1,
			},
			Spec: computev1alpha.InstanceSpec{
				Controller: &computev1alpha.InstanceController{
					SchedulingGates: []computev1alpha.SchedulingGate{
						{Name: instancecontrol.ReferencedDataSchedulingGate.String()},
					},
				},
			},
			Status: computev1alpha.InstanceStatus{
				Conditions: []metav1.Condition{
					{
						Type:               computev1alpha.ReferencedDataReady,
						Status:             metav1.ConditionFalse,
						Reason:             computev1alpha.ReferencedDataReasonSourceNotFound,
						Message:            testMsgConfigMapNotFound,
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}

		noNetworkFailure := func(_ context.Context, _ client.Client, _ *computev1alpha.Instance) (bool, string, error) {
			return false, "", nil
		}
		r := &InstanceReconciler{}
		_, err := r.reconcileInstanceReadyCondition(context.Background(), nil, inst, noNetworkFailure)
		require.NoError(t, err)

		cond := apimeta.FindStatusCondition(inst.Status.Conditions, computev1alpha.InstanceReady)
		require.NotNil(t, cond)
		assert.Equal(t, computev1alpha.ReferencedDataReasonSourceNotFound, cond.Reason,
			"SourceNotFound should propagate verbatim to Ready condition")
		assert.Contains(t, cond.Message, `ConfigMap "app-config" not found`)
	})
}

// TestReconcileInstanceReadyCondition_QuotaVsReferencedData is the RFC §8.1
// headline case: QuotaGranted=False/QuotaExceeded AND
// ReferencedDataReady=False/SourceNotFound co-occur.
//
// SourceNotFound (priority 5) must win over PendingQuota (priority 3) on Ready.
// Programmed=False and Running=False must still be set (quota side effects are
// preserved regardless of which reason wins Ready).
func TestReconcileInstanceReadyCondition_QuotaVsReferencedData(t *testing.T) {
	noNetworkFailure := func(_ context.Context, _ client.Client, _ *computev1alpha.Instance) (bool, string, error) {
		return false, "", nil
	}

	// Instance has both the Quota gate and the ReferencedData gate, matching the
	// scenario where reconcileQuotaCondition and reconcileReferencedDataCondition
	// have both already run and written their respective sub-conditions.
	inst := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testInstanceName,
			Namespace:  testDefaultNamespace,
			Generation: 1,
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{
					{Name: instancecontrol.QuotaSchedulingGate.String()},
					{Name: instancecontrol.ReferencedDataSchedulingGate.String()},
				},
			},
		},
		Status: computev1alpha.InstanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               computev1alpha.InstanceQuotaGranted,
					Status:             metav1.ConditionFalse,
					Reason:             computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
					Message:            testMsgQuotaExceeded,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               computev1alpha.ReferencedDataReady,
					Status:             metav1.ConditionFalse,
					Reason:             computev1alpha.ReferencedDataReasonSourceNotFound,
					Message:            testMsgConfigMapNotFound,
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	r := &InstanceReconciler{}
	changed, err := r.reconcileInstanceReadyCondition(context.Background(), nil, inst, noNetworkFailure)
	require.NoError(t, err)
	assert.True(t, changed)

	// Ready must carry the higher-priority SourceNotFound reason (priority 5),
	// not PendingQuota (priority 3).
	readyCond := apimeta.FindStatusCondition(inst.Status.Conditions, computev1alpha.InstanceReady)
	require.NotNil(t, readyCond, "Ready condition must be set")
	assert.Equal(t, metav1.ConditionFalse, readyCond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceNotFound, readyCond.Reason,
		"SourceNotFound (priority 5) must beat PendingQuota (priority 3)")
	assert.Equal(t, testMsgConfigMapNotFound, readyCond.Message,
		"Ready message must be the SourceNotFound message verbatim")

	// Programmed and Running must still be set to False/PendingQuota — quota
	// side effects are preserved regardless of which reason wins Ready.
	programmedCond := apimeta.FindStatusCondition(inst.Status.Conditions, computev1alpha.InstanceProgrammed)
	require.NotNil(t, programmedCond, "Programmed condition must be set when quota is denied")
	assert.Equal(t, metav1.ConditionFalse, programmedCond.Status)
	assert.Equal(t, computev1alpha.InstanceProgrammedReasonPendingQuota, programmedCond.Reason,
		"Programmed must reflect quota denial even when Ready surfaces a different reason")

	availableCond := apimeta.FindStatusCondition(inst.Status.Conditions, computev1alpha.InstanceAvailable)
	require.NotNil(t, availableCond, "Available condition must be set when quota is denied")
	assert.Equal(t, metav1.ConditionFalse, availableCond.Status)
	assert.Equal(t, computev1alpha.InstanceProgrammedReasonPendingQuota, availableCond.Reason,
		"Available must reflect quota denial even when Ready surfaces a different reason")
}

// TestInstanceBlockingReasonPriority exhaustively verifies the priority table for
// Instance.Ready reason selection, as extended in this layer to rank
// referenced-data reasons. Every listed reason must return the expected integer;
// reasons absent from the table (including WorkloadDeployment-only reasons that
// the Instance-side table does not rank) must return 0.
//
// NOTE (split): the priority scheme here is the foundation's Instance-side table
// (Provisioning=1, PendingQuota=3, hard runtime=5, NetworkFailedToCreate=7),
// extended with referenced-data tiers (transient=4, terminal=5). This differs
// from the WorkloadDeployment-side table (wdBlockingReasonPriority), which keeps
// its own 1..7 ranking.
func TestInstanceBlockingReasonPriority(t *testing.T) {
	tests := []struct {
		reason   string
		wantPrio int
	}{
		// Priority 0: unknown / not ranked on the Instance-side table.
		{"", 0},
		{"SomethingElse", 0},
		{computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, 0},
		{computev1alpha.WorkloadDeploymentReasonNetworkProvisioning, 0},
		{computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, 0},
		{computev1alpha.WorkloadReasonNetworkNotFound, 0},
		// Priority 1: transient runtime startup.
		{computev1alpha.InstanceReadyReasonProvisioning, 1},
		// Priority 3: quota.
		{computev1alpha.InstanceProgrammedReasonPendingQuota, 3},
		// Priority 4: transient referenced-data.
		{computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, 4},
		{computev1alpha.ReferencedDataReasonAwaitingPropagation, 4},
		{computev1alpha.ReferencedDataReasonResolving, 4},
		// Priority 5: hard runtime errors and terminal referenced-data.
		{computev1alpha.InstanceReadyReasonImageUnavailable, 5},
		{computev1alpha.InstanceReadyReasonInstanceCrashing, 5},
		{computev1alpha.InstanceReadyReasonConfigurationError, 5},
		{computev1alpha.ReferencedDataReasonSourceNotFound, 5},
		{computev1alpha.ReferencedDataReasonSourceTooLarge, 5},
		{computev1alpha.ReferencedDataReasonSourceUnauthorized, 5},
		// Priority 7: hard infra error.
		{reasonNetworkFailedToCreate, 7},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			assert.Equal(t, tt.wantPrio, instanceBlockingReasonPriority(tt.reason),
				"unexpected priority for reason %q", tt.reason)
		})
	}
}

// TestEmitEvent exercises the three load-bearing behaviors of the emitEvent
// helper directly: the nil-recorder guard, the "%s" indirection that keeps a
// literal '%' in a message from being format-expanded, and the note truncation
// that keeps events.k8s.io/v1 from rejecting an oversized note server-side.
func TestEmitEvent(t *testing.T) {
	obj := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "emit-test-instance", Namespace: "default"},
	}

	t.Run("nil recorder does not panic", func(t *testing.T) {
		r := &InstanceReconciler{}
		require.NotPanics(t, func() {
			r.emitEvent(obj, corev1.EventTypeWarning, "SomeReason", eventActionClaimingQuota, "msg")
		})
	})

	t.Run("literal % in message is not format-expanded", func(t *testing.T) {
		rec := newCapturingEventRecorder(1)
		r := &InstanceReconciler{recorder: rec}
		r.emitEvent(obj, corev1.EventTypeWarning, "SomeReason", eventActionClaimingQuota,
			"connection refused: path %2Fapi got 50% errors")
		last := rec.LastRecorded()
		require.NotNil(t, last)
		assert.Contains(t, last.Note, "%2Fapi")
		assert.Contains(t, last.Note, "50%")
		assert.NotContains(t, last.Note, "(MISSING)")
	})

	t.Run("note truncated at maxEventNoteLen", func(t *testing.T) {
		rec := newCapturingEventRecorder(1)
		r := &InstanceReconciler{recorder: rec}
		r.emitEvent(obj, corev1.EventTypeWarning, "SomeReason", eventActionClaimingQuota,
			strings.Repeat("x", 2000))
		last := rec.LastRecorded()
		require.NotNil(t, last)
		assert.LessOrEqual(t, len(last.Note), maxEventNoteLen)
		assert.True(t, strings.HasSuffix(last.Note, "..."))
	})
}
