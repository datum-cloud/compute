// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"fmt"
	"os"
	"slices"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// breakGlassGroup may write the expected-referenced-data annotation regardless
// of the configured resolver identities. A cluster administrator can already
// edit the propagation policies this annotation feeds, so rejecting the group
// would remove a repair path without removing the capability.
const breakGlassGroup = "system:masters"

// SetupWorkloadDeploymentWebhookWithManager registers the WorkloadDeployment
// validating webhook, which guards the expected-referenced-data annotation.
//
// resolverUsernames are the authenticated users permitted to write the
// annotation. An empty slice falls back to the manager's own service account,
// which is the identity the resolver runs under in every overlay that deploys
// this webhook.
func SetupWorkloadDeploymentWebhookWithManager(mgr mcmanager.Manager, resolverUsernames []string) error {
	allowed := resolverUsernames
	if len(allowed) == 0 {
		inferred, ok := inferManagerUsername()
		if !ok {
			// Registering with an empty set would reject the resolver's own
			// writes and stall every instance that references data, so this
			// fails at startup instead of at the first admission request.
			return fmt.Errorf(
				"cannot determine the referenced-data resolver identity: set referencedData.resolverUsernames, " +
					"or run with POD_NAMESPACE and SERVICE_ACCOUNT_NAME set from the downward API",
			)
		}
		allowed = []string{inferred}
	}

	webhook := &workloadDeploymentWebhook{
		resolverUsernames: sets.New(allowed...),
	}

	return ctrl.NewWebhookManagedBy(mgr.GetLocalManager(), &computev1alpha.WorkloadDeployment{}).
		WithValidator(webhook).
		Complete()
}

// inferManagerUsername builds the service account username the manager runs
// under from the environment the Deployment projects into the pod. It reports
// false when either value is absent, which is the case outside a cluster.
func inferManagerUsername() (string, bool) {
	namespace := os.Getenv("POD_NAMESPACE")
	serviceAccount := os.Getenv("SERVICE_ACCOUNT_NAME")
	if namespace == "" || serviceAccount == "" {
		return "", false
	}
	return fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccount), true
}

// +kubebuilder:webhook:path=/validate-compute-datumapis-com-v1alpha-workloaddeployment,mutating=false,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=workloaddeployments,verbs=create;update,versions=v1alpha,name=vworkloaddeployment.kb.io,admissionReviewVersions=v1

// workloadDeploymentWebhook keeps the expected-referenced-data annotation under
// the resolver's sole authority.
//
// Under dependency propagation the annotation decides what the federation
// engine ships to cells, so naming an object there is what causes it to leave
// the management plane. The resolver only names objects the requesting user was
// authorized to read, checked by the Workload webhook's access review; a write
// from anywhere else would bypass that check and deliver arbitrary project data
// to a shared edge tier.
//
// This webhook is registered on project control planes only. The cell overlay
// disables the webhook server, and the federation hub never serves it, so
// neither the federator's mirrored copy nor Karmada's propagated copy is
// subject to it — which is deliberate, because those writers are not the
// resolver and would otherwise be rejected.
type workloadDeploymentWebhook struct {
	resolverUsernames sets.Set[string]
}

var _ admission.Validator[*computev1alpha.WorkloadDeployment] = &workloadDeploymentWebhook{}

func (r *workloadDeploymentWebhook) ValidateCreate(ctx context.Context, deployment *computev1alpha.WorkloadDeployment) (admission.Warnings, error) {
	_, present := deployment.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	if !present {
		return nil, nil
	}

	// A create that already carries the annotation authorizes propagation
	// before the resolver has read anything, so it is held to the same
	// authority as an update.
	if err := r.authorizeAnnotationWrite(ctx); err != nil {
		return nil, r.invalid(deployment, err)
	}
	return nil, nil
}

func (r *workloadDeploymentWebhook) ValidateUpdate(ctx context.Context, oldDeployment *computev1alpha.WorkloadDeployment, newDeployment *computev1alpha.WorkloadDeployment) (admission.Warnings, error) {
	oldValue, oldPresent := oldDeployment.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	newValue, newPresent := newDeployment.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]

	// Removal is guarded alongside addition and modification. A stale
	// declaration keeps delivering data the workload no longer references, and
	// a premature removal strips data a running instance still needs, so only
	// the resolver decides when it goes away.
	if oldPresent == newPresent && oldValue == newValue {
		return nil, nil
	}

	if err := r.authorizeAnnotationWrite(ctx); err != nil {
		return nil, r.invalid(newDeployment, err)
	}
	return nil, nil
}

func (r *workloadDeploymentWebhook) ValidateDelete(_ context.Context, _ *computev1alpha.WorkloadDeployment) (admission.Warnings, error) {
	// Deleting the deployment releases its companions through the resolver's
	// finalizer, so there is nothing to guard here.
	return nil, nil
}

// authorizeAnnotationWrite reports whether the requester may write the
// expected-referenced-data annotation.
func (r *workloadDeploymentWebhook) authorizeAnnotationWrite(ctx context.Context) error {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return err
	}

	if r.resolverUsernames.Has(req.UserInfo.Username) {
		return nil
	}
	if slices.Contains(req.UserInfo.Groups, breakGlassGroup) {
		return nil
	}

	// The configured set is not named in the message: it tells the caller
	// nothing they can act on, and the annotation is internal bookkeeping they
	// should not be setting in the first place.
	return fmt.Errorf(
		"annotation %s is managed by the referenced-data resolver and cannot be set by %q; reference ConfigMaps and Secrets from the workload template instead",
		computev1alpha.ExpectedReferencedDataAnnotation, req.UserInfo.Username,
	)
}

func (r *workloadDeploymentWebhook) invalid(deployment *computev1alpha.WorkloadDeployment, err error) error {
	return errors.NewInvalid(
		deployment.GroupVersionKind().GroupKind(),
		deployment.Name,
		field.ErrorList{field.Forbidden(
			field.NewPath("metadata", "annotations").Key(computev1alpha.ExpectedReferencedDataAnnotation),
			err.Error(),
		)},
	)
}
