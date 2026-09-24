package webhook

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/validation"
	computewebhook "go.datum.net/compute/internal/webhook"
)

// SetupInstanceTypeWebhookWithManager sets up the webhook with the Manager.
func SetupInstanceTypeWebhookWithManager(mgr mcmanager.Manager, deprecationGracePeriod time.Duration) error {
	return ctrl.NewWebhookManagedBy(mgr.GetLocalManager(), &computev1alpha.InstanceType{}).
		WithValidator(&instanceTypeValidator{
			reader:                 projectReader(mgr),
			deprecationGracePeriod: deprecationGracePeriod,
		}).
		Complete()
}

// projectReader returns a reader for the project control plane a request is
// admitted into. It reads the API server directly, so the replacement and
// referrer checks see writes the cache may not have caught up with.
func projectReader(mgr mcmanager.Manager) func(context.Context) (client.Reader, error) {
	return func(ctx context.Context) (client.Reader, error) {
		cl, err := mgr.GetCluster(ctx, multicluster.ClusterName(computewebhook.ClusterNameFromContext(ctx)))
		if err != nil {
			return nil, err
		}
		return cl.GetAPIReader(), nil
	}
}

// +kubebuilder:webhook:path=/validate-compute-datumapis-com-v1alpha-instancetype,mutating=false,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=instancetypes,verbs=create;update;delete,versions=v1alpha,name=vinstancetype.kb.io,admissionReviewVersions=v1

type instanceTypeValidator struct {
	// reader returns a reader for the project control plane the request is
	// admitted into, so validation can check the other instance types there.
	reader func(context.Context) (client.Reader, error)

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
	reader, err := v.reader(ctx)
	if err != nil {
		return validation.InstanceTypeValidationOptions{}, err
	}
	return validation.InstanceTypeValidationOptions{
		Client:                 reader,
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
