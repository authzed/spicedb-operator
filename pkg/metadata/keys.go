package metadata

import (
	"fmt"
	"strings"

	"github.com/authzed/controller-idioms/adopt"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
)

const (
	OwningClusterIndex                 = "owning-cluster"
	OwningClusterDatastoreURIIndex     = "owning-cluster-datastore-uri"
	OwningClusterPresharedKeyIndex     = "owning-cluster-preshared-key"
	OwningClusterMigrationSecretsIndex = "owning-cluster-migration-secrets"

	// CredentialType* are the internal credential-role identifiers. They are
	// used to derive per-role label keys and index names; they are not stored
	// directly on Kubernetes objects.
	CredentialTypeDatastoreURI     = "datastore-uri"     // nolint: gosec
	CredentialTypePresharedKey     = "preshared-key"     // nolint: gosec
	CredentialTypeMigrationSecrets = "migration-secrets" // nolint: gosec

	// Per-role label keys. A secret carries exactly the keys for the roles it
	// serves. Key presence (not value) is what the index functions check, so a
	// shared secret can carry all applicable keys simultaneously.
	CredentialTypeDatastoreURILabelKey     = "authzed.com/credential-type-datastore-uri"     // nolint: gosec
	CredentialTypePresharedKeyLabelKey     = "authzed.com/credential-type-preshared-key"     // nolint: gosec
	CredentialTypeMigrationSecretsLabelKey = "authzed.com/credential-type-migration-secrets" // nolint: gosec

	OperatorManagedLabelKey         = "authzed.com/managed-by"
	OperatorManagedLabelValue       = "operator"
	OwnerLabelKey                   = "authzed.com/cluster"
	OwnerAnnotationKeyPrefix        = "authzed.com.cluster-owner/"
	ComponentLabelKey               = "authzed.com/cluster-component"
	ComponentSpiceDBLabelValue      = "spicedb"
	ComponentMigrationJobLabelValue = "migration-job"
	ComponentServiceAccountLabel    = "spicedb-serviceaccount"
	ComponentRoleLabel              = "spicedb-role"
	ComponentServiceLabel           = "spicedb-service"
	ComponentRoleBindingLabel       = "spicedb-rolebinding"
	ComponentPDBLabel               = "spicedb-pdb"
	SpiceDBMigrationRequirementsKey = "authzed.com/spicedb-migration"
	SpiceDBTargetMigrationKey       = "authzed.com/spicedb-target-migration"
	SpiceDBSecretRequirementsKey    = "authzed.com/spicedb-secret" // nolint: gosec
	SpiceDBConfigKey                = "authzed.com/spicedb-configuration"
	FieldManager                    = "spicedb-operator"

	KubernetesNameLabelKey      = "app.kubernetes.io/name"
	KubernetesInstanceLabelKey  = "app.kubernetes.io/instance"
	KubernetesComponentLabelKey = "app.kubernetes.io/component"
	KubernetesVersionLabelKey   = "app.kubernetes.io/version"
)

var (
	ApplyForceOwned          = metav1.ApplyOptions{FieldManager: FieldManager, Force: true}
	PatchForceOwned          = metav1.PatchOptions{FieldManager: FieldManager, Force: ptr.To(true)}
	ManagedDependentSelector = MustParseSelector(fmt.Sprintf("%s=%s", OperatorManagedLabelKey, OperatorManagedLabelValue))
)

func SelectorForComponent(owner, component string) labels.Selector {
	return labels.SelectorFromSet(LabelsForComponent(owner, component))
}

func LabelsForComponent(owner, component string) map[string]string {
	return map[string]string{
		OwnerLabelKey:           owner,
		ComponentLabelKey:       component,
		OperatorManagedLabelKey: OperatorManagedLabelValue,
	}
}

func GVRMetaNamespaceKeyer(gvr schema.GroupVersionResource, key string) string {
	return fmt.Sprintf("%s.%s.%s::%s", gvr.Resource, gvr.Version, gvr.Group, key)
}

