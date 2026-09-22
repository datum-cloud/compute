package validation

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// InstanceTypeValidationOptions carries what InstanceType validation needs to
// check references across objects. The Client reads the control plane, and the
// AdmissionRequest lets a check that depends on other objects be skipped for a
// dry-run admission.
type InstanceTypeValidationOptions struct {
	Client           client.Reader
	AdmissionRequest admission.Request
	Context          context.Context

	// DeprecationGracePeriod is the minimum time a type must remain Deprecated
	// before it can be Disabled. A value <= 0 disables the check.
	DeprecationGracePeriod time.Duration
}

// ValidateInstanceTypeCreate validates an InstanceType on creation.
func ValidateInstanceTypeCreate(it *computev1alpha.InstanceType, opts InstanceTypeValidationOptions) field.ErrorList {
	allErrs := field.ErrorList{}
	fldPath := field.NewPath("spec")
	allErrs = append(allErrs, validateInstanceTypeSpec(it.Spec, fldPath, it.Name, opts, true)...)

	// An instance type must be created in Active phase
	if it.Spec.Lifecycle.Phase != computev1alpha.InstanceTypePhaseActive {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("lifecycle", "phase"), it.Spec.Lifecycle.Phase, "new instance types must be created in Active phase"))
	}

	return allErrs
}

// ValidateInstanceTypeUpdate validates an InstanceType on update.
func ValidateInstanceTypeUpdate(newIT, oldIT *computev1alpha.InstanceType, opts InstanceTypeValidationOptions) field.ErrorList {
	allErrs := field.ErrorList{}
	fldPath := field.NewPath("spec")
	// Only re-verify the replacement when the reference changed: a successor that
	// is already stored was validated when it was set, so an unrelated edit must
	// not be rejected because that successor has since retired.
	verifyReplacement := newIT.Spec.Lifecycle.ReplacementInstanceType != oldIT.Spec.Lifecycle.ReplacementInstanceType
	allErrs = append(allErrs, validateInstanceTypeSpec(newIT.Spec, fldPath, newIT.Name, opts, verifyReplacement)...)
	allErrs = append(allErrs, validateInstanceTypeUpdate(newIT, oldIT, fldPath)...)
	allErrs = append(allErrs, validateNoActiveReferrers(newIT, oldIT, fldPath, opts)...)
	allErrs = append(allErrs, validateDeprecationGracePeriod(newIT, oldIT, opts, fldPath)...)
	return allErrs
}

func validateInstanceTypeSpec(spec computev1alpha.InstanceTypeSpec, fldPath *field.Path, name string, opts InstanceTypeValidationOptions, verifyReplacement bool) field.ErrorList {
	var allErrs field.ErrorList

	// Validate CPU > 0
	cpu := spec.Resources.CPU
	if cpu.Sign() <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("resources", "cpu"), cpu.String(), "must be greater than 0"))
	}

	// Validate Memory > 0
	mem := spec.Resources.Memory
	if mem.Sign() <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("resources", "memory"), mem.String(), "must be greater than 0"))
	}

	// A type must name a successor before it retires: the phase may only move to
	// Deprecated or Disabled when a replacement is set. The replacement may be set
	// at any phase, but retiring without one leaves instances with nowhere to go.
	replacement := spec.Lifecycle.ReplacementInstanceType
	replacementPath := fldPath.Child("lifecycle", "replacementInstanceType")
	if replacement == "" && spec.Lifecycle.Phase != computev1alpha.InstanceTypePhaseActive {
		allErrs = append(allErrs, field.Invalid(replacementPath, replacement, "replacementInstanceType must be set when phase is Deprecated or Disabled"))
	}

	// The replacement type must not refer to the type itself, and any other named
	// successor must actually exist and be active.
	if replacement == name {
		allErrs = append(allErrs, field.Invalid(replacementPath, replacement, "cannot be self-referential"))
	} else if verifyReplacement {
		allErrs = append(allErrs, validateReplacementInstanceTypeExists(replacement, replacementPath, opts)...)
	}

	return allErrs
}

