package instanceruntime

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"go.datum.net/compute/api/v1alpha"
)

func TestPodRevisionMigrationAndReplacement(t *testing.T) {
	instance := &v1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{UID: "instance"},
		Spec:       v1alpha.InstanceSpec{Controller: &v1alpha.InstanceController{TemplateHash: "old"}},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		UID: "pod-old",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: v1alpha.GroupVersion.String(), Kind: "Instance", UID: instance.UID, Controller: ptr.To(true),
		}},
	}}
	current, err := PodCurrent(instance, pod)
	require.NoError(t, err)
	require.True(t, current)
	// A hash-format migration leaves the runtime alone.
	instance.Spec.Controller.TemplateHash = "new-format"
	current, err = PodCurrent(instance, pod)
	require.NoError(t, err)
	require.True(t, current)
	instance.Spec.Controller.RuntimeRevision = 1
	current, err = PodCurrent(instance, pod)
	require.NoError(t, err)
	require.False(t, current)
	require.Error(t, StampNewPod(instance, pod))
	replacement := pod.DeepCopy()
	replacement.UID = ""
	require.NoError(t, StampNewPod(instance, replacement))
	replacement.UID = "pod-new"
	current, err = PodCurrent(instance, replacement)
	require.NoError(t, err)
	require.True(t, current)
	now := metav1.Now()
	replacement.DeletionTimestamp = &now
	current, err = PodCurrent(instance, replacement)
	require.NoError(t, err)
	require.False(t, current)
}

func TestPodRevisionRejectsForeignAndMalformedPods(t *testing.T) {
	instance := &v1alpha.Instance{ObjectMeta: metav1.ObjectMeta{UID: "instance"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: v1alpha.GroupVersion.String(), Kind: "Instance", UID: "previous", Controller: ptr.To(true),
		}},
	}}
	_, err := PodRevision(instance, pod)
	require.Error(t, err)
	pod.OwnerReferences[0].UID = instance.UID
	for _, value := range []string{"-1", "invalid", "9223372036854775808"} {
		pod.Annotations = map[string]string{PodRevisionAnnotation: value}
		_, err = PodRevision(instance, pod)
		require.Error(t, err)
	}
}
