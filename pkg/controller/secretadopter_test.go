package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	kubetesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/manager"
	"github.com/authzed/controller-idioms/queue"
	"github.com/authzed/controller-idioms/typed"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

// newTestControllerForSecretAdopter creates a minimal Controller suitable for
// testing secretAdopter. It sets up a real typed.Registry with a fake dynamic
// informer factory for the secrets GVR, populated with the given secrets.
// It returns the Controller and a buffered channel that receives SpiceDBCluster
// objects passed to PatchStatus.
func newTestControllerForSecretAdopter(
	t *testing.T,
	testNamespace string,
	secrets []*corev1.Secret,
	extraIndexers cache.Indexers,
) (*Controller, chan *v1alpha1.SpiceDBCluster) {
	t.Helper()

	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")

	// Build runtime.Object slice for the fake dynamic client.
	runtimeObjs := make([]runtime.Object, 0, len(secrets))
	for _, s := range secrets {
		runtimeObjs = append(runtimeObjs, s.DeepCopy())
	}
	dclient := dfake.NewSimpleDynamicClient(kscheme.Scheme, runtimeObjs...)

	// Capture PatchStatus calls by intercepting Apply-patch actions on the
	// SpiceDBCluster resource (status subresource).
	patchedCh := make(chan *v1alpha1.SpiceDBCluster, 4)
	dclient.PrependReactor("patch", v1alpha1.SpiceDBClusterResourceName,
		func(action kubetesting.Action) (bool, runtime.Object, error) {
			patchAction, ok := action.(kubetesting.PatchAction)
			if !ok {
				return false, nil, nil
			}
			var cluster v1alpha1.SpiceDBCluster
			if err := json.Unmarshal(patchAction.GetPatch(), &cluster); err != nil {
				return false, nil, err
			}
			patchedCh <- &cluster
			u, _ := typed.ObjToUnstructuredObj(&cluster)
			return true, u, nil
		})

	// Set up the registry with a DependentFactoryKey factory for the test
	// namespace, filtered by the managed label (matching production setup in
	// NewController).
	registry := typed.NewRegistry()
	factoryKey := DependentFactoryKey(testNamespace)
	informerFactory := registry.MustNewFilteredDynamicSharedInformerFactory(
		factoryKey,
		dclient,
		0,
		testNamespace,
		func(opts *metav1.ListOptions) {
			opts.LabelSelector = metadata.ManagedDependentSelector.String()
		},
	)

	// Add the OwningClusterIndex plus any extra indexes (e.g. per-type).
	idxs := cache.Indexers{metadata.OwningClusterIndex: metadata.GetClusterKeyFromMeta}
	for k, v := range extraIndexers {
		idxs[k] = v
	}
	inf := informerFactory.ForResource(secretsGVR).Informer()
	if err := inf.AddIndexers(idxs); err != nil {
		t.Fatalf("failed to add indexers: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	// Fake kubernetes client for Secrets.Get and Secrets.Apply (used inside
	// the adoption handler when a secret needs labelling/annotating or when
	// the adoption handler cleans up old owner annotations). Pre-populate with
	// the same secrets so that Apply calls for existing secrets succeed.
	kclientObjs := make([]runtime.Object, 0, len(secrets))
	for _, s := range secrets {
		kclientObjs = append(kclientObjs, s.DeepCopy())
	}
	kclient := kfake.NewClientset(kclientObjs...)

	// Fake recorder.
	recorder := record.NewFakeRecorder(10)

	c := &Controller{
		OwnedResourceController: &manager.OwnedResourceController{
			Registry: registry,
			Recorder: recorder,
		},
		client:  dclient,
		kclient: kclient,
	}

	return c, patchedCh
}

func TestControllerSecretAdopter(t *testing.T) {
	const testNamespace = "test"
	const clusterName = "mycluster"

	// ownerAnnotationKey marks a secret as owned by clusterName, matching the
	// OwnerAnnotationKeyFunc in NewSecretAdoptionHandler.
	ownerAnnotationKey := metadata.OwnerAnnotationKeyPrefix + clusterName

	// adoptedSecret returns a secret already adopted with an optional credential
	// type. When credType is non-empty the corresponding per-role label key is
	// set so the secret appears in the per-type index. When credType is "" (the
	// legacy SecretRef path) only the managed label is set.
	adoptedSecret := func(name, credType string) *corev1.Secret {
		labels := map[string]string{
			metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
		}
		if lk := metadata.LabelKeyForCredentialType(credType); lk != "" {
			labels[lk] = "true"
		}
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
				Labels:    labels,
				Annotations: map[string]string{
					ownerAnnotationKey: "owned",
				},
			},
		}
	}

	// unlabelledSecret returns a secret that exists in the cluster but has NOT
	// yet been labelled with the managed label. It will be visible to the
	// kclient (existence check) but NOT visible via the filtered informer cache.
	unlabelledSecret := func(name string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
			},
		}
	}

	ref := func(name string) *v1alpha1.CredentialRef { return &v1alpha1.CredentialRef{SecretName: name} }

	tests := []struct {
		name string

		// cluster is the SpiceDBCluster whose credentials drive secretAdopter.
		cluster *v1alpha1.SpiceDBCluster
		// secretsInRegistry are pre-loaded into the fake dynamic informer AND
		// the fake kclient. Only secrets with the managed label are visible
		// through the filtered factory; unlabelled secrets are only reachable
		// via kclient.Get (the existence check).
		secretsInRegistry []*corev1.Secret

		expectNextCalled bool
		// expectPatchedSecret: if non-zero, PatchStatus must be called with a
		// MissingSecret condition referencing this secret.
		expectPatchedSecret types.NamespacedName
	}{
		{
			name: "no secrets - calls next",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
			},
			expectNextCalled: true,
		},
		{
			name: "single secret already adopted via SecretRef - calls next",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
				Spec:       v1alpha1.ClusterSpec{SecretRef: "secret1"},
			},
			// SecretRef path uses the legacy OwningClusterIndex; no type label needed.
			secretsInRegistry: []*corev1.Secret{adoptedSecret("secret1", "")},
			expectNextCalled:  true,
		},
		{
			name: "two secrets both present via Credentials - calls next",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
				Spec: v1alpha1.ClusterSpec{Credentials: &v1alpha1.ClusterCredentials{
					DatastoreURI: ref("secret1"),
					PresharedKey: ref("secret2"),
				}},
			},
			secretsInRegistry: []*corev1.Secret{
				adoptedSecret("secret1", metadata.CredentialTypeDatastoreURI),
				adoptedSecret("secret2", metadata.CredentialTypePresharedKey),
			},
			expectNextCalled: true,
		},
		{
			name: "first secret missing - does not call next, sets MissingSecret condition",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
				Spec: v1alpha1.ClusterSpec{Credentials: &v1alpha1.ClusterCredentials{
					DatastoreURI: ref("missing-first"),
					PresharedKey: ref("secret2"),
				}},
			},
			secretsInRegistry: []*corev1.Secret{adoptedSecret("secret2", metadata.CredentialTypePresharedKey)},
			expectNextCalled:  false,
			expectPatchedSecret: types.NamespacedName{
				Namespace: testNamespace,
				Name:      "missing-first",
			},
		},
		{
			name: "second secret missing - does not call next, sets MissingSecret condition",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
				Spec: v1alpha1.ClusterSpec{Credentials: &v1alpha1.ClusterCredentials{
					DatastoreURI: ref("secret1"),
					PresharedKey: ref("missing-second"),
				}},
			},
			secretsInRegistry: []*corev1.Secret{adoptedSecret("secret1", metadata.CredentialTypeDatastoreURI)},
			expectNextCalled:  false,
			expectPatchedSecret: types.NamespacedName{
				Namespace: testNamespace,
				Name:      "missing-second",
			},
		},
		{
			// The secret exists in the cluster but has not been labelled yet
			// (not visible in the filtered informer cache). The adoption handler
			// should call Apply to add the managed label and owner annotation,
			// then call next.
			name: "unlabelled secret gets adopted via SecretRef - calls next",
			cluster: &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
				Spec:       v1alpha1.ClusterSpec{SecretRef: "new-secret"},
			},
			secretsInRegistry: []*corev1.Secret{unlabelledSecret("new-secret")},
			expectNextCalled:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, patchedCh := newTestControllerForSecretAdopter(t, testNamespace, tt.secretsInRegistry, cache.Indexers{
				metadata.OwningClusterDatastoreURIIndex:     metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypeDatastoreURI),
				metadata.OwningClusterPresharedKeyIndex:     metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypePresharedKey),
				metadata.OwningClusterMigrationSecretsIndex: metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypeMigrationSecrets),
			})

			// Use a real queue.Operations wired to a cancelable context so that
			// QueueOps.RequeueErr / RequeueAPIErr properly cancel secretCtx,
			// matching the canonical errors.Is(ctx.Err(), context.Canceled) check
			// used in secretAdopter.
			handlerCtx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			ops := queue.NewOperations(cancel, func(_ time.Duration) { cancel() }, cancel)

			cluster := tt.cluster

			ctx := handlerCtx
			ctx = QueueOps.WithValue(ctx, ops)
			ctx = CtxCacheNamespace.WithValue(ctx, testNamespace)
			ctx = CtxCluster.WithValue(ctx, cluster)
			ctx = CtxClusterNN.WithValue(ctx, types.NamespacedName{
				Namespace: testNamespace,
				Name:      clusterName,
			})
			// CtxSecretNames is still set for collectSecrets (which runs after
			// secretAdopter); secretAdopter itself derives pairs from the cluster spec.
			ctx = CtxSecretNames.WithValue(ctx, credentialSecretNames(cluster))

			nextCalled := false
			next := handler.NewHandlerFromFunc(func(_ context.Context) {
				nextCalled = true
			}, "testnext")

			c.secretAdopter(next).Handle(ctx)

			require.Equal(t, tt.expectNextCalled, nextCalled, "next called mismatch")

			if (tt.expectPatchedSecret != types.NamespacedName{}) {
				require.Equal(t, 1, len(patchedCh),
					"expected exactly one PatchStatus call")
				patched := <-patchedCh
				require.Equal(t, clusterName, patched.Name)
				require.Equal(t, testNamespace, patched.Namespace)

				found := false
				for _, cond := range patched.Status.Conditions {
					if cond.Type == v1alpha1.ConditionTypePreconditionsFailed &&
						cond.Reason == v1alpha1.ConditionReasonMissingSecret {
						require.Contains(t, cond.Message, tt.expectPatchedSecret.String(),
							"MissingSecret condition message should reference the missing secret")
						found = true
						break
					}
				}
				require.True(t, found,
					"expected MissingSecret condition in patched status; got: %v",
					patched.Status.Conditions)
			} else {
				require.Equal(t, 0, len(patchedCh),
					"unexpected PatchStatus call")
			}
		})
	}
}

// TestSecretAdopterPerTypeIndexIsolation is a regression test for the bug
// where N sequential AdoptionHandlers sharing OwningClusterIndex would each
// remove the other handler's valid adoptee as if it were stale, causing an
// infinite re-adoption loop.
//
// With per-type indexes, each adoption handler only sees secrets of its own
// credential type in the index, so it never treats another type's secret as
// an extra to clean up.
func TestSecretAdopterPerTypeIndexIsolation(t *testing.T) {
	const testNamespace = "test"
	const clusterName = "mycluster"

	ownerAnnotationKey := metadata.OwnerAnnotationKeyPrefix + clusterName

	makeAdoptedSecret := func(name, credType string) *corev1.Secret {
		labels := map[string]string{
			metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
		}
		if lk := metadata.LabelKeyForCredentialType(credType); lk != "" {
			labels[lk] = "true"
		}
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   testNamespace,
				Labels:      labels,
				Annotations: map[string]string{ownerAnnotationKey: "owned"},
			},
		}
	}

	extraIndexers := cache.Indexers{
		metadata.OwningClusterDatastoreURIIndex:     metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypeDatastoreURI),
		metadata.OwningClusterPresharedKeyIndex:     metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypePresharedKey),
		metadata.OwningClusterMigrationSecretsIndex: metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypeMigrationSecrets),
	}
	c, _ := newTestControllerForSecretAdopter(t, testNamespace, []*corev1.Secret{
		makeAdoptedSecret("secret-ds", metadata.CredentialTypeDatastoreURI),
		makeAdoptedSecret("secret-psk", metadata.CredentialTypePresharedKey),
	}, extraIndexers)

	handlerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ops := queue.NewOperations(cancel, func(_ time.Duration) { cancel() }, cancel)

	cluster := &v1alpha1.SpiceDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
		Spec: v1alpha1.ClusterSpec{
			Credentials: &v1alpha1.ClusterCredentials{
				DatastoreURI: &v1alpha1.CredentialRef{SecretName: "secret-ds"},
				PresharedKey: &v1alpha1.CredentialRef{SecretName: "secret-psk"},
			},
		},
	}

	ctx := handlerCtx
	ctx = QueueOps.WithValue(ctx, ops)
	ctx = CtxCacheNamespace.WithValue(ctx, testNamespace)
	ctx = CtxCluster.WithValue(ctx, cluster)
	ctx = CtxClusterNN.WithValue(ctx, types.NamespacedName{Namespace: testNamespace, Name: clusterName})
	ctx = CtxSecretNames.WithValue(ctx, []string{"secret-ds", "secret-psk"})

	nextCalled := false
	c.secretAdopter(handler.NewHandlerFromFunc(func(_ context.Context) {
		nextCalled = true
	}, "testnext")).Handle(ctx)

	require.True(t, nextCalled, "next should be called when both secrets are already adopted")
	require.Empty(t, c.kclient.(*kfake.Clientset).Actions(),
		"secretAdopter must not issue any API calls when all secrets are already adopted with correct type labels")
}