// validateReplacementInstanceTypeExists confirms that a named replacement
// instance type exists and is Active, so a deprecated or disabled tier always
// points at a live successor that instances can actually move to. The check is
// skipped on a dry-run admission: within a single dry-run apply the referenced
// type may legitimately be created in the same batch but not yet stored, so
// requiring it here would reject valid manifests.
func validateReplacementInstanceTypeExists(replacement string, fldPath *field.Path, opts InstanceTypeValidationOptions) field.ErrorList {
	if replacement == "" {
		return nil
	}
	if req := opts.AdmissionRequest; req.DryRun != nil && *req.DryRun {
		return nil
	}

	ref := &computev1alpha.InstanceType{}
	if err := opts.Client.Get(opts.Context, client.ObjectKey{Name: replacement}, ref); err != nil {
		if apierrors.IsNotFound(err) {
			return field.ErrorList{field.Invalid(fldPath, replacement, "references a non-existent instance type")}
		}
		return field.ErrorList{field.InternalError(fldPath, err)}
	}

	if ref.Spec.Lifecycle.Phase != computev1alpha.InstanceTypePhaseActive {
		return field.ErrorList{field.Invalid(fldPath, replacement, "replacementInstanceType must be Active")}
	}
	return nil
}

// validateNoActiveReferrers prevents an instance type from leaving Active while
// a live successor still depends on it. Any other InstanceType that names this
// type as its replacement (and is not itself Disabled) would be left with a
// retiring target, so the move out of Active is rejected until those
// dependents have moved on.
func validateNoActiveReferrers(newIT, oldIT *computev1alpha.InstanceType, fldPath *field.Path, opts InstanceTypeValidationOptions) field.ErrorList {
	// Only the move out of Active is restricted; Deprecated -> Disabled is a
	// downstream transition that is already valid to perform.
	if oldIT.Spec.Lifecycle.Phase != computev1alpha.InstanceTypePhaseActive ||
		newIT.Spec.Lifecycle.Phase == oldIT.Spec.Lifecycle.Phase {
		return nil
	}

	referrers, err := nonDisabledReferrers(newIT.Name, opts)
	if err != nil {
		return field.ErrorList{field.InternalError(fldPath.Child("lifecycle", "phase"), err)}
	}
	if len(referrers) == 0 {
		return nil
	}
	return field.ErrorList{field.Forbidden(fldPath.Child("lifecycle", "phase"),
		fmt.Sprintf("cannot be moved out of Active while %s still reference it as a replacement", strings.Join(referrers, ", ")))}
}

// nonDisabledReferrers returns the names of every InstanceType that names the
// given type as its replacement and is not itself Disabled. Such referrers are
// live dependents: they expect the target to remain a valid successor. The
// check is skipped on a dry-run admission: the dependents may legitimately be
// created or disabled within the same dry-run batch.
func nonDisabledReferrers(name string, opts InstanceTypeValidationOptions) ([]string, error) {
	if req := opts.AdmissionRequest; req.DryRun != nil && *req.DryRun {
		return nil, nil
	}

	list := &computev1alpha.InstanceTypeList{}
	if err := opts.Client.List(opts.Context, list); err != nil {
		return nil, err
	}

	var referrers []string
	for i := range list.Items {
		ref := &list.Items[i]
		if ref.Name == name {
			continue
		}
		if ref.Spec.Lifecycle.ReplacementInstanceType != name {
			continue
		}
		if ref.Spec.Lifecycle.Phase == computev1alpha.InstanceTypePhaseDisabled {
			continue
		}
		referrers = append(referrers, ref.Name)
	}
	return referrers, nil
}

