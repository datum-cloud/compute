// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// unprogrammedInterface reports the first interface the data plane says it no
// longer carries. Unknown is not reported: it is the state of every interface
// before its sandbox is attached.
func unprogrammedInterface(instance *computev1alpha.Instance) (string, bool) {
	for _, status := range instance.Status.NetworkInterfaces {
		programmed := apimeta.FindStatusCondition(status.Conditions, computev1alpha.InstanceNetworkInterfaceProgrammed)
		if programmed == nil || programmed.Status != metav1.ConditionFalse {
			continue
		}
		detail := programmed.Message
		if detail == "" {
			detail = programmed.Reason
		}
		return fmt.Sprintf("Interface %q is not programmed: %s", status.Name, detail), true
	}
	return "", false
}
