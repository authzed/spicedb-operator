package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"
	"github.com/authzed/controller-idioms/queue/fake"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

func TestConfigChangedHandlerSecretHash(t *testing.T) {
	secretA := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
		Data: map[string][]byte{
			"datastore_uri":     []byte("uri-alpha"),
			"preshared_key":     []byte("psk-alpha"),
			"migration_secrets": []byte("mig-alpha"),
			"irrelevant":        []byte("should-not-affect-hash"),
		},
	}
	secretB := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "beta"},
		Data: map[string][]byte{
			"custom_uri": []byte("uri-beta"),
			"custom_psk": []byte("psk-beta"),
		},
	}

	tests := []struct {
		name           string
		cluster        *v1alpha1.SpiceDBCluster
		secrets        map[string]*corev1.Secret
		expectHashFunc func() string
	}{
		{
			name:           "no secrets yields empty hash",
			cluster:        &v1alpha1.SpiceDBCluster{},
			secrets:        map[string]*corev1.Secret{},
			expectHashFunc: func() string { return "" },
		},
		{
			name: "SecretRef path hashes datastore_uri, migration_secrets, and preshared_key",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{SecretRef: "alpha"},
			},
			secrets: map[string]*corev1.Secret{"alpha": secretA},
			expectHashFunc: func() string {
				type hashEntry struct{ Secret, Key, Value string }
				return hash.SecureObject([]hashEntry{
					{Secret: "alpha", Key: "datastore_uri", Value: "uri-alpha"},
					{Secret: "alpha", Key: "migration_secrets", Value: "mig-alpha"},
					{Secret: "alpha", Key: "preshared_key", Value: "psk-alpha"},
				})
			},
		},
		{
			name: "SecretRef path: irrelevant key change does not affect hash",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{SecretRef: "alpha"},
			},
			secrets: map[string]*corev1.Secret{
				"alpha": {
					ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
					Data: map[string][]byte{
						"datastore_uri":     []byte("uri-alpha"),
						"preshared_key":     []byte("psk-alpha"),
						"migration_secrets": []byte("mig-alpha"),
						"irrelevant":        []byte("different-value"),
					},
				},
			},
			expectHashFunc: func() string {
				type hashEntry struct{ Secret, Key, Value string }
				return hash.SecureObject([]hashEntry{
					{Secret: "alpha", Key: "datastore_uri", Value: "uri-alpha"},
					{Secret: "alpha", Key: "migration_secrets", Value: "mig-alpha"},
					{Secret: "alpha", Key: "preshared_key", Value: "psk-alpha"},
				})
			},
		},
		{
			name: "Credentials path with custom keys",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI: &v1alpha1.CredentialRef{SecretName: "beta", Key: "custom_uri"},
						PresharedKey: &v1alpha1.CredentialRef{SecretName: "beta", Key: "custom_psk"},
						// MigrationSecrets is nil -- defaults to DatastoreURI secret with migration_secrets key
					},
				},
			},
			secrets: map[string]*corev1.Secret{"beta": secretB},
			expectHashFunc: func() string {
				type hashEntry struct{ Secret, Key, Value string }
				return hash.SecureObject([]hashEntry{
					{Secret: "beta", Key: "custom_psk", Value: "psk-beta"},
					{Secret: "beta", Key: "custom_uri", Value: "uri-beta"},
					{Secret: "beta", Key: "migration_secrets", Value: ""},
				})
			},
		},
		{
			name: "Credentials with Skip on DatastoreURI: only preshared_key hashed",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI: &v1alpha1.CredentialRef{Skip: true},
						PresharedKey: &v1alpha1.CredentialRef{SecretName: "alpha", Key: "preshared_key"},
					},
				},
			},
			secrets: map[string]*corev1.Secret{"alpha": secretA},
			expectHashFunc: func() string {
				type hashEntry struct{ Secret, Key, Value string }
				return hash.SecureObject([]hashEntry{
					{Secret: "alpha", Key: "preshared_key", Value: "psk-alpha"},
				})
			},
		},
		{
			name: "Credentials path with explicit MigrationSecrets in separate secret",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI:     &v1alpha1.CredentialRef{SecretName: "beta", Key: "custom_uri"},
						PresharedKey:     &v1alpha1.CredentialRef{SecretName: "beta", Key: "custom_psk"},
						MigrationSecrets: &v1alpha1.CredentialRef{SecretName: "alpha", Key: "migration_secrets"},
					},
				},
			},
			secrets: map[string]*corev1.Secret{"beta": secretB, "alpha": secretA},
			expectHashFunc: func() string {
				type hashEntry struct{ Secret, Key, Value string }
				return hash.SecureObject([]hashEntry{
					{Secret: "alpha", Key: "migration_secrets", Value: "mig-alpha"},
					{Secret: "beta", Key: "custom_psk", Value: "psk-beta"},
					{Secret: "beta", Key: "custom_uri", Value: "uri-beta"},
				})
			},
		},
		{
			name: "Credentials path nil MigrationSecrets: migration_secrets key is hashed from DatastoreURI secret",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI: &v1alpha1.CredentialRef{SecretName: "alpha", Key: "datastore_uri"},
						PresharedKey: &v1alpha1.CredentialRef{SecretName: "alpha", Key: "preshared_key"},
						// MigrationSecrets is nil -- should default to DatastoreURI secret
					},
				},
			},
			secrets: map[string]*corev1.Secret{"alpha": secretA},
			expectHashFunc: func() string {
				type hashEntry struct{ Secret, Key, Value string }
				return hash.SecureObject([]hashEntry{
					{Secret: "alpha", Key: "datastore_uri", Value: "uri-alpha"},
					{Secret: "alpha", Key: "migration_secrets", Value: "mig-alpha"},
					{Secret: "alpha", Key: "preshared_key", Value: "psk-alpha"},
				})
			},
		},
		{
			name: "Credentials path MigrationSecrets Skip: not hashed",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI:     &v1alpha1.CredentialRef{SecretName: "beta", Key: "custom_uri"},
						PresharedKey:     &v1alpha1.CredentialRef{SecretName: "beta", Key: "custom_psk"},
						MigrationSecrets: &v1alpha1.CredentialRef{Skip: true},
					},
				},
			},
			secrets: map[string]*corev1.Secret{"beta": secretB},
			expectHashFunc: func() string {
				type hashEntry struct{ Secret, Key, Value string }
				return hash.SecureObject([]hashEntry{
					{Secret: "beta", Key: "custom_psk", Value: "psk-beta"},
					{Secret: "beta", Key: "custom_uri", Value: "uri-beta"},
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeInterface{}

			cluster := tt.cluster
			if cluster.Generation == 0 {
				cluster.Generation = 1
				cluster.Status.ObservedGeneration = 1
			}
			// Give the cluster a name/namespace
			if cluster.Name == "" {
				cluster.Name = "test"
				cluster.Namespace = "test"
			}

			var capturedHash string
			nextCalled := false

			h := &ConfigChangedHandler{
				patchStatus: func(_ context.Context, _ *v1alpha1.SpiceDBCluster) error {
					return nil
				},
				next: handler.ContextHandlerFunc(func(ctx context.Context) {
					nextCalled = true
					capturedHash = CtxSecretHash.Value(ctx)
				}),
			}

			ctx := context.Background()
			ctx = QueueOps.WithValue(ctx, ctrls)
			ctx = CtxCluster.WithValue(ctx, cluster)
			ctx = CtxSecrets.WithValue(ctx, tt.secrets)

			h.Handle(ctx)

			require.True(t, nextCalled, "expected next handler to be called")
			expected := tt.expectHashFunc()
			require.Equal(t, expected, capturedHash, "secret hash mismatch")
		})
	}
}

