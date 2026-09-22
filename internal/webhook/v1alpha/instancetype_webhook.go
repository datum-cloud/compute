package webhook

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/validation"
)

// SetupInstanceTypeWebhookWithManager sets up the webhook with the Manager.
// catalog reads the cluster holding the instance type catalog, which is not the
// cluster mgr runs against when discovery goes through Milo.
func SetupInstanceTypeWebhookWithManager(
	mgr ctrl.Manager, catalog client.Reader, deprecationGracePeriod time.Duration,
) error {
	return ctrl.NewWebhookManagedBy(mgr, &computev1alpha.InstanceType{}).
		WithValidator(&instanceTypeValidator{
			client:                 catalog,
			deprecationGracePeriod: deprecationGracePeriod,
		}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-compute-datumapis-com-v1alpha-instancetype,mutating=false,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=instancetypes,verbs=create;update;delete,versions=v1alpha,name=vinstancetype.kb.io,admissionReviewVersions=v1

type instanceTypeValidator struct {
	// client reads the control plane so validation can confirm that the
	// instance type a lifecycle references actually exists.
	client client.Reader

	// deprecationGracePeriod is the minimum time a type must remain Deprecated
	// before it can be Disabled.
	deprecationGracePeriod time.Duration
}

var _ admission.Validator[*computev1alpha.InstanceType] = &instanceTypeValidator{}

// validationOptions builds the cross-object validation options for the request
// being admitted.
func (v *instanceTypeValidator) validationOptions(ctx context.Context) (validation.InstanceTypeValidationOptions, error) {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return validation.InstanceTypeValidationOptions{}, err
	}
	return validation.InstanceTypeValidationOptions{
		Client:                 v.client,
		AdmissionRequest:       req,
		Context:                ctx,
		DeprecationGracePeriod: v.deprecationGracePeriod,
	}, nil
}

// ValidateCreate implements admission.Validator
func (v *instanceTypeValidator) ValidateCreate(ctx context.Context, instanceType *computev1alpha.InstanceType) (admission.Warnings, error) {
	opts, err := v.validationOptions(ctx)
	if err != nil {
		return nil, err
	}
	if errs := validation.ValidateInstanceTypeCreate(instanceType, opts); len(errs) > 0 {
		return nil, apierrors.NewInvalid(instanceType.GroupVersionKind().GroupKind(), instanceType.Name, errs)
	}
	return nil, nil
}

// ValidateUpdate implements admission.Validator
func (v *instanceTypeValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *computev1alpha.InstanceType) (admission.Warnings, error) {
	opts, err := v.validationOptions(ctx)
	if err != nil {
		return nil, err
	}
	if errs := validation.ValidateInstanceTypeUpdate(newObj, oldObj, opts); len(errs) > 0 {
		return nil, apierrors.NewInvalid(newObj.GroupVersionKind().GroupKind(), newObj.Name, errs)
	}
	return nil, nil
}

// ValidateDelete implements admission.Validator
func (v *instanceTypeValidator) ValidateDelete(ctx context.Context, obj *computev1alpha.InstanceType) (admission.Warnings, error) {
	opts, err := v.validationOptions(ctx)
	if err != nil {
		return nil, err
	}
	if errs := validation.ValidateInstanceTypeDelete(obj, opts); len(errs) > 0 {
		return nil, apierrors.NewInvalid(obj.GroupVersionKind().GroupKind(), obj.Name, errs)
	}
	return nil, nil
}