func GVRMetaNamespaceKeyFunc(gvr schema.GroupVersionResource, obj interface{}) (string, error) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return d.Key, nil
	}
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return "", err
	}
	return GVRMetaNamespaceKeyer(gvr, key), nil
}

func SplitGVRMetaNamespaceKey(key string) (gvr *schema.GroupVersionResource, namespace, name string, err error) {
	before, after, ok := strings.Cut(key, "::")
	if !ok {
		err = fmt.Errorf("error parsing key: %s", key)
		return gvr, namespace, name, err
	}
	gvr, _ = schema.ParseResourceArg(before)
	if gvr == nil {
		err = fmt.Errorf("error parsing gvr from key: %s", before)
		return gvr, namespace, name, err
	}
	namespace, name, err = cache.SplitMetaNamespaceKey(after)
	return gvr, namespace, name, err
}

// LabelKeyForCredentialType returns the per-role label key for the given
// credential type, or "" for unknown types (including the legacy empty-string
// SecretRef type).
func LabelKeyForCredentialType(credType string) string {
	switch credType {
	case CredentialTypeDatastoreURI:
		return CredentialTypeDatastoreURILabelKey
	case CredentialTypePresharedKey:
		return CredentialTypePresharedKeyLabelKey
	case CredentialTypeMigrationSecrets:
		return CredentialTypeMigrationSecretsLabelKey
	default:
		return ""
	}
}

// IndexNameForCredentialType maps a credential type to its dedicated index
// name. The empty string (legacy SecretRef) falls back to OwningClusterIndex.
func IndexNameForCredentialType(credType string) string {
	switch credType {
	case CredentialTypeDatastoreURI:
		return OwningClusterDatastoreURIIndex
	case CredentialTypePresharedKey:
		return OwningClusterPresharedKeyIndex
	case CredentialTypeMigrationSecrets:
		return OwningClusterMigrationSecretsIndex
	default:
		return OwningClusterIndex
	}
}

// GetClusterKeyFromMetaForType returns a cache.IndexFunc that indexes objects
// by owning cluster, but only for objects that carry the per-role label key for
// the given credential type. Key presence (not value) is checked, so a single
// secret can carry multiple role labels and appear in multiple indexes without
// any handler treating another role's secret as stale.
func GetClusterKeyFromMetaForType(credentialType string) cache.IndexFunc {
	labelKey := LabelKeyForCredentialType(credentialType)
	return func(in any) ([]string, error) {
		if d, ok := in.(cache.DeletedFinalStateUnknown); ok {
			in = d.Obj
		}
		objMeta, err := meta.Accessor(in)
		if err != nil {
			return nil, err
		}
		// When labelKey is "" (legacy SecretRef / unknown type) skip the type
		// filter and index all owned objects, matching OwningClusterIndex.
		if labelKey != "" {
			if _, ok := objMeta.GetLabels()[labelKey]; !ok {
				return nil, nil
			}
		}
		return adopt.OwnerKeysFromMeta(OwnerAnnotationKeyPrefix)(in)
	}
}

func GetClusterKeyFromMeta(in any) ([]string, error) {
	if d, ok := in.(cache.DeletedFinalStateUnknown); ok {
		in = d.Obj
	}
	objMeta, err := meta.Accessor(in)
	if err != nil {
		return nil, err
	}

	clusterNames, err := adopt.OwnerKeysFromMeta(OwnerAnnotationKeyPrefix)(in)
	if err != nil {
		return nil, err
	}

	objLabels := objMeta.GetLabels()
	if len(objLabels) > 0 {
		clusterName, ok := objLabels[OwnerLabelKey]
		if ok {
			nn := types.NamespacedName{Name: clusterName, Namespace: objMeta.GetNamespace()}
			clusterNames = append(clusterNames, nn.String())
		}
	}

	return clusterNames, nil
}
