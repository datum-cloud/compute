package webhook

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
)

const (
	testTypeName = "test-type"
	newTypeName  = "new-type"
)

// newWebhookClient builds a client seeded with the given instance types.
func newWebhookClient(seed ...*computev1alpha.InstanceType) client.Client {
	scheme := k8sruntime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		panic(err)
	}
	objs := make([]client.Object, 0, len(seed))
	for _, it := range seed {
		objs = append(objs, it)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// instanceTypeTestCase holds a validator and admission context bound to a
// client seeded with the given referenced types.
type instanceTypeTestCase struct {
	v   *instanceTypeValidator
	ctx context.Context
}

// fixedReader stands in for the project control plane a request is admitted
// into, returning c whatever the request.
func fixedReader(c client.Reader) func(context.Context) (client.Reader, error) {
	return func(context.Context) (client.Reader, error) { return c, nil }
}

// newInstanceTypeValidator builds the validator and context for a request
// admitted against a client seeded with the given instance types.
func newInstanceTypeValidator(seed ...*computev1alpha.InstanceType) *instanceTypeTestCase {
	return &instanceTypeTestCase{
		v:   &instanceTypeValidator{reader: fixedReader(newWebhookClient(seed...))},
		ctx: admission.NewContextWithRequest(context.Background(), admission.Request{}),
	}
}

func TestInstanceTypeValidation_Create(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceTypes, true)

	tc := newInstanceTypeValidator()
	v := tc.v
	ctx := tc.ctx

	tests := []struct {
		name         string
		instanceType *computev1alpha.InstanceType
		wantErr      bool
	}{
		{
			name: "valid instance type",
			instanceType: &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("1Gi"),
					},
					Lifecycle: computev1alpha.InstanceTypeLifecycle{
						Phase: computev1alpha.InstanceTypePhaseActive,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid cpu",
			instanceType: &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("0"),
						Memory: resource.MustParse("1Gi"),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid memory",
			instanceType: &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("-1Gi"),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid phase",
			instanceType: &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("1Gi"),
					},
					Lifecycle: computev1alpha.InstanceTypeLifecycle{
						Phase: "InvalidPhase",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "self referential replacement",
			instanceType: &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("1Gi"),
					},
					Lifecycle: computev1alpha.InstanceTypeLifecycle{
						Phase:                   computev1alpha.InstanceTypePhaseDeprecated,
						ReplacementInstanceType: testTypeName,
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := v.ValidateCreate(ctx, tt.instanceType)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestInstanceTypeValidation_Update(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceTypes, true)

	oldInstanceType := &computev1alpha.InstanceType{
		ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
		Spec: computev1alpha.InstanceTypeSpec{
			Resources: computev1alpha.InstanceTypeResources{
				CPU:    resource.MustParse("1000m"),
				Memory: resource.MustParse("1Gi"),
			},
			Lifecycle: computev1alpha.InstanceTypeLifecycle{
				Phase: computev1alpha.InstanceTypePhaseActive,
			},
		},
	}

	// A literal copy of the type referenced as a successor.
	successor := &computev1alpha.InstanceType{
		ObjectMeta: metav1.ObjectMeta{Name: newTypeName},
		Spec: computev1alpha.InstanceTypeSpec{
			Resources: computev1alpha.InstanceTypeResources{
				CPU:    resource.MustParse("1000m"),
				Memory: resource.MustParse("1Gi"),
			},
			Lifecycle: computev1alpha.InstanceTypeLifecycle{
				Phase: computev1alpha.InstanceTypePhaseActive,
			},
		},
	}

	t.Run("valid update", func(t *testing.T) {
		tc := newInstanceTypeValidator(successor)
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		_, err := tc.v.ValidateUpdate(tc.ctx, oldInstanceType, newInstanceType)
		require.NoError(t, err)
	})

	t.Run("invalid update - references a non-existent replacement", func(t *testing.T) {
		tc := newInstanceTypeValidator()
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = "missing-type"

		_, err := tc.v.ValidateUpdate(tc.ctx, oldInstanceType, newInstanceType)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "references a non-existent instance type")
	})

	t.Run("valid update - dry-run skips the existence check", func(t *testing.T) {
		tc := newInstanceTypeValidator()
		dryRun := true
		tc.ctx = admission.NewContextWithRequest(tc.ctx, admission.Request{
			AdmissionRequest: admissionv1.AdmissionRequest{DryRun: &dryRun},
		})
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = "missing-type"

		_, err := tc.v.ValidateUpdate(tc.ctx, oldInstanceType, newInstanceType)
		require.NoError(t, err)
	})

	t.Run("invalid update - mutated resources", func(t *testing.T) {
		tc := newInstanceTypeValidator()
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Resources.CPU = resource.MustParse("2000m")

		_, err := tc.v.ValidateUpdate(tc.ctx, oldInstanceType, newInstanceType)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cpu is immutable")
	})

	t.Run("invalid update - transition directly from Active to Disabled", func(t *testing.T) {
		tc := newInstanceTypeValidator()
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled

		_, err := tc.v.ValidateUpdate(tc.ctx, oldInstanceType, newInstanceType)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lifecycle progression only allows Active -> Deprecated -> Disabled")
	})

	t.Run("valid update - transition from Deprecated to Disabled", func(t *testing.T) {
		tc := newInstanceTypeValidator(successor)
		deprecatedInstanceType := oldInstanceType.DeepCopy()
		deprecatedInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		deprecatedInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		disabledInstanceType := deprecatedInstanceType.DeepCopy()
		disabledInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled

		_, err := tc.v.ValidateUpdate(tc.ctx, deprecatedInstanceType, disabledInstanceType)
		require.NoError(t, err)
	})

	t.Run("valid update - unchanged replacement is not re-validated", func(t *testing.T) {
		tc := newInstanceTypeValidator()
		deprecatedInstanceType := oldInstanceType.DeepCopy()
		deprecatedInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		deprecatedInstanceType.Spec.Lifecycle.ReplacementInstanceType = "retired-type"

		// An unrelated edit (display name) that keeps the same successor must not
		// be rejected just because that successor has since retired.
		editedInstanceType := deprecatedInstanceType.DeepCopy()
		editedInstanceType.Spec.DisplayName = "renamed"

		_, err := tc.v.ValidateUpdate(tc.ctx, deprecatedInstanceType, editedInstanceType)
		require.NoError(t, err)
	})

	t.Run("invalid update - cannot leave Active while a non-disabled type references it", func(t *testing.T) {
		// A dependent (still Active) points at test-type as its replacement.
		dependent := activeType("dependent")
		dependent.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		tc := newInstanceTypeValidator(successor, dependent)
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		_, err := tc.v.ValidateUpdate(tc.ctx, oldInstanceType, newInstanceType)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be moved out of Active while dependent still reference it as a replacement")
	})

	t.Run("invalid update - lists all non-disabled referrers", func(t *testing.T) {
		// Two separate dependents point at test-type as their replacement.
		depA := activeType("dependent-a")
		depA.Spec.Lifecycle.ReplacementInstanceType = testTypeName
		depB := activeType("dependent-b")
		depB.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		tc := newInstanceTypeValidator(successor, depA, depB)
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		_, err := tc.v.ValidateUpdate(tc.ctx, oldInstanceType, newInstanceType)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependent-a, dependent-b")
	})

	t.Run("valid update - can leave Active when only disabled types reference it", func(t *testing.T) {
		// The dependent is Disabled, so test-type is free to leave Active.
		disabled := activeType("dependent")
		disabled.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled
		disabled.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		tc := newInstanceTypeValidator(successor, disabled)
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		_, err := tc.v.ValidateUpdate(tc.ctx, oldInstanceType, newInstanceType)
		require.NoError(t, err)
	})
}

func TestInstanceTypeValidation_Delete(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceTypes, true)

	testType := &computev1alpha.InstanceType{
		ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
		Spec: computev1alpha.InstanceTypeSpec{
			Resources: computev1alpha.InstanceTypeResources{
				CPU:    resource.MustParse("1000m"),
				Memory: resource.MustParse("1Gi"),
			},
			Lifecycle: computev1alpha.InstanceTypeLifecycle{
				Phase: computev1alpha.InstanceTypePhaseActive,
			},
		},
	}

	t.Run("valid delete - no referrers", func(t *testing.T) {
		tc := newInstanceTypeValidator()
		_, err := tc.v.ValidateDelete(tc.ctx, testType)
		require.NoError(t, err)
	})

	t.Run("valid delete - only disabled types reference it", func(t *testing.T) {
		disabled := activeType("dependent")
		disabled.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled
		disabled.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		tc := newInstanceTypeValidator(disabled)
		_, err := tc.v.ValidateDelete(tc.ctx, testType)
		require.NoError(t, err)
	})

	t.Run("invalid delete - a non-disabled type references it", func(t *testing.T) {
		dependent := activeType("dependent")
		dependent.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		tc := newInstanceTypeValidator(dependent)
		_, err := tc.v.ValidateDelete(tc.ctx, testType)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be deleted while dependent still reference it as a replacement")
	})

	t.Run("invalid delete - lists all non-disabled referrers", func(t *testing.T) {
		depA := activeType("dependent-a")
		depA.Spec.Lifecycle.ReplacementInstanceType = testTypeName
		depB := activeType("dependent-b")
		depB.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		tc := newInstanceTypeValidator(depA, depB)
		_, err := tc.v.ValidateDelete(tc.ctx, testType)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependent-a, dependent-b")
	})

	t.Run("valid delete - not restricted on dry-run", func(t *testing.T) {
		dependent := activeType("dependent")
		dependent.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		tc := newInstanceTypeValidator(dependent)
		dryRun := true
		tc.ctx = admission.NewContextWithRequest(tc.ctx, admission.Request{
			AdmissionRequest: admissionv1.AdmissionRequest{DryRun: &dryRun},
		})

		_, err := tc.v.ValidateDelete(tc.ctx, testType)
		require.NoError(t, err)
	})
}

