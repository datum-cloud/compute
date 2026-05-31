// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"slices"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// CollectFromTemplate walks an InstanceTemplateSpec and returns a deduplicated,
// deterministically-sorted ReferencedSet of all ConfigMaps and Secrets
// referenced by:
//   - container env.ValueFrom.ConfigMapKeyRef / SecretKeyRef
//   - container envFrom[].configMapRef / secretRef
//   - spec.volumes[].configMap and spec.volumes[].secret
//
// The namespace field on every returned ObjectRef is set to the provided
// namespace (the Workload's namespace). References are always same-namespace.
func CollectFromTemplate(namespace string, template computev1alpha.InstanceTemplateSpec) ReferencedSet {
	seen := make(map[string]struct{})
	var refs []ObjectRef

	add := func(kind, name string) {
		if name == "" {
			return
		}
		key := kind + "/" + name
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		refs = append(refs, ObjectRef{
			Kind:      kind,
			Name:      name,
			Namespace: namespace,
		})
	}

	// Collect from sandbox containers.
	if sb := template.Spec.Runtime.Sandbox; sb != nil {
		for _, c := range sb.Containers {
			// env[].valueFrom
			for _, e := range c.Env {
				if e.ValueFrom == nil {
					continue
				}
				if e.ValueFrom.ConfigMapKeyRef != nil {
					add("ConfigMap", e.ValueFrom.ConfigMapKeyRef.Name)
				}
				if e.ValueFrom.SecretKeyRef != nil {
					add("Secret", e.ValueFrom.SecretKeyRef.Name)
				}
			}

			// envFrom[]
			// Skip entries that have both refs set — validateEnvFrom rejects
			// them, so there is no valid single source to collect from.
			for _, ef := range c.EnvFrom {
				if ef.ConfigMapRef != nil && ef.SecretRef != nil {
					continue
				}
				if ef.ConfigMapRef != nil {
					add("ConfigMap", ef.ConfigMapRef.Name)
				}
				if ef.SecretRef != nil {
					add("Secret", ef.SecretRef.Name)
				}
			}
		}
	}

	// Collect from volumes.
	for _, v := range template.Spec.Volumes {
		if v.ConfigMap != nil {
			add("ConfigMap", v.ConfigMap.Name)
		}
		if v.Secret != nil {
			add("Secret", v.Secret.SecretName)
		}
	}

	// Sort deterministically: kind descending (Secret > ConfigMap) then name
	// ascending within kind. This ordering is stable and matches the companion
	// name sort used by the expected-set annotation.
	slices.SortFunc(refs, func(a, b ObjectRef) int {
		if a.Kind != b.Kind {
			// "Secret" > "ConfigMap" lexicographically — sort ascending so
			// ConfigMap comes first, then Secret.
			if a.Kind < b.Kind {
				return -1
			}
			return 1
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})

	return ReferencedSet(refs)
}

// CollectFromSpec is a convenience wrapper around CollectFromTemplate for
// callers (e.g. the admission webhook validator) that already hold a bare
// InstanceSpec rather than the full InstanceTemplateSpec.
func CollectFromSpec(namespace string, spec computev1alpha.InstanceSpec) ReferencedSet {
	return CollectFromTemplate(namespace, computev1alpha.InstanceTemplateSpec{Spec: spec})
}

// TemplateReferencesData returns true if the template references at least one
// ConfigMap or Secret. Used by the stateful instance controller to decide
// whether to stamp the ReferencedData scheduling gate.
func TemplateReferencesData(template computev1alpha.InstanceTemplateSpec) bool {
	return len(CollectFromTemplate("", template)) > 0
}