// TestSecretAdopterSharedSecret verifies that a single secret used for multiple
// credential roles is handled independently for each role: both type labels must
// be applied, and once they are no adoption API calls should be issued.
func TestSecretAdopterSharedSecret(t *testing.T) {
	const testNamespace = "test"
	const clusterName = "mycluster"

	ownerAnnotationKey := metadata.OwnerAnnotationKeyPrefix + clusterName
	extraIndexers := cache.Indexers{
		metadata.OwningClusterDatastoreURIIndex:     metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypeDatastoreURI),
		metadata.OwningClusterPresharedKeyIndex:     metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypePresharedKey),
		metadata.OwningClusterMigrationSecretsIndex: metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypeMigrationSecrets),
	}

	cluster := &v1alpha1.SpiceDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
		Spec: v1alpha1.ClusterSpec{
			Credentials: &v1alpha1.ClusterCredentials{
				DatastoreURI: &v1alpha1.CredentialRef{SecretName: "shared"},
				PresharedKey: &v1alpha1.CredentialRef{SecretName: "shared"},
			},
		},
	}

	// sharedSecret carries both per-role label keys and the owner annotation.
	sharedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared",
			Namespace: testNamespace,
			Labels: map[string]string{
				metadata.OperatorManagedLabelKey:            metadata.OperatorManagedLabelValue,
				metadata.CredentialTypeDatastoreURILabelKey: "true",
				metadata.CredentialTypePresharedKeyLabelKey: "true",
			},
			Annotations: map[string]string{ownerAnnotationKey: "owned"},
		},
	}

	c, _ := newTestControllerForSecretAdopter(t, testNamespace, []*corev1.Secret{sharedSecret}, extraIndexers)

	handlerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ops := queue.NewOperations(cancel, func(_ time.Duration) { cancel() }, cancel)

	ctx := handlerCtx
	ctx = QueueOps.WithValue(ctx, ops)
	ctx = CtxCacheNamespace.WithValue(ctx, testNamespace)
	ctx = CtxCluster.WithValue(ctx, cluster)
	ctx = CtxClusterNN.WithValue(ctx, types.NamespacedName{Namespace: testNamespace, Name: clusterName})
	ctx = CtxSecretNames.WithValue(ctx, credentialSecretNames(cluster))

	nextCalled := false
	c.secretAdopter(handler.NewHandlerFromFunc(func(_ context.Context) {
		nextCalled = true
	}, "testnext")).Handle(ctx)

	require.True(t, nextCalled, "next should be called when the shared secret carries all role labels")
	require.Empty(t, c.kclient.(*kfake.Clientset).Actions(),
		"no API calls should be issued when the shared secret already has all required type labels")
}

