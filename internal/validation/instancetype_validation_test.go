package validation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	testTypeName = "test-type"
	newTypeName  = "new-type"
)

// newValidationClient builds a client seeded with the given instance types.
func newValidationClient(seed ...*computev1alpha.InstanceType) client.Client {
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

// options returns validation options carrying the client and a non-dry-run
// admission request.
func options(c client.Client) InstanceTypeValidationOptions {
	return InstanceTypeValidationOptions{
		Client:           c,
		AdmissionRequest: admission.Request{},
		Context:          context.Background(),
	}
}

func TestValidateInstanceTypeCreate(t *testing.T) {
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
			name: "create in Deprecated phase rejected",
			instanceType: &computev1alpha.InstanceType{
				ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
				Spec: computev1alpha.InstanceTypeSpec{
					Resources: computev1alpha.InstanceTypeResources{
						CPU:    resource.MustParse("1000m"),
						Memory: resource.MustParse("1Gi"),
					},
					Lifecycle: computev1alpha.InstanceTypeLifecycle{
						Phase: computev1alpha.InstanceTypePhaseDeprecated,
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
						Phase:                   computev1alpha.InstanceTypePhaseActive,
						ReplacementInstanceType: testTypeName,
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateInstanceTypeCreate(tt.instanceType, options(newValidationClient()))
			if tt.wantErr {
				assert.NotEmpty(t, errs)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidateInstanceTypeUpdate(t *testing.T) {
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

	t.Run("valid update", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient(activeType(newTypeName))))
		require.Empty(t, errs)
	})

	t.Run("invalid update - references a non-existent replacement", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = "missing-type"

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient()))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "references a non-existent instance type")
	})

	t.Run("valid update - dry-run skips the existence check", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = "missing-type"

		opts := options(newValidationClient())
		dryRun := true
		opts.AdmissionRequest.DryRun = &dryRun

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, opts)
		require.Empty(t, errs)
	})

	t.Run("invalid update - mutated resources", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Resources.CPU = resource.MustParse("2000m")

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient()))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "cpu is immutable")
	})

	t.Run("invalid update - mutated memory", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Resources.Memory = resource.MustParse("2Gi")

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient()))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "memory is immutable")
	})

	t.Run("invalid update - deprecating without a replacement", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient()))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "replacementInstanceType must be set when phase is Deprecated or Disabled")
	})

	t.Run("invalid update - transition directly from Active to Disabled", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient()))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "lifecycle progression only allows Active -> Deprecated -> Disabled")
	})

	t.Run("invalid update - backwards transition from Deprecated to Active", func(t *testing.T) {
		deprecatedInstanceType := oldInstanceType.DeepCopy()
		deprecatedInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated

		reactivatedInstanceType := deprecatedInstanceType.DeepCopy()
		reactivatedInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseActive

		errs := ValidateInstanceTypeUpdate(reactivatedInstanceType, deprecatedInstanceType, options(newValidationClient()))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "lifecycle progression only allows Active -> Deprecated -> Disabled")
	})

	t.Run("valid update - transition from Deprecated to Disabled", func(t *testing.T) {
		deprecatedInstanceType := oldInstanceType.DeepCopy()
		deprecatedInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		deprecatedInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		disabledInstanceType := deprecatedInstanceType.DeepCopy()
		disabledInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled

		errs := ValidateInstanceTypeUpdate(disabledInstanceType, deprecatedInstanceType, options(newValidationClient(activeType(newTypeName))))
		require.Empty(t, errs)
	})

	t.Run("invalid update - replacement is not active", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = "retiring-type"

		// The referenced successor exists but is Deprecated, not a live target.
		retiring := activeType("retiring-type")
		retiring.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient(retiring)))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "replacementInstanceType must be Active")
	})

	t.Run("invalid update - cannot go back from Disabled to Deprecated", func(t *testing.T) {
		disabledInstanceType := oldInstanceType.DeepCopy()
		disabledInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled

		deprecatedInstanceType := disabledInstanceType.DeepCopy()
		deprecatedInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated

		errs := ValidateInstanceTypeUpdate(deprecatedInstanceType, disabledInstanceType, options(newValidationClient()))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "lifecycle progression only allows Active -> Deprecated -> Disabled")
	})

	t.Run("invalid update - cannot go back from Disabled to Active", func(t *testing.T) {
		disabledInstanceType := oldInstanceType.DeepCopy()
		disabledInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled

		reactivatedInstanceType := disabledInstanceType.DeepCopy()
		reactivatedInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseActive

		errs := ValidateInstanceTypeUpdate(reactivatedInstanceType, disabledInstanceType, options(newValidationClient()))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "lifecycle progression only allows Active -> Deprecated -> Disabled")
	})

	t.Run("valid update - unchanged replacement is not re-validated", func(t *testing.T) {
		deprecatedInstanceType := oldInstanceType.DeepCopy()
		deprecatedInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		deprecatedInstanceType.Spec.Lifecycle.ReplacementInstanceType = "retired-type"

		// An unrelated edit (display name) that keeps the same successor must not
		// be rejected just because that successor has since retired.
		editedInstanceType := deprecatedInstanceType.DeepCopy()
		editedInstanceType.Spec.DisplayName = "renamed"

		errs := ValidateInstanceTypeUpdate(editedInstanceType, deprecatedInstanceType, options(newValidationClient()))
		require.Empty(t, errs)
	})

	t.Run("invalid update - cannot leave Active while a non-disabled type references it", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		// A dependent (still Active) points at test-type as its replacement.
		dependent := activeType("dependent")
		dependent.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseActive
		dependent.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient(activeType(newTypeName), dependent)))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "cannot be moved out of Active while dependent still reference it as a replacement")
	})

	t.Run("invalid update - lists all non-disabled referrers", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		// Two separate dependents point at test-type as their replacement.
		depA := activeType("dependent-a")
		depA.Spec.Lifecycle.ReplacementInstanceType = testTypeName
		depB := activeType("dependent-b")
		depB.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient(activeType(newTypeName), depA, depB)))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "dependent-a, dependent-b")
	})

	t.Run("valid update - can leave Active when only disabled types reference it", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		// The dependent is Disabled, so test-type is free to leave Active.
		disabled := activeType("dependent")
		disabled.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled
		disabled.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, options(newValidationClient(activeType(newTypeName), disabled)))
		require.Empty(t, errs)
	})

	t.Run("valid update - leaving Active is not restricted on dry-run", func(t *testing.T) {
		newInstanceType := oldInstanceType.DeepCopy()
		newInstanceType.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newInstanceType.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		dependent := activeType("dependent")
		dependent.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		opts := options(newValidationClient(activeType(newTypeName), dependent))
		dryRun := true
		opts.AdmissionRequest.DryRun = &dryRun

		errs := ValidateInstanceTypeUpdate(newInstanceType, oldInstanceType, opts)
		require.Empty(t, errs)
	})
}

func TestValidateInstanceTypeDeprecationGracePeriod(t *testing.T) {
	const grace = 60 * 24 * time.Hour

	deprecatedType := &computev1alpha.InstanceType{
		ObjectMeta: metav1.ObjectMeta{Name: testTypeName},
		Spec: computev1alpha.InstanceTypeSpec{
			Resources: computev1alpha.InstanceTypeResources{
				CPU:    resource.MustParse("1000m"),
				Memory: resource.MustParse("1Gi"),
			},
			Lifecycle: computev1alpha.InstanceTypeLifecycle{
				Phase:                   computev1alpha.InstanceTypePhaseDeprecated,
				ReplacementInstanceType: newTypeName,
			},
		},
	}

	toDisabled := func(deprecated *computev1alpha.InstanceType) *computev1alpha.InstanceType {
		disabled := deprecated.DeepCopy()
		disabled.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled
		return disabled
	}

	t.Run("disabled after sufficient time", func(t *testing.T) {
		old := deprecatedType.DeepCopy()
		at := metav1.NewTime(time.Now().Add(-80 * 24 * time.Hour))
		old.Status.DeprecatedAt = &at

		opts := options(newValidationClient(activeType(newTypeName)))
		opts.DeprecationGracePeriod = grace
		errs := ValidateInstanceTypeUpdate(toDisabled(old), old, opts)
		require.Empty(t, errs)
	})

	t.Run("disabled before grace period elapses", func(t *testing.T) {
		old := deprecatedType.DeepCopy()
		at := metav1.NewTime(time.Now().Add(-1 * 24 * time.Hour))
		old.Status.DeprecatedAt = &at

		opts := options(newValidationClient(activeType(newTypeName)))
		opts.DeprecationGracePeriod = grace
		errs := ValidateInstanceTypeUpdate(toDisabled(old), old, opts)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "cannot be disabled until 1440h0m0s after deprecation")
	})

	t.Run("disabled without a recorded deprecation time", func(t *testing.T) {
		opts := options(newValidationClient(activeType(newTypeName)))
		opts.DeprecationGracePeriod = grace
		errs := ValidateInstanceTypeUpdate(toDisabled(deprecatedType), deprecatedType, opts)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "deprecation time is not recorded")
	})

	t.Run("disabled before grace elapses is allowed on dry-run", func(t *testing.T) {
		old := deprecatedType.DeepCopy()
		at := metav1.NewTime(time.Now().Add(-1 * 24 * time.Hour))
		old.Status.DeprecatedAt = &at

		opts := options(newValidationClient(activeType(newTypeName)))
		opts.DeprecationGracePeriod = grace
		dryRun := true
		opts.AdmissionRequest.DryRun = &dryRun
		errs := ValidateInstanceTypeUpdate(toDisabled(old), old, opts)
		require.Empty(t, errs)
	})

	t.Run("grace period disabled when set to zero", func(t *testing.T) {
		old := deprecatedType.DeepCopy()
		at := metav1.NewTime(time.Now().Add(-1 * 24 * time.Hour))
		old.Status.DeprecatedAt = &at

		opts := options(newValidationClient(activeType(newTypeName)))
		opts.DeprecationGracePeriod = 0
		errs := ValidateInstanceTypeUpdate(toDisabled(old), old, opts)
		require.Empty(t, errs)
	})

	t.Run("non-disabling transitions are not gated", func(t *testing.T) {
		// Active -> Deprecated is not the gated transition, even with no
		// deprecation time recorded.
		activeOld := deprecatedType.DeepCopy()
		activeOld.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseActive

		newDeprecated := activeOld.DeepCopy()
		newDeprecated.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDeprecated
		newDeprecated.Spec.Lifecycle.ReplacementInstanceType = newTypeName

		opts := options(newValidationClient(activeType(newTypeName)))
		opts.DeprecationGracePeriod = grace
		errs := ValidateInstanceTypeUpdate(newDeprecated, activeOld, opts)
		require.Empty(t, errs)
	})
}

