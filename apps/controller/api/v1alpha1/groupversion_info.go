// Package v1alpha1 contains the Worm API.
// +kubebuilder:object:generate=true
// +groupName=lab.experiments.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "lab.experiments.io", Version: "v1alpha1"}

var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &Worm{}, &WormList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
