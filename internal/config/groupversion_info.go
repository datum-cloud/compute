package config

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "apiserver.config.datumapis.com", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &objectSchemeBuilder{}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// objectSchemeBuilder registers API objects against GroupVersion. API packages
// must stay cheap to import, so this mirrors what the deprecated
// controller-runtime scheme.Builder did without depending on controller-runtime.
//
// +kubebuilder:object:generate=false
type objectSchemeBuilder struct {
	runtime.SchemeBuilder
}

// Register adds one or more objects to the builder so they can be added to a scheme.
func (b *objectSchemeBuilder) Register(objects ...runtime.Object) *objectSchemeBuilder {
	b.SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, objects...)
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	return b
}
