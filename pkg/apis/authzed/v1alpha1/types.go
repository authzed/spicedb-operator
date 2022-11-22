package v1alpha1

import (
	"encoding/json"

	"golang.org/x/exp/slices"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen crd object rbac:roleName=spicedb-operator-role paths="../../../apis/..." output:crd:artifacts:config=../../../../config/crds output:rbac:artifacts:config=../../../../config/rbac
//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen crd paths="../../../apis/..." output:crd:artifacts:config=../../../crds

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
// +kubebuilder:resource:categories=authzed
type SpiceDBCluster struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterStatus `json:"status,omitempty"`
}

func (c *SpiceDBCluster) WithAnnotations(entries map[string]string) *SpiceDBCluster {
	annotations := c.ObjectMeta.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	for k, v := range entries {
		annotations[k] = v
	}
	c.ObjectMeta.SetAnnotations(annotations)
	return c
}

// ClusterSpec holds the desired state of the cluster.
type ClusterSpec struct {
	// Version is the name of the version of SpiceDB that will be run.
	// The version is usually a simple version string like `v1.13.0`, but the
	// operator is configured with a data source that tells it what versions
	// are allowed, and they may have other names.
	// If omitted, the newest version in the head of the channel will be used.
	// Note that the `config.image` field will take precedence over
	// version/channel, if it is specified
	Version string `json:"version,omitempty"`

	// Channel is a defined series of updates that operator should follow.
	// The operator is configured with a datasource that configures available
	// channels and update paths.
	// If `version` is not specified, then the operator will keep SpiceDB
	// up-to-date with the current head of the channel.
	// If `version` is specified, then the operator will write available updates
	// in the status.
	Channel string `json:"channel,omitempty"`

	// Config values to be passed to the cluster
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Config json.RawMessage `json:"config,omitempty"`

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

	// TargetMigrationHash is a hash of the desired migration target and config
	TargetMigrationHash string `json:"targetMigrationHash,omitempty"`

	// CurrentMigrationHash is a hash of the currently running migration target and config.
	// If this is equal to TargetMigrationHash (and there are no conditions) then the datastore
	// is fully migrated.
	CurrentMigrationHash string `json:"currentMigrationHash,omitempty"`

	// SecretHash is a digest of the last applied secret
	SecretHash string `json:"secretHash,omitempty"`

	// Image is the image that is or will be used for this cluster
	Image string `json:"image,omitempty"`

	// Migration is the name of the last migration applied
	Migration string `json:"migration,omitempty"`

	// Phase is the currently running phase (used for phased migrations)
	Phase string `json:"phase,omitempty"`

	// CurrentVersion is a description of the currently selected version from
	// the channel, if an update channel is being used.
	CurrentVersion *SpiceDBVersion `json:"version,omitempty"`

	// AvailableVersions is a list of versions that the currently running
	// version can be updated to. Only applies if using an update channel.
	AvailableVersions []SpiceDBVersion `json:"availableVersions,omitempty"`

	// Conditions for the current state of the Stack.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

func (s ClusterStatus) Equals(other ClusterStatus) bool {
	switch {
	case s.ObservedGeneration == other.ObservedGeneration &&
		s.TargetMigrationHash == other.TargetMigrationHash &&
		s.CurrentMigrationHash == other.TargetMigrationHash &&
		s.SecretHash == other.SecretHash &&
		s.Image == other.Image &&
		s.Migration == other.Migration &&
		s.Phase == other.Phase &&
		s.CurrentVersion.Equals(other.CurrentVersion) &&
		slices.Equal(s.AvailableVersions, other.AvailableVersions) &&
		slices.Equal(s.Conditions, other.Conditions):
		return true
	default:
		return false
	}
}

type SpiceDBVersion struct {
	Name        string `json:"name"`
	Channel     string `json:"channel"`
	Description string `json:"description,omitempty"`
}

func (v *SpiceDBVersion) Equals(other *SpiceDBVersion) bool {
	if v == other {
		return true
	}
	if v != nil && other != nil && v.Name == other.Name && v.Channel == other.Channel {
		return true
	}
	return false
}

// SpiceDBClusterList is a list of SpiceDBCluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SpiceDBClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []SpiceDBCluster `json:"items"`
}
