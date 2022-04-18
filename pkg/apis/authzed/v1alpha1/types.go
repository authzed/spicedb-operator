package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen crd object rbac:roleName=spicedb-operator-role paths="../../../apis/..." output:crd:artifacts:config=../../../../config/crds output:rbac:artifacts:config=../../../../config/rbac
//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen crd paths="../../../apis/..." output:crd:artifacts:config=../../../bootstrap/crds

const (
	SpiceDBClusterResourceName = "spicedbclusters"
	SpiceDBClusterKind         = "SpiceDBCluster"
)

// SpiceDBCluster defines all options for a full SpiceDB cluster
//
// +crd
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
type SpiceDBCluster struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterStatus `json:"status,omitempty"`
}

// ClusterSpec holds the desired state of the cluster.
type ClusterSpec struct {
	// Config values to be passed to the cluster
	// +optional
	Config map[string]string `json:"config,omitempty"`

	// SecretName points to a secret (in the same namespace) that holds secret
	// config for the cluster like passwords, credentials, etc.
	// If the secret is omitted, one will be generated
	// +optional
	SecretRef string `json:"secretName,omitempty"`
}

// ClusterStatus communicates the observed state of the cluster.
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

// SpiceDBClusterList is a list of SpiceDBCluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SpiceDBClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []SpiceDBCluster `json:"items"`
}
