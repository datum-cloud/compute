package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
	"go.datum.net/compute/internal/quota"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
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
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             computev1alpha.InstanceProgrammedReasonPendingProgramming,
				Message:            "Instance has not been programmed",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance with scheduling gates should set scheduling gates present",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
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
							Message:            "Instance has not been programmed",
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
					Name:       "test-instance",
					Namespace:  "default",
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
				Reason:             "NetworkFailedToCreate",
				Message:            "Network creation failed: timeout",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance not programmed should set pending programming",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionFalse,
							Reason:  "TestReason",
							Message: "Test message",
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             "TestReason",
				Message:            "Test message",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance programmed but not running should wait for running",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: "Instance has been programmed",
						},
						{
							Type:    computev1alpha.InstanceRunning,
							Status:  metav1.ConditionFalse,
							Reason:  "TestReason",
							Message: "Test message",
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             "TestReason",
				Message:            "Test message",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance fully ready should set ready condition",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: "Instance has been programmed",
						},
						{
							Type:    computev1alpha.InstanceRunning,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceRunningReasonRunning,
							Message: "Instance is running",
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionTrue,
				Reason:             computev1alpha.InstanceReadyReasonRunning,
				Message:            "Instance is ready",
				ObservedGeneration: 1,
			},
		},
		{
			name: "no change when condition already matches",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               computev1alpha.InstanceReady,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceReadyReasonRunning,
							Message:            "Instance is ready",
							ObservedGeneration: 1,
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: "Instance has been programmed",
						},
						{
							Type:    computev1alpha.InstanceRunning,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceRunningReasonRunning,
							Message: "Instance is running",
						},
					},
				},
			},
			expectedChanged: false,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionTrue,
				Reason:             computev1alpha.InstanceReadyReasonRunning,
				Message:            "Instance is ready",
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
					Name:       "test-instance",
					Namespace:  "default",
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
							Message:            "Instance has been programmed",
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:               computev1alpha.InstanceRunning,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceRunningReasonRunning,
							Message:            "Instance is running",
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
					Name:       "test-instance",
					Namespace:  "default",
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
							Message:            "Instance has been programmed",
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:               computev1alpha.InstanceRunning,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceRunningReasonRunning,
							Message:            "Instance is running",
							LastTransitionTime: metav1.Now(),
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:    computev1alpha.InstanceReady,
				Status:  metav1.ConditionTrue,
				Reason:  computev1alpha.InstanceReadyReasonRunning,
				Message: "Instance is ready",
			},
		},
		{
			name: "quota pending unknown does not block ready condition",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
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
				Message: "Instance has not been programmed",
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

	claimName := namespace + "--" + instanceName

	const deploymentName = "my-deployment"

	// makeDeployment builds a WorkloadDeployment that owns the test instance.
	makeDeployment := func() *computev1alpha.WorkloadDeployment {
		return &computev1alpha.WorkloadDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: namespace,
				UID:       "test-uid",
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
				Namespace:  namespace,
				Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "compute.datumapis.com/v1alpha",
						Kind:       "WorkloadDeployment",
						Name:       deploymentName,
						UID:        "test-uid",
						Controller: func() *bool { b := true; return &b }(),
					},
				},
			},
			Spec: computev1alpha.InstanceSpec{
				Controller: &computev1alpha.InstanceController{
					SchedulingGates: gates,
				},
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Resources: computev1alpha.InstanceRuntimeResources{InstanceType: "d1-standard-2"},
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
					APIGroup: "resourcemanager.miloapis.com",
					Kind:     "Project",
					Name:     clusterName,
				},
				ResourceRef: quotav1alpha1.UnversionedObjectReference{
					APIGroup:  "compute.datumapis.com",
					Kind:      "Instance",
					Name:      instanceName,
					Namespace: namespace,
				},
				Requests: []quotav1alpha1.ResourceRequest{
					{ResourceType: "compute.datumapis.com/instances", Amount: 1},
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
			mgr:             mgr,
			scheme:          s,
			quotaClientManager: qm,
			edgeClusterName: "test-edge",
			// Milo mode: project ID == ClusterName.
			projectIDForInstance: func(cn multicluster.ClusterName, _ *computev1alpha.Instance) string {
				return string(cn)
			},
		}

		// Initialize the finalizer registry so that r.finalizers.Finalize is not
		// a nil-pointer dereference. SetupWithManager does this in production; in
		// tests we replicate the same steps manually.
		r.finalizers = finalizer.NewFinalizers()
		require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

		return r, projectClient, quotaClient
	}

	t.Run("quota granted flow: claim granted removes gate and sets QuotaGranted=True", func(t *testing.T) {
		s := newTestScheme(t)
		instance := makeInstance(s,
			computev1alpha.SchedulingGate{Name: instancecontrol.NetworkSchedulingGate.String()},
			computev1alpha.SchedulingGate{Name: instancecontrol.QuotaSchedulingGate.String()},
		)
		claim := makeClaim(s, metav1.ConditionTrue, quotav1alpha1.ResourceClaimGrantedReason)

		r, projectClient, _ := newReconciler(t, []client.Object{instance, makeDeployment()}, []client.Object{claim})

		// First reconcile: sets QuotaGranted=True in status, returns early.
		_, err := r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated))

		quotaCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, quotaCond)
		assert.Equal(t, metav1.ConditionTrue, quotaCond.Status)
		assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable, quotaCond.Reason)

		// Second reconcile: status is already set, so removes the scheduling gate.
		_, err = r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &updated))

		hasQuotaGate := false
		for _, g := range updated.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.QuotaSchedulingGate.String() {
				hasQuotaGate = true
			}
		}
		assert.False(t, hasQuotaGate, "QuotaSchedulingGate should have been removed")
	})

	t.Run("quota exceeded flow: conditions cascade to block Programmed/Running/Ready", func(t *testing.T) {
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

		runningCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceRunning)
		require.NotNil(t, runningCond)
		assert.Equal(t, metav1.ConditionFalse, runningCond.Status)
		assert.Equal(t, computev1alpha.InstanceProgrammedReasonPendingQuota, runningCond.Reason)

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

		// Second reconcile should see granted claim and update status.
		_, err = r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		var recovered computev1alpha.Instance
		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &recovered))
		quotaCond = apimeta.FindStatusCondition(recovered.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		require.NotNil(t, quotaCond)
		assert.Equal(t, metav1.ConditionTrue, quotaCond.Status)

		// Third reconcile removes the gate (status is already true, no more status write needed).
		_, err = r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: instanceName}}, ClusterName: clusterName})
		require.NoError(t, err)

		require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: instanceName}, &recovered))
		hasQuotaGate := false
		for _, g := range recovered.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.QuotaSchedulingGate.String() {
				hasQuotaGate = true
			}
		}
		assert.False(t, hasQuotaGate, "QuotaSchedulingGate should have been removed after quota granted")
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
					Resources: computev1alpha.InstanceRuntimeResources{InstanceType: "d1-standard-2"},
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

