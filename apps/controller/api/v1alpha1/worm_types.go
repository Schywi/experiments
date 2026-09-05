package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WormSpec defines the desired bounded reproduction policy.
type WormSpec struct {
	// WorkerDeploymentName is the Deployment whose scale subresource is managed.
	WorkerDeploymentName string `json:"workerDeploymentName"`
	// MaxReplicas is an inclusive, hard upper bound for worker replicas.
	MaxReplicas int32 `json:"maxReplicas"`
}

// WormStatus records accepted replication intents and the desired worker count.
type WormStatus struct {
	DesiredReplicas    int32 `json:"desiredReplicas,omitempty"`
	CurrentReplicas    int32 `json:"currentReplicas,omitempty"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// AcceptedIntentIDs is bounded by the CRD schema and makes retries idempotent.
	AcceptedIntentIDs []string `json:"acceptedIntentIDs,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=worms,scope=Namespaced,shortName=worm
// Worm is the singleton reproduction policy for a worker Deployment.
type Worm struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WormSpec   `json:"spec,omitempty"`
	Status            WormStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WormList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Worm `json:"items"`
}
