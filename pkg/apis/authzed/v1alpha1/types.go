package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen crd object rbac:roleName=authzed-operator-role paths="../../../apis/..." output:crd:artifacts:config=../../../../config/crds output:rbac:artifacts:config=../../../../config/rbac

// AuthzedEnterpriseCluster defines a full Authzed Enterprise cluster
//
// +crd
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type AuthzedEnterpriseCluster struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterStatus `json:"status,omitempty"`
}

// ClusterSpec holds the desired state of the Stack.
type ClusterSpec struct {
	// Config values to be passed to the cluster
	// +optional
	// TODO: map[string]interface?
	Config map[string]string `json:"config,omitempty"`

	// SecretName points to a secret (in the same namespace) that holds secret
	// config for the cluster like passwords, credentials, etc.
	// If the secret is omitted, one will be generated
	// +optional
	SecretRef string `json:"secretName,omitempty"`
}

// ClusterStatus communicates the observed state of the Authzed Stack.
type ClusterStatus struct {
	// ObservedGeneration represents the .metadata.generation that has been
	// seen by the controller.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,3,opt,name=observedGeneration"`

	// SecretHash is a digest of the last applied secret
	SecretHash string `json:"secretHash,omitempty"`

	// Conditions for the current state of the Stack.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// AuthzedEnterpriseClusterList is a list of Stack resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type AuthzedEnterpriseClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []AuthzedEnterpriseCluster `json:"items"`
}