// TestReconcileQuotaSingleMode verifies that in single-cell mode the project ID
// is taken from instance.Namespace rather than the (always-"single") ClusterName.
func TestReconcileQuotaSingleMode(t *testing.T) {
	const (
		instanceName  = "my-instance"
		projectNS     = "ns-abc123" // the Milo project namespace propagated by Karmada
		deploymentName = "my-deployment"
	)

	claimName := projectNS + "--" + instanceName

	s := newTestScheme(t)

	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       instanceName,
			Namespace:  projectNS,
			Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "compute.datumapis.com/v1alpha",
					Kind:       "WorkloadDeployment",
					Name:       deploymentName,
					UID:        "test-uid",
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
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: "d1-standard-2"},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
		},
	}

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: projectNS, UID: "test-uid"},
	}

	// ResourceClaim keyed under the project namespace (the quota-side project ID
	// in single mode).
	claim := &quotav1alpha1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: projectNS},
		Spec: quotav1alpha1.ResourceClaimSpec{
			ConsumerRef: quotav1alpha1.ConsumerRef{
				APIGroup: "resourcemanager.miloapis.com",
				Kind:     "Project",
				Name:     projectNS,
			},
			ResourceRef: quotav1alpha1.UnversionedObjectReference{
				APIGroup:  "compute.datumapis.com",
				Kind:      "Instance",
				Name:      instanceName,
				Namespace: projectNS,
			},
			Requests: []quotav1alpha1.ResourceRequest{
				{ResourceType: "compute.datumapis.com/instances", Amount: 1},
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

	// The quota client is keyed by the project namespace, not "single".
	quotaClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(claim).
		WithStatusSubresource(&quotav1alpha1.ResourceClaim{}).
		Build()

	qm := quota.New(nil)
	qm.StoreClient(projectNS, quotaClient)

	mgr := &fakeMCManager{
		clusters: map[string]cluster.Cluster{
			"single": newFakeCluster(projectClient),
		},
	}

	r := &InstanceReconciler{
		mgr:             mgr,
		scheme:          s,
		quotaClientManager: qm,
		// "single" matches what initializeClusterDiscovery sets in ProviderSingle
		// mode (defaults to "single" when ClusterName is not configured).
		edgeClusterName: "single",
		// Single-cell mode: project ID comes from instance.Namespace.
		projectIDForInstance: func(_ multicluster.ClusterName, inst *computev1alpha.Instance) string {
			return inst.Namespace
		},
		// Single-cell mode: watch map func must always return "single".
		clusterNameForProject: func(_ string) multicluster.ClusterName {
			return "single"
		},
	}

	r.finalizers = finalizer.NewFinalizers()
	require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))

	req := mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: projectNS, Name: instanceName}},
		ClusterName: "single",
	}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{Namespace: projectNS, Name: instanceName}, &updated))

	quotaCond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	require.NotNil(t, quotaCond, "QuotaGranted condition must be set")
	assert.Equal(t, metav1.ConditionTrue, quotaCond.Status, "quota should be granted in single mode")
	assert.Equal(t, computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable, quotaCond.Reason)

	// Verify clusterNameForProject always returns "single" so the watch map func
	// never enqueues an unknown cluster name.
	assert.Equal(t, multicluster.ClusterName("single"), r.resolveClusterNameForProject(projectNS))
	assert.Equal(t, multicluster.ClusterName("single"), r.resolveClusterNameForProject("any-other-project"))
}
