// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.datum.net/compute/internal/controller"
)

const PodNetworkWebhookPath = "/mutate-v1-pod-primary-network"

// PodNetworkAttachmentHandler installs the gate only at Pod CREATE admission.
// The webhook configuration's objectSelector limits calls to opted-in Pods.
type PodNetworkAttachmentHandler struct {
	Decoder admission.Decoder
}

func (h *PodNetworkAttachmentHandler) Handle(_ context.Context, req admission.Request) admission.Response {
	var pod corev1.Pod
	if err := h.Decoder.Decode(req, &pod); err != nil {
		return admission.Errored(400, err)
	}
	if pod.Labels[controller.PodPrimaryNetworkEnabledLabel] != controller.PodPrimaryNetworkEnabledValue {
		return admission.Allowed("Pod did not request a primary VPC network")
	}
	if pod.Annotations[controller.PodPrimaryNetworkAnnotation] == "" {
		return admission.Denied("primary VPC network label requires a network name annotation")
	}
	if pod.Spec.HostNetwork {
		return admission.Denied("primary VPC network is incompatible with hostNetwork")
	}
	if pod.Annotations[controller.MultusDefaultNetworkAnnotation] != "" {
		return admission.Denied("primary VPC network Pods cannot specify another Multus default network")
	}
	if !hasSchedulingGate(&pod, controller.PodNetworkSchedulingGate) {
		pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: controller.PodNetworkSchedulingGate})
	}
	data, err := json.Marshal(&pod)
	if err != nil {
		return admission.Errored(500, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, data)
}

func hasSchedulingGate(pod *corev1.Pod, name string) bool {
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == name {
			return true
		}
	}
	return false
}

func NewPodNetworkAttachmentHandler(scheme *runtime.Scheme) *admission.Webhook {
	return &admission.Webhook{Handler: &PodNetworkAttachmentHandler{Decoder: admission.NewDecoder(scheme)}}
}