func TestConfigChangedHandlerStatusUpdate(t *testing.T) {
	// pre-compute the hash that matches the secretRef "alpha" secret below,
	// so we can construct a cluster whose status is already up-to-date.
	type hashEntry struct{ Secret, Key, Value string }
	upToDateHash := hash.SecureObject([]hashEntry{
		{Secret: "alpha", Key: "datastore_uri", Value: "uri"},
		{Secret: "alpha", Key: "migration_secrets", Value: ""},
		{Secret: "alpha", Key: "preshared_key", Value: "psk"},
	})
	alphaSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
		Data:       map[string][]byte{"datastore_uri": []byte("uri"), "preshared_key": []byte("psk")},
	}

	tests := []struct {
		name              string
		cluster           *v1alpha1.SpiceDBCluster
		secrets           map[string]*corev1.Secret
		patchErr          error
		expectPatchCalled bool
		expectNextCalled  bool
		checkPatch        func(t *testing.T, patched *v1alpha1.SpiceDBCluster)
	}{
		{
			name: "generation changed triggers patch",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test", Generation: 2},
				Status:     v1alpha1.ClusterStatus{ObservedGeneration: 1},
			},
			secrets:           map[string]*corev1.Secret{},
			expectPatchCalled: true,
			expectNextCalled:  true,
			checkPatch: func(t *testing.T, patched *v1alpha1.SpiceDBCluster) {
				require.Equal(t, int64(2), patched.Status.ObservedGeneration)
				require.NotNil(t, patched.FindStatusCondition(v1alpha1.ConditionTypeValidating))
			},
		},
		{
			name: "secret hash changed triggers patch",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test", Generation: 1},
				Spec:       v1alpha1.ClusterSpec{SecretRef: "alpha"},
				Status:     v1alpha1.ClusterStatus{ObservedGeneration: 1, SecretHash: "old-hash"},
			},
			secrets:           map[string]*corev1.Secret{"alpha": alphaSecret},
			expectPatchCalled: true,
			expectNextCalled:  true,
			checkPatch: func(t *testing.T, patched *v1alpha1.SpiceDBCluster) {
				require.Equal(t, upToDateHash, patched.Status.SecretHash)
				require.NotNil(t, patched.FindStatusCondition(v1alpha1.ConditionTypeValidating))
			},
		},
		{
			name: "nothing changed skips patch",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test", Generation: 1},
				Spec:       v1alpha1.ClusterSpec{SecretRef: "alpha"},
				Status:     v1alpha1.ClusterStatus{ObservedGeneration: 1, SecretHash: upToDateHash},
			},
			secrets:           map[string]*corev1.Secret{"alpha": alphaSecret},
			expectPatchCalled: false,
			expectNextCalled:  true,
		},
		{
			name: "MissingSecret precondition triggers patch and is cleared",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test", Generation: 1},
				Status: v1alpha1.ClusterStatus{
					ObservedGeneration: 1,
					Conditions: []metav1.Condition{
						v1alpha1.NewMissingSecretCondition(types.NamespacedName{Namespace: "test", Name: "secret"}),
					},
				},
			},
			secrets:           map[string]*corev1.Secret{},
			expectPatchCalled: true,
			expectNextCalled:  true,
			checkPatch: func(t *testing.T, patched *v1alpha1.SpiceDBCluster) {
				require.Nil(t, patched.FindStatusCondition(v1alpha1.ConditionTypePreconditionsFailed))
				require.NotNil(t, patched.FindStatusCondition(v1alpha1.ConditionTypeValidating))
			},
		},
		{
			name: "patch error requeues without calling next",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test", Generation: 2},
				Status:     v1alpha1.ClusterStatus{ObservedGeneration: 1},
			},
			secrets:           map[string]*corev1.Secret{},
			patchErr:          fmt.Errorf("server error"),
			expectPatchCalled: true,
			expectNextCalled:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeInterface{}
			patchCalled := false
			var patchedCluster *v1alpha1.SpiceDBCluster
			nextCalled := false

			h := &ConfigChangedHandler{
				patchStatus: func(_ context.Context, patch *v1alpha1.SpiceDBCluster) error {
					patchCalled = true
					patchedCluster = patch
					return tt.patchErr
				},
				next: handler.ContextHandlerFunc(func(_ context.Context) {
					nextCalled = true
				}),
			}

			ctx := context.Background()
			ctx = QueueOps.WithValue(ctx, ctrls)
			ctx = CtxCluster.WithValue(ctx, tt.cluster)
			ctx = CtxSecrets.WithValue(ctx, tt.secrets)

			h.Handle(ctx)

			require.Equal(t, tt.expectPatchCalled, patchCalled, "patchStatus called mismatch")
			require.Equal(t, tt.expectNextCalled, nextCalled, "next called mismatch")
			if tt.checkPatch != nil {
				require.NotNil(t, patchedCluster)
				tt.checkPatch(t, patchedCluster)
			}
		})
	}
}
