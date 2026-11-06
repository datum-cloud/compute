// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestSettleRoutineErrors(t *testing.T) {
	t.Parallel()

	gr := schema.GroupResource{Group: computev1alpha.GroupVersion.Group, Resource: "workloads"}
	conflict := apierrors.NewConflict(gr, "w", errors.New("the object has been modified"))
	pending := fmt.Errorf("%w: hub copy", errRemovalPending)
	failure := errors.New("boom")

	tests := []struct {
		name       string
		in         ctrl.Result
		err        error
		wantResult ctrl.Result
		wantErr    error
	}{
		{name: "success keeps its result", in: ctrl.Result{RequeueAfter: removalPollInterval * 3}, wantResult: ctrl.Result{RequeueAfter: removalPollInterval * 3}},
		{name: "wrapped conflict retries soon", err: fmt.Errorf("failed updating workload status: %w", conflict), wantResult: ctrl.Result{RequeueAfter: conflictRetryDelay}},
		{name: "removal pending polls", err: fmt.Errorf("failed to finalize: %w", kerrors.NewAggregate([]error{pending})), wantResult: ctrl.Result{RequeueAfter: removalPollInterval}},
		{name: "aggregated conflicts retry soon", err: kerrors.NewAggregate([]error{conflict, conflict}), wantResult: ctrl.Result{RequeueAfter: conflictRetryDelay}},
		{name: "real failure alongside a conflict is reported", err: kerrors.NewAggregate([]error{conflict, failure}), wantErr: failure},
		{name: "real failure joined with a pending removal is reported", err: errors.Join(pending, failure), wantErr: failure},
		{name: "conflict and pending removal poll for removal", err: kerrors.NewAggregate([]error{conflict, pending}), wantResult: ctrl.Result{RequeueAfter: removalPollInterval}},
		{name: "real failure beside a nested aggregate is reported", err: errors.Join(kerrors.NewAggregate([]error{conflict}), failure), wantErr: failure},
		{name: "real failure wrapped beside an aggregate is reported", err: fmt.Errorf("%w; %w", kerrors.NewAggregate([]error{pending}), failure), wantErr: failure},
		{name: "not found is not swallowed", err: apierrors.NewNotFound(gr, "w"), wantErr: apierrors.NewNotFound(gr, "w")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := settleRoutineErrors(context.Background(), "test", tt.in, tt.err)
			if tt.wantErr == nil {
				require.NoError(t, err)
				assert.Equal(t, tt.wantResult, result)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErr.Error())
		})
	}
}

func TestWorkloadDeploymentFederator_FinalizerReleaseRaces(t *testing.T) {
	t.Parallel()

	gr := computev1alpha.GroupVersion.WithResource("workloaddeployments").GroupResource()
	tests := []struct {
		name       string
		updateErr  error
		wantResult ctrl.Result
	}{
		{name: "already deleted", updateErr: apierrors.NewNotFound(gr, testWDName)},
		{name: "stale copy", updateErr: apierrors.NewConflict(gr, testWDName, errors.New("the object has been modified")), wantResult: ctrl.Result{RequeueAfter: conflictRetryDelay}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wd := testWorkloadDeployment(withFinalizer, withDeletionTimestamp)
			wd.Annotations = map[string]string{computev1alpha.FederationNamespaceAnnotation: testKarmadaNSStr}
			projectClient := fake.NewClientBuilder().
				WithScheme(newProjectScheme()).
				WithObjects(wd).
				WithInterceptorFuncs(interceptor.Funcs{
					Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
						return tt.updateErr
					},
				}).
				Build()
			r := newTestFederator(projectClient, newKarmadaFakeClient(testHubNamespace()))

			result, err := r.Reconcile(context.Background(), reconcileRequest())
			require.NoError(t, err)
			assert.Equal(t, tt.wantResult, result)
		})
	}
}
