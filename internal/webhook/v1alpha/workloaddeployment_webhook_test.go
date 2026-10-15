// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	// testResolverUsername is the identity the referenced-data resolver
	// authenticates as in these tests, in the form the API server reports for a
	// service account.
	testResolverUsername = "system:serviceaccount:compute-system:compute-manager"
	testUserUsername     = "user@example.com"

	// testExpectedCompanions matches the value the resolver writes: a JSON
	// array of kind-qualified companion tokens.
	testExpectedCompanions      = `["ConfigMap/app-config","Secret/app-secret"]`
	testExpectedCompanionsAdded = `["ConfigMap/app-config","Secret/app-secret","Secret/registry-creds"]`
)

// newWorkloadDeploymentWebhook builds the validator with the resolver as the
// only permitted annotation writer.
func newWorkloadDeploymentWebhook() *workloadDeploymentWebhook {
	return &workloadDeploymentWebhook{
		resolverUsernames: sets.New(testResolverUsername),
	}
}

// requestAs returns an admission context carrying the given identity, matching
// how the API server populates userInfo on a request.
func requestAs(username string, groups ...string) context.Context {
	return admission.NewContextWithRequest(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UserInfo: authenticationv1.UserInfo{
				Username: username,
				Groups:   groups,
			},
		},
	})
}

// testDeployment mirrors the shape the Workload controller writes: a
// deployment pinned to one location, optionally carrying the resolver's
// expected-companion annotation.
func testDeployment(annotationValue *string) *computev1alpha.WorkloadDeployment {
	wd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-workload-default-dfw",
			Namespace: metav1.NamespaceDefault,
			Labels: map[string]string{
				computev1alpha.WorkloadUIDLabel: "00000000-0000-0000-0000-000000000005",
				computev1alpha.LocationLabel:    "dfw",
			},
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			WorkloadRef: computev1alpha.WorkloadReference{
				Name: "test-workload",
				UID:  "00000000-0000-0000-0000-000000000005",
			},
			PlacementName: testDefaultPlacement,
			LocationRef:   locationsv1alpha1.LocationReference{Name: "dfw"},
		},
	}
	if annotationValue != nil {
		wd.Annotations = map[string]string{
			computev1alpha.ExpectedReferencedDataAnnotation: *annotationValue,
		}
	}
	return wd
}

func ptr(s string) *string { return &s }

func TestWorkloadDeploymentWebhook_CreateWithoutAnnotationAllowed(t *testing.T) {
	t.Parallel()

	_, err := newWorkloadDeploymentWebhook().ValidateCreate(
		requestAs(testUserUsername), testDeployment(nil))

	require.NoError(t, err, "a deployment that names no referenced data is not the webhook's business")
}

func TestWorkloadDeploymentWebhook_CreateWithAnnotationRejectedForUser(t *testing.T) {
	t.Parallel()

	_, err := newWorkloadDeploymentWebhook().ValidateCreate(
		requestAs(testUserUsername), testDeployment(ptr(testExpectedCompanions)))

	require.Error(t, err, "a create carrying the annotation authorizes propagation before the resolver has read anything")
	assert.Contains(t, err.Error(), computev1alpha.ExpectedReferencedDataAnnotation)
	assert.Contains(t, err.Error(), testUserUsername, "the message must name the rejected identity")
}

func TestWorkloadDeploymentWebhook_CreateWithAnnotationAllowedForResolver(t *testing.T) {
	t.Parallel()

	_, err := newWorkloadDeploymentWebhook().ValidateCreate(
		requestAs(testResolverUsername), testDeployment(ptr(testExpectedCompanions)))

	require.NoError(t, err)
}

func TestWorkloadDeploymentWebhook_UpdateUnrelatedFieldAllowed(t *testing.T) {
	t.Parallel()

	oldWD := testDeployment(ptr(testExpectedCompanions))
	newWD := testDeployment(ptr(testExpectedCompanions))
	newWD.Spec.Replicas = func() *int32 { r := int32(3); return &r }()

	_, err := newWorkloadDeploymentWebhook().ValidateUpdate(
		requestAs(testUserUsername), oldWD, newWD)

	require.NoError(t, err, "the guard covers the annotation only; every other field stays writable")
}

func TestWorkloadDeploymentWebhook_UpdateAnnotationRejectedForUser(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		old *string
		new *string
	}{
		"adds the annotation":     {old: nil, new: ptr(testExpectedCompanions)},
		"changes the companions":  {old: ptr(testExpectedCompanions), new: ptr(testExpectedCompanionsAdded)},
		"removes the annotation":  {old: ptr(testExpectedCompanions), new: nil},
		"empties the annotation":  {old: ptr(testExpectedCompanions), new: ptr("")},
		"replaces with empty set": {old: ptr(testExpectedCompanions), new: ptr("[]")},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := newWorkloadDeploymentWebhook().ValidateUpdate(
				requestAs(testUserUsername), testDeployment(tc.old), testDeployment(tc.new))

			require.Error(t, err)
			assert.Contains(t, err.Error(), computev1alpha.ExpectedReferencedDataAnnotation)
		})
	}
}

func TestWorkloadDeploymentWebhook_UpdateAnnotationAllowedForResolver(t *testing.T) {
	t.Parallel()

	_, err := newWorkloadDeploymentWebhook().ValidateUpdate(
		requestAs(testResolverUsername),
		testDeployment(ptr(testExpectedCompanions)),
		testDeployment(ptr(testExpectedCompanionsAdded)))

	require.NoError(t, err, "rejecting the resolver would stall every instance that references data")
}

func TestWorkloadDeploymentWebhook_UpdateAnnotationAllowedForBreakGlassGroup(t *testing.T) {
	t.Parallel()

	_, err := newWorkloadDeploymentWebhook().ValidateUpdate(
		requestAs("kubernetes-admin", breakGlassGroup),
		testDeployment(ptr(testExpectedCompanions)),
		testDeployment(nil))

	require.NoError(t, err, "a cluster administrator can already edit the policies this annotation feeds")
}

func TestWorkloadDeploymentWebhook_DeleteAllowed(t *testing.T) {
	t.Parallel()

	_, err := newWorkloadDeploymentWebhook().ValidateDelete(
		requestAs(testUserUsername), testDeployment(ptr(testExpectedCompanions)))

	require.NoError(t, err, "the resolver's finalizer releases companions on delete")
}

// TestSetupWorkloadDeploymentWebhook_RefusesUnknownResolverIdentity pins the
// fail-fast behaviour. Registering with no permitted writer would reject the
// resolver's own annotation writes, which is a silent, total outage of
// referenced-data delivery, so setup must fail loudly instead.
func TestSetupWorkloadDeploymentWebhook_RefusesUnknownResolverIdentity(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	t.Setenv("SERVICE_ACCOUNT_NAME", "")

	err := SetupWorkloadDeploymentWebhookWithManager(nil, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolverUsernames")
}

func TestInferManagerUsername(t *testing.T) {
	t.Run("builds the service account username from the downward API", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "compute-system")
		t.Setenv("SERVICE_ACCOUNT_NAME", "compute-manager")

		got, ok := inferManagerUsername()

		require.True(t, ok)
		assert.Equal(t, testResolverUsername, got)
	})

	t.Run("reports false when either value is missing", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "compute-system")
		t.Setenv("SERVICE_ACCOUNT_NAME", "")

		_, ok := inferManagerUsername()

		assert.False(t, ok)
	})
}
