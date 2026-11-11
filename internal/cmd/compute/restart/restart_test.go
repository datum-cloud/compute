package restart

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

const (
	testWorkload    = "web"
	testLocation    = "us-east-1"
	testRestartedAt = "2026-01-02T15:04:05Z"
)

func newFixture(t *testing.T) (client.Client, *computev1alpha.Workload) {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(s))

	workload := &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkload,
			Namespace: util.ResourceNamespace,
			UID:       types.UID("workload-uid"),
		},
	}
	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkload + "-default-" + testLocation,
			Namespace: util.ResourceNamespace,
			Labels:    map[string]string{computev1alpha.WorkloadUIDLabel: string(workload.UID)},
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			LocationRef: locationsv1alpha1.LocationReference{Name: testLocation},
		},
	}

	errDeploymentWrite := errors.New("workloaddeployments are not writable by project users")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(workload, deployment).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*computev1alpha.WorkloadDeployment); ok {
					return errDeploymentWrite
				}
				return c.Update(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*computev1alpha.WorkloadDeployment); ok {
					return errDeploymentWrite
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	var stored computev1alpha.Workload
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(workload), &stored))
	return c, &stored
}

func TestRestartLocation_AnnotatesWorkloadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, workload := newFixture(t)

	require.NoError(t, restartLocation(ctx, c, workload, testLocation, testRestartedAt))

	var got computev1alpha.Workload
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(workload), &got))
	assert.Equal(t, testRestartedAt, got.Annotations[computev1alpha.LocationRestartAnnotationPrefix+testLocation])
	assert.NotContains(t, got.Spec.Template.Annotations, computev1alpha.RestartedAtAnnotation)
}

func TestRestartLocation_UnknownLocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, workload := newFixture(t)

	err := restartLocation(ctx, c, workload, "eu-west-1", testRestartedAt)
	require.ErrorContains(t, err, `no deployment found for workload "web" in location "eu-west-1"`)

	var got computev1alpha.Workload
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(workload), &got))
	assert.Empty(t, got.Annotations)
}