// ValidateInstanceTypeDelete validates an InstanceType on deletion: it cannot be
// deleted while a live (non-Disabled) successor still references it as a
// replacement.
func ValidateInstanceTypeDelete(it *computev1alpha.InstanceType, opts InstanceTypeValidationOptions) field.ErrorList {
	referrers, err := nonDisabledReferrers(it.Name, opts)
	if err != nil {
		return field.ErrorList{field.InternalError(field.NewPath("metadata"), err)}
	}

	// TODO: once implementing the instanceType to WorkloadDeployments, validate
	// that an instanceType cannot be deleted if a workloadDeployment still
	// references it.
	if len(referrers) == 0 {
		return nil
	}
	return field.ErrorList{field.Forbidden(field.NewPath("metadata", "name"),
		fmt.Sprintf("cannot be deleted while %s still reference it as a replacement", strings.Join(referrers, ", ")))}
}

// validateDeprecationGracePeriod requires that a type stays Deprecated for at
// least the configured grace period before it can be Disabled, giving customers
// time to migrate off the retiring tier. It only applies to the Deprecated ->
// Disabled transition, and is skipped on a dry-run admission.
func validateDeprecationGracePeriod(newIT, oldIT *computev1alpha.InstanceType, opts InstanceTypeValidationOptions, fldPath *field.Path) field.ErrorList {
	if opts.DeprecationGracePeriod <= 0 {
		return nil
	}
	// If the instance type is not being disabled, we don't need to validate the deprecation grace period.
	if oldIT.Spec.Lifecycle.Phase != computev1alpha.InstanceTypePhaseDeprecated ||
		newIT.Spec.Lifecycle.Phase != computev1alpha.InstanceTypePhaseDisabled {
		return nil
	}
	if req := opts.AdmissionRequest; req.DryRun != nil && *req.DryRun {
		return nil
	}

	phasePath := fldPath.Child("lifecycle", "phase")
	if oldIT.Status.DeprecatedAt == nil {
		return field.ErrorList{field.Forbidden(phasePath, "cannot be disabled: deprecation time is not recorded")}
	}
	if elapsed := time.Since(oldIT.Status.DeprecatedAt.Time); elapsed < opts.DeprecationGracePeriod {
		return field.ErrorList{field.Forbidden(phasePath,
			fmt.Sprintf("cannot be disabled until %s after deprecation", opts.DeprecationGracePeriod))}
	}
	return nil
}

func validateInstanceTypeUpdate(newIT, oldIT *computev1alpha.InstanceType, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// Immutability check for resources. CPU and memory are each immutable, so
	// a change reports which dimension was edited.
	if oldIT.Spec.Resources.CPU.Cmp(newIT.Spec.Resources.CPU) != 0 {
		allErrs = append(allErrs, field.Forbidden(fldPath.Child("resources", "cpu"), "cpu is immutable"))
	}
	if oldIT.Spec.Resources.Memory.Cmp(newIT.Spec.Resources.Memory) != 0 {
		allErrs = append(allErrs, field.Forbidden(fldPath.Child("resources", "memory"), "memory is immutable"))
	}

	// The lifecycle progression must follow Active -> Deprecated -> Disabled; no
	// other transitions are allowed. Phase is CRD-defaulted to Active and
	// validated against an enum, so it is always populated by the time this
	// runs.
	oldPhase := oldIT.Spec.Lifecycle.Phase
	newPhase := newIT.Spec.Lifecycle.Phase

	if oldPhase != newPhase {
		valid := (oldPhase == computev1alpha.InstanceTypePhaseActive && newPhase == computev1alpha.InstanceTypePhaseDeprecated) ||
			(oldPhase == computev1alpha.InstanceTypePhaseDeprecated && newPhase == computev1alpha.InstanceTypePhaseDisabled)
		if !valid {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("lifecycle", "phase"), "lifecycle progression only allows Active -> Deprecated -> Disabled"))
		}
	}

	return allErrs
}
