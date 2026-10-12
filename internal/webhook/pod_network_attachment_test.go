// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.datum.net/compute/internal/controller"
)

func TestPodNetworkAdmission(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	h := &PodNetworkAttachmentHandler{Decoder: admission.NewDecoder(scheme)}
	for _, tc := range []struct {
		name    string
		pod     corev1.Pod
		allowed bool
		patched bool
	}{
		{"ordinary", corev1.Pod{}, true, false},
		{"opted in", corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{controller.PodPrimaryNetworkEnabledLabel: controller.PodPrimaryNetworkEnabledValue}, Annotations: map[string]string{controller.PodPrimaryNetworkAnnotation: "galactic"}}}, true, true},
		{"missing network", corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{controller.PodPrimaryNetworkEnabledLabel: controller.PodPrimaryNetworkEnabledValue}}}, false, false},
		{"host network", corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{controller.PodPrimaryNetworkEnabledLabel: controller.PodPrimaryNetworkEnabledValue}, Annotations: map[string]string{controller.PodPrimaryNetworkAnnotation: "galactic"}}, Spec: corev1.PodSpec{HostNetwork: true}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(tc.pod)
			resp := h.Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Object: runtime.RawExtension{Raw: data}}})
			if resp.Allowed != tc.allowed {
				t.Fatalf("allowed=%v result=%+v", resp.Allowed, resp.Result)
			}
			if (len(resp.Patches) > 0) != tc.patched {
				t.Fatalf("patches=%+v", resp.Patches)
			}
		})
	}
}