func TestValidateInstanceTypeDelete(t *testing.T) {
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
		errs := ValidateInstanceTypeDelete(testType, options(newValidationClient()))
		require.Empty(t, errs)
	})

	t.Run("valid delete - only disabled types reference it", func(t *testing.T) {
		disabled := activeType("dependent")
		disabled.Spec.Lifecycle.Phase = computev1alpha.InstanceTypePhaseDisabled
		disabled.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		errs := ValidateInstanceTypeDelete(testType, options(newValidationClient(disabled)))
		require.Empty(t, errs)
	})

	t.Run("invalid delete - a non-disabled type references it", func(t *testing.T) {
		dependent := activeType("dependent")
		dependent.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		errs := ValidateInstanceTypeDelete(testType, options(newValidationClient(dependent)))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "cannot be deleted while dependent still reference it as a replacement")
	})

	t.Run("invalid delete - lists all non-disabled referrers", func(t *testing.T) {
		depA := activeType("dependent-a")
		depA.Spec.Lifecycle.ReplacementInstanceType = testTypeName
		depB := activeType("dependent-b")
		depB.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		errs := ValidateInstanceTypeDelete(testType, options(newValidationClient(depA, depB)))
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "dependent-a, dependent-b")
	})

	t.Run("valid delete - not restricted on dry-run", func(t *testing.T) {
		dependent := activeType("dependent")
		dependent.Spec.Lifecycle.ReplacementInstanceType = testTypeName

		opts := options(newValidationClient(dependent))
		dryRun := true
		opts.AdmissionRequest.DryRun = &dryRun

		errs := ValidateInstanceTypeDelete(testType, opts)
		require.Empty(t, errs)
	})
}

// activeType builds an Active instance type that may be referenced as a
// successor.
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
