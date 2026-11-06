// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

// settleRoutineErrors turns outcomes that are routine on a busy control plane
// into a timed retry, so the reconcile error count reports only failures.
// A write conflict and a wait on removal both resolve by trying again.
func settleRoutineErrors(ctx context.Context, result ctrl.Result, err error) (ctrl.Result, error) {
	if err == nil {
		return result, nil
	}
	logger := log.FromContext(ctx)
	switch {
	case everyError(err, isRemovalPending):
		logger.V(1).Info("waiting for removal; retrying", "reason", err.Error())
		return ctrl.Result{RequeueAfter: removalPollInterval}, nil
	case everyError(err, apierrors.IsConflict):
		logger.V(1).Info("write lost to a newer version; retrying", "reason", err.Error())
		return ctrl.Result{RequeueAfter: conflictRetryDelay}, nil
	}
	return result, err
}

func isRemovalPending(err error) bool {
	return errors.Is(err, errRemovalPending)
}

// everyError reports whether match holds for every error err combines, so a
// routine error never hides a real one aggregated alongside it.
func everyError(err error, match func(error) bool) bool {
	var errs []error
	var agg kerrors.Aggregate
	var joined interface{ Unwrap() []error }
	switch {
	case errors.As(err, &agg):
		errs = agg.Errors()
	case errors.As(err, &joined):
		errs = joined.Unwrap()
	default:
		return match(err)
	}
	for _, e := range errs {
		if !everyError(e, match) {
			return false
		}
	}
	return len(errs) > 0
}
