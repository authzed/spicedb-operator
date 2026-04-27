package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func TestGetClusterKeyFromMetaForType(t *testing.T) {
	ownerAnnotation := OwnerAnnotationKeyPrefix + "mycluster"
	// secret creates a secret whose label set marks it as carrying the given
	// credential type role. Key presence (not value) is what matters.
	secret := func(credType string) *corev1.Secret {
		labels := map[string]string{}
		if lk := LabelKeyForCredentialType(credType); lk != "" {
			labels[lk] = "true"
		}
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "mysecret",
				Namespace:   "test",
				Labels:      labels,
				Annotations: map[string]string{ownerAnnotation: "owned"},
			},
		}
	}
	secretNoTypeLabel := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "mysecret",
				Namespace:   "test",
				Labels:      map[string]string{},
				Annotations: map[string]string{ownerAnnotation: "owned"},
			},
		}
	}

	tests := []struct {
		name       string
		indexType  string
		obj        any
		expectKeys []string
		expectErr  bool
	}{
		{
			name:       "matching type label returns owner key",
			indexType:  CredentialTypeDatastoreURI,
			obj:        secret(CredentialTypeDatastoreURI),
			expectKeys: []string{"test/mycluster"},
		},
		{
			name:      "non-matching type label returns nil",
			indexType: CredentialTypeDatastoreURI,
			obj:       secret(CredentialTypePresharedKey),
		},
		{
			name:      "missing type label returns nil",
			indexType: CredentialTypeDatastoreURI,
			obj:       secretNoTypeLabel(),
		},
		{
			name:       "tombstone with matching type label returns owner key",
			indexType:  CredentialTypeDatastoreURI,
			obj:        cache.DeletedFinalStateUnknown{Key: "test/mysecret", Obj: secret(CredentialTypeDatastoreURI)},
			expectKeys: []string{"test/mycluster"},
		},
		{
			name:      "non-object tombstone returns error without panic",
			indexType: CredentialTypeDatastoreURI,
			obj:       cache.DeletedFinalStateUnknown{Key: "test/mysecret", Obj: "not-an-object"},
			expectErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := GetClusterKeyFromMetaForType(tt.indexType)(tt.obj)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expectKeys, keys)
		})
	}
}

func TestLabelKeyForCredentialType(t *testing.T) {
	tests := []struct {
		credType  string
		expectKey string
	}{
		{CredentialTypeDatastoreURI, CredentialTypeDatastoreURILabelKey},
		{CredentialTypePresharedKey, CredentialTypePresharedKeyLabelKey},
		{CredentialTypeMigrationSecrets, CredentialTypeMigrationSecretsLabelKey},
		{"", ""},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.credType, func(t *testing.T) {
			require.Equal(t, tt.expectKey, LabelKeyForCredentialType(tt.credType))
		})
	}
}

func TestIndexNameForCredentialType(t *testing.T) {
	tests := []struct {
		credType  string
		expectIdx string
	}{
		{CredentialTypeDatastoreURI, OwningClusterDatastoreURIIndex},
		{CredentialTypePresharedKey, OwningClusterPresharedKeyIndex},
		{CredentialTypeMigrationSecrets, OwningClusterMigrationSecretsIndex},
		{"", OwningClusterIndex},
		{"unknown", OwningClusterIndex},
	}
	for _, tt := range tests {
		t.Run(tt.credType, func(t *testing.T) {
			require.Equal(t, tt.expectIdx, IndexNameForCredentialType(tt.credType))
		})
	}
}
