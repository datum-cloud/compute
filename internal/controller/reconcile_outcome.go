// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// errRemovalPending marks a reconcile that is waiting for an object to finish
// deleting where no watch reports that it has gone.
var errRemovalPending = errors.New("removal pending")

// removalPollInterval is how often a reconcile held by errRemovalPending
// rechecks. The error backoff grows to minutes and would hold deletion that
// long.
const removalPollInterval = 5 * time.Second

// conflictRetryDelay is how soon a reconcile that lost a write to a newer
// version of the object retries against that version.
const conflictRetryDelay = time.Second

const (
	retryReasonConflict       = "conflict"
	retryReasonRemovalPending = "removal_pending"
)

// routineRetries counts the reconciles settleRoutineErrors retried instead of
// failing, so a wait that never ends stays visible outside the error rate.
var routineRetries = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "reconcile_routine_retries_total",
		Help:      "Reconciles retried on a timer instead of failing, by controller and reason (conflict, removal_pending).",
	},
	[]string{"controller", "reason"},
)

func init() {
	ctrlmetrics.Registry.MustRegister(routineRetries)
}

// settleRoutineErrors turns outcomes that are routine on a busy control plane
// into a timed retry, so the reconcile error count reports only failures.
// A write conflict and a wait on removal both resolve by trying again. An
// error that mixes the two is retried on the slower removal poll.
func settleRoutineErrors(ctx context.Context, controller string, result ctrl.Result, err error) (ctrl.Result, error) {
	if err == nil {
		return result, nil
	}
	logger := log.FromContext(ctx)
	switch {
	case everyError(err, apierrors.IsConflict):
		routineRetries.WithLabelValues(controller, retryReasonConflict).Inc()
		logger.V(1).Info("write lost to a newer version; retrying", "reason", err.Error())
		return ctrl.Result{RequeueAfter: conflictRetryDelay}, nil
	case everyError(err, isRoutine):
		routineRetries.WithLabelValues(controller, retryReasonRemovalPending).Inc()
		logger.V(1).Info("waiting for removal; retrying", "reason", err.Error())
		return ctrl.Result{RequeueAfter: removalPollInterval}, nil
	}
	return result, err
}

func isRoutine(err error) bool {
	return errors.Is(err, errRemovalPending) || apierrors.IsConflict(err)
}

// everyError reports whether match holds for every error err combines, so a
// routine error never hides a real one aggregated alongside it. Wrapping is
// followed only down a single chain, and the first combining error found
// splits the walk, so a real error beside a nested aggregate is still seen.
func everyError(err error, match func(error) bool) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		var errs []error
		switch combined := e.(type) {
		case kerrors.Aggregate:
			errs = combined.Errors()
		case interface{ Unwrap() []error }:
			errs = combined.Unwrap()
		default:
			continue
		}
		for _, member := range errs {
			if !everyError(member, match) {
				return false
			}
		}
		return len(errs) > 0
	}
	return match(err)
}
