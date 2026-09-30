// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/internal/validation"
	"go.datum.net/compute/pkg/runtimeclass"
)

// SetupInstanceConsoleSessionWebhookWithManager registers admission for shell
// sessions. It is registered whatever the InstanceConsoleSessions gate says,
// so that a disabled environment refuses sessions with a reason instead of
// storing ones nothing will serve.
func SetupInstanceConsoleSessionWebhookWithManager(mgr mcmanager.Manager) error {
	hook := &instanceConsoleSessionWebhook{reader: projectReader(mgr)}
	return ctrl.NewWebhookManagedBy(mgr.GetLocalManager(), &computev1alpha.InstanceConsoleSession{}).
		WithDefaulter(hook).
		WithValidator(hook).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-compute-datumapis-com-v1alpha-instanceconsolesession,mutating=true,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=instanceconsolesessions,verbs=create,versions=v1alpha,name=minstanceconsolesession.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-compute-datumapis-com-v1alpha-instanceconsolesession,mutating=false,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=instanceconsolesessions,verbs=create,versions=v1alpha,name=vinstanceconsolesession.kb.io,admissionReviewVersions=v1

type instanceConsoleSessionWebhook struct {
	// reader returns a reader for the project control plane the request is
	// admitted into.
	reader func(context.Context) (client.Reader, error)
}

var (
	_ admission.Defaulter[*computev1alpha.InstanceConsoleSession] = &instanceConsoleSessionWebhook{}
	_ admission.Validator[*computev1alpha.InstanceConsoleSession] = &instanceConsoleSessionWebhook{}
)

// Default records the authenticated requester on the session and adds the
// controller's finalizer, so a session deleted before the controller first sees
// it still has its end recorded.
func (w *instanceConsoleSessionWebhook) Default(ctx context.Context, session *computev1alpha.InstanceConsoleSession) error {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return err
	}
	controllerutil.AddFinalizer(session, computev1alpha.InstanceConsoleSessionFinalizer)
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[computev1alpha.InstanceConsoleSessionRequesterAnnotation] = req.UserInfo.Username
	return nil
}

// ValidateCreate implements admission.Validator.
func (w *instanceConsoleSessionWebhook) ValidateCreate(ctx context.Context, session *computev1alpha.InstanceConsoleSession) (admission.Warnings, error) {
	opts := validation.InstanceConsoleSessionValidationOptions{}
	if features.FeatureGate.Enabled(features.InstanceConsoleSessions) {
		var err error
		if opts, err = w.validationOptions(ctx, session); err != nil {
			return nil, err
		}
	}
	if errs := validation.ValidateInstanceConsoleSessionCreate(session, opts); len(errs) > 0 {
		return nil, apierrors.NewInvalid(computev1alpha.GroupVersion.WithKind("InstanceConsoleSession").GroupKind(), session.Name, errs)
	}
	return nil, nil
}

// ValidateUpdate implements admission.Validator. The webhook is not registered
// for updates; the spec is immutable in the schema.
func (w *instanceConsoleSessionWebhook) ValidateUpdate(context.Context, *computev1alpha.InstanceConsoleSession, *computev1alpha.InstanceConsoleSession) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete implements admission.Validator. The webhook is not registered
// for deletes; deleting a session revokes it.
func (w *instanceConsoleSessionWebhook) ValidateDelete(context.Context, *computev1alpha.InstanceConsoleSession) (admission.Warnings, error) {
	return nil, nil
}

// validationOptions reads the instance and runtime class catalog from the API
// server rather than the cache, so a session for an instance created moments
// ago is not refused.
func (w *instanceConsoleSessionWebhook) validationOptions(
	ctx context.Context,
	session *computev1alpha.InstanceConsoleSession,
) (validation.InstanceConsoleSessionValidationOptions, error) {
	reader, err := w.reader(ctx)
	if err != nil {
		return validation.InstanceConsoleSessionValidationOptions{}, err
	}

	opts := validation.InstanceConsoleSessionValidationOptions{}

	var instance computev1alpha.Instance
	key := client.ObjectKey{Namespace: session.Namespace, Name: session.Spec.InstanceRef.Name}
	if err := reader.Get(ctx, key, &instance); err != nil {
		if !apierrors.IsNotFound(err) {
			return opts, fmt.Errorf("failed to read instance %q: %w", key.Name, err)
		}
	} else {
		opts.Instance = &instance
	}

	var classes computev1alpha.RuntimeClassList
	if err := reader.List(ctx, &classes); err != nil {
		return opts, fmt.Errorf("failed to list runtime classes: %w", err)
	}
	opts.RuntimeClasses = runtimeclass.Catalog(classes.Items)

	return opts, nil
}