// TestSecretAdopterSharedSecretMigration is a regression test for the SSA
// field-manager collision that occurs when the same secret is used for two
// credential roles and needs migration (has no per-role type labels yet).
//
// Both adoption handlers run during the same reconcile. If they use the same
// SSA field manager for the labels patch, the second handler's apply releases
// ownership of the first handler's type label — Kubernetes removes it and the
// next reconcile sees the label missing again, causing an infinite loop.
//
// The fix is to use a role-qualified field manager per handler so each handler
// exclusively owns its own type label and the two never interfere.
func TestSecretAdopterSharedSecretMigration(t *testing.T) {
	const testNamespace = "test"
	const clusterName = "mycluster"

	extraIndexers := cache.Indexers{
		metadata.OwningClusterDatastoreURIIndex:     metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypeDatastoreURI),
		metadata.OwningClusterPresharedKeyIndex:     metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypePresharedKey),
		metadata.OwningClusterMigrationSecretsIndex: metadata.GetClusterKeyFromMetaForType(metadata.CredentialTypeMigrationSecrets),
	}

	cluster := &v1alpha1.SpiceDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
		Spec: v1alpha1.ClusterSpec{
			Credentials: &v1alpha1.ClusterCredentials{
				DatastoreURI: &v1alpha1.CredentialRef{SecretName: "shared"},
				PresharedKey: &v1alpha1.CredentialRef{SecretName: "shared"},
			},
		},
	}

	// unlabelledSecret exists in the cluster (visible via kclient.Get) but has
	// no managed label so it is not visible through the filtered informer cache.
	unlabelledSharedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: testNamespace},
	}

	c, _ := newTestControllerForSecretAdopter(t, testNamespace, []*corev1.Secret{unlabelledSharedSecret}, extraIndexers)

	// Intercept every Apply (SSA patch) on secrets and record the field manager
	// used. Two handlers adopting the same secret must use distinct field
	// managers so their label ownership does not overlap.
	type applyRecord struct {
		fieldManager string
		labels       map[string]string
	}
	var labelApplies []applyRecord
	c.kclient.(*kfake.Clientset).PrependReactor("patch", "secrets",
		func(action kubetesting.Action) (bool, runtime.Object, error) {
			pa, ok := action.(kubetesting.PatchActionImpl)
			if !ok || pa.GetPatchType() != types.ApplyPatchType {
				return false, nil, nil
			}
			// Parse labels out of the SSA patch JSON.
			var obj struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
			}
			_ = json.Unmarshal(pa.GetPatch(), &obj)
			if obj.Metadata.Labels != nil {
				labelApplies = append(labelApplies, applyRecord{
					fieldManager: pa.PatchOptions.FieldManager,
					labels:       obj.Metadata.Labels,
				})
			}
			// Return the unlabelled secret so the handler doesn't error out.
			return true, unlabelledSharedSecret.DeepCopy(), nil
		})

	handlerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ops := queue.NewOperations(cancel, func(_ time.Duration) { cancel() }, cancel)

	ctx := handlerCtx
	ctx = QueueOps.WithValue(ctx, ops)
	ctx = CtxCacheNamespace.WithValue(ctx, testNamespace)
	ctx = CtxCluster.WithValue(ctx, cluster)
	ctx = CtxClusterNN.WithValue(ctx, types.NamespacedName{Namespace: testNamespace, Name: clusterName})
	ctx = CtxSecretNames.WithValue(ctx, credentialSecretNames(cluster))

	nextCalled := false
	c.secretAdopter(handler.NewHandlerFromFunc(func(_ context.Context) {
		nextCalled = true
	}, "testnext")).Handle(ctx)

	require.True(t, nextCalled, "secretAdopter should call next after adopting both roles")

	// Both role labels must be applied via distinct field managers. If the same
	// manager is used for both, the second apply would release ownership of the
	// first role's label, causing Kubernetes to remove it (SSA semantics).
	require.Len(t, labelApplies, 2, "expected one labels Apply per credential role")
	require.NotEqual(t, labelApplies[0].fieldManager, labelApplies[1].fieldManager,
		"each credential role must use a distinct SSA field manager to avoid label ownership collisions")

	// Merge both applies' label maps so the assertions are order-independent.
	allLabels := lo.Assign(labelApplies[0].labels, labelApplies[1].labels)
	require.Contains(t, allLabels, metadata.CredentialTypeDatastoreURILabelKey,
		"one apply must carry the datastore-uri type label")
	require.Contains(t, allLabels, metadata.CredentialTypePresharedKeyLabelKey,
		"one apply must carry the preshared-key type label")
}
