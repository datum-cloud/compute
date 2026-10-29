// Package instanceruntime defines the revision contract between compute and
// runtime providers. A revision identifies a running process, independently of
// the template hash used to compare workload settings.
package instanceruntime

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.datum.net/compute/api/v1alpha"
)

const PodRevisionAnnotation = "compute.datumapis.com/runtime-revision"

func DesiredRevision(instance *v1alpha.Instance) int64 {
	if instance.Spec.Controller == nil {
		return 0
	}
	return instance.Spec.Controller.RuntimeRevision
}

// StampNewPod records what a provider is about to boot. Never copy the desired
// revision onto an existing Pod: doing so would relabel an old runtime as new.
func StampNewPod(instance *v1alpha.Instance, pod *corev1.Pod) error {
	if pod.UID != "" || !pod.CreationTimestamp.IsZero() {
		return fmt.Errorf("cannot stamp a runtime revision on an existing Pod")
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[PodRevisionAnnotation] = strconv.FormatInt(DesiredRevision(instance), 10)
	return nil
}

// PodRevision validates ownership before interpreting the revision. Pods created
// before this contract have revision zero; they must not be adopted as revision
// one when compute requests the first update.
func PodRevision(instance *v1alpha.Instance, pod *corev1.Pod) (int64, error) {
	owner := metav1.GetControllerOf(pod)
	if instance.UID == "" || owner == nil || owner.UID != instance.UID ||
		owner.Kind != "Instance" || owner.APIVersion != v1alpha.GroupVersion.String() {
		return 0, fmt.Errorf("Pod %s/%s does not belong to Instance %s", pod.Namespace, pod.Name, instance.UID)
	}
	value := pod.Annotations[PodRevisionAnnotation]
	if value == "" {
		return 0, nil
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 0 {
		return 0, fmt.Errorf("Pod %s/%s has invalid runtime revision %q", pod.Namespace, pod.Name, value)
	}
	return revision, nil
}

// PodCurrent is false during deletion and until the Pod carries the requested
// runtime revision. Providers must additionally check Pod readiness before
// reporting ObservedRuntimeRevision. A false result is not permission to force
// delete: stop the old runtime, wait for deletion and network detach, then create.
func PodCurrent(instance *v1alpha.Instance, pod *corev1.Pod) (bool, error) {
	revision, err := PodRevision(instance, pod)
	if err != nil {
		return false, err
	}
	return pod.DeletionTimestamp.IsZero() && revision == DesiredRevision(instance), nil
}