func TestInstanceTypeValidation_DeprecationGracePeriod(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceTypes, true)

	const grace = 60 * 24 * time.Hour

	// disabledType builds a Deprecated type to move to Disabled, with the given
	// deprecation time recorded in status.
	disabledType := func(deprecatedAt time.Time) *computev1alpha.InstanceType {
		old := activeType(testTypeName)
		old.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		old.Spec.Lifecycle.ReplacementInstanceType = newTypeName
		old.Status.DeprecatedAt = &metav1.Time{Time: deprecatedAt}
		return old
	}

	newDisabled := func(old *computev1alpha.InstanceType) *computev1alpha.InstanceType {
		newObj := old.DeepCopy()
		newObj.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled
		return newObj
	}

	dryRun := true

	tests := []struct {
		name    string
		old     *computev1alpha.InstanceType
		grace   time.Duration
		request *admissionv1.AdmissionRequest
		wantErr bool
		wantMsg string
	}{
		{
			name:    "disabled after grace elapsed",
			old:     disabledType(time.Now().Add(-80 * 24 * time.Hour)),
			grace:   grace,
			wantErr: false,
		},
		{
			name:    "disabled before grace elapsed",
			old:     disabledType(time.Now().Add(-1 * 24 * time.Hour)),
			grace:   grace,
			wantErr: true,
			wantMsg: "cannot be disabled until",
		},
		{
			name: "disabled without recorded deprecation time",
			old: func() *computev1alpha.InstanceType {
				o := activeType(testTypeName)
				o.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
				o.Spec.Lifecycle.ReplacementInstanceType = newTypeName
				return o
			}(),
			grace:   grace,
			wantErr: true,
			wantMsg: "deprecation time is not recorded",
		},
		{
			name:    "disabled before grace elapses on dry-run",
			old:     disabledType(time.Now().Add(-1 * 24 * time.Hour)),
			grace:   grace,
			request: &admissionv1.AdmissionRequest{DryRun: &dryRun},
			wantErr: false,
		},
		{
			name:    "disabled with grace disabled (zero)",
			old:     disabledType(time.Now().Add(-1 * 24 * time.Hour)),
			grace:   0,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &instanceTypeValidator{reader: fixedReader(newWebhookClient()), deprecationGracePeriod: tt.grace}
			ctx := admission.NewContextWithRequest(context.Background(), admission.Request{})
			if tt.request != nil {
				ctx = admission.NewContextWithRequest(ctx, admission.Request{AdmissionRequest: *tt.request})
			}
			_, err := v.ValidateUpdate(ctx, tt.old, newDisabled(tt.old))
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// activeType builds an Active instance type that may be referenced as a
// successor.
// TestInstanceTypeValidation_GateOff verifies that the webhook itself, not
// just the validation package it delegates to, rejects every write while the
// InstanceTypes gate is off — exercising the same v.reader(ctx) lookup and
// admission.Request plumbing a real request goes through.
func TestInstanceTypeValidation_GateOff(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceTypes, false)

	tc := newInstanceTypeValidator()
	valid := activeType(testTypeName)

	t.Run("create", func(t *testing.T) {
		_, err := tc.v.ValidateCreate(tc.ctx, valid)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "instance types are not enabled on this control plane")
	})

	t.Run("update", func(t *testing.T) {
		newIT := valid.DeepCopy()
		newIT.Spec.Resources.CPU = resource.MustParse("2000m") // would otherwise fail immutability
		_, err := tc.v.ValidateUpdate(tc.ctx, valid, newIT)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "instance types are not enabled on this control plane")
	})

	t.Run("delete", func(t *testing.T) {
		_, err := tc.v.ValidateDelete(tc.ctx, valid)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "instance types are not enabled on this control plane")
	})
}

func activeType(name string) *computev1alpha.InstanceType {
	return &computev1alpha.InstanceType{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: computev1alpha.InstanceTypeSpec{
			Resources: computev1alpha.InstanceTypeResources{
				CPU:    resource.MustParse("1000m"),
				Memory: resource.MustParse("1Gi"),
			},
			Lifecycle: computev1alpha.InstanceTypeLifecycle{
				Phase: computev1alpha.InstanceTypePhaseActive,
			},
		},
	}
}
