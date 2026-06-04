package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
)

// newTestScheme builds a runtime.Scheme with the types needed for instance reconcile tests.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(s))
	require.NoError(t, quotav1alpha1.AddToScheme(s))
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
							Message:            "Quota exceeded for project",
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
				Message: "Quota exceeded for project",
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
// wrote the status update and returned before reaching removeQuotaSchedulingGate.
// Because ResourceClaims are immutable (no further transitions) and local
// Instances are not watched (WithEngageWithLocalCluster(false)), no requeue ever
// arrived — leaving the Quota gate stranded in spec.controller.schedulingGates
// and the projected Instance stuck "Pending (SchedulingGatesPresent)".
//
// The fix: on the success path (quotaErr==nil), fall through to
// removeQuotaSchedulingGate after persisting the status update, so gate removal
// happens in the same reconcile pass as the QuotaGranted=True status write.
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
		fakeRecorder *record.FakeRecorder,
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
		fakeRecorder := record.NewFakeRecorder(10)
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
	})

	// FM-4/FM-5: 404 on Create maps to NamespaceNotFound when the claim namespace
	// is known (the more common case for project-exists-but-namespace-absent), and
	// to ProjectNotFound when the namespace itself is empty (project CP path missing).
	t.Run("FM-5: 404 on Create with known namespace sets QuotaNamespaceNotFound", func(t *testing.T) {
		fakeRecorder := record.NewFakeRecorder(10)
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
		fakeRecorder := record.NewFakeRecorder(10)
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
		fakeRecorder := record.NewFakeRecorder(10)

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
			recorder:           record.NewFakeRecorder(10),
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
		fakeRecorder := record.NewFakeRecorder(10)

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
		// then removeQuotaSchedulingGate reads the in-memory condition (gen=2 ==
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
}
