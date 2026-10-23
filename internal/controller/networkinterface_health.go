// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const interfaceWithdrawalGrace = 30 * time.Second

// unprogrammedInterface reports the first interface the data plane has stopped
// carrying for at least interfaceWithdrawalGrace, or else how long until one
// that stopped more recently crosses it. Unknown is not reported: it is the
// state of every interface before its sandbox is attached.
func unprogrammedInterface(instance *computev1alpha.Instance, now time.Time) (string, bool, time.Duration) {
	var recheckAfter time.Duration
	for _, status := range instance.Status.NetworkInterfaces {
		programmed := apimeta.FindStatusCondition(status.Conditions, computev1alpha.InstanceNetworkInterfaceProgrammed)
		if programmed == nil || programmed.Status != metav1.ConditionFalse {
			continue
		}
		if remaining := interfaceWithdrawalGrace - now.Sub(programmed.LastTransitionTime.Time); remaining > 0 {
			if recheckAfter == 0 || remaining < recheckAfter {
				recheckAfter = remaining
			}
			continue
		}
		detail := programmed.Message
		if detail == "" {
			detail = programmed.Reason
		}
		return fmt.Sprintf("Interface %q is not programmed: %s", status.Name, detail), true, 0
	}
	return "", false, recheckAfter
}
