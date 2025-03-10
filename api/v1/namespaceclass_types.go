package v1

import (
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type NamespaceClass struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   NamespaceClassSpec   `json:"spec,omitempty"`
    Status NamespaceClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NamespaceClassList struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ListMeta `json:"metadata,omitempty"`
    Items           []NamespaceClass `json:"items"`
}

type NamespaceClassSpec struct {
    // Resources is a list of raw Kubernetes resource manifests to apply to namespaces.
    // +kubebuilder:validation:Optional
    Resources []runtime.RawExtension `json:"resources,omitempty"`
}

type NamespaceClassStatus struct {
    // Conditions represent the latest observations of the NamespaceClass's state.
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // LastUpdateTime is the last time the NamespaceClass was updated.
    LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty"`

    // ManagedNamespaces lists namespaces using this class.
    ManagedNamespaces []string `json:"managedNamespaces,omitempty"`
}

func init() {
    SchemeBuilder.Register(&NamespaceClass{}, &NamespaceClassList{})
}