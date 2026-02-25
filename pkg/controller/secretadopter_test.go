package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/manager"
	"github.com/authzed/controller-idioms/queue"
	"github.com/authzed/controller-idioms/typed"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubetesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	dfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"

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

	// Add the OwningClusterIndex, matching production setup in NewController.
	inf := informerFactory.ForResource(secretsGVR).Informer()
	if err := inf.AddIndexers(cache.Indexers{
		metadata.OwningClusterIndex: metadata.GetClusterKeyFromMeta,
	}); err != nil {
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

	// adoptedSecret returns a secret already adopted: it has both the managed
	// label (visible to the filtered informer) and the owner annotation (found
	// in the OwningClusterIndex).
	adoptedSecret := func(name string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
				Labels: map[string]string{
					metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
				},
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

	tests := []struct {
		name string

		// secretNames is placed into CtxSecretNames.
		secretNames []string
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
			name:             "no secrets - calls next",
			secretNames:      []string{},
			expectNextCalled: true,
		},
		{
			name:              "single secret already adopted - calls next",
			secretNames:       []string{"secret1"},
			secretsInRegistry: []*corev1.Secret{adoptedSecret("secret1")},
			expectNextCalled:  true,
		},
		{
			name:              "two secrets both present - calls next",
			secretNames:       []string{"secret1", "secret2"},
			secretsInRegistry: []*corev1.Secret{adoptedSecret("secret1"), adoptedSecret("secret2")},
			expectNextCalled:  true,
		},
		{
			name:              "first secret missing - does not call next, sets MissingSecret condition",
			secretNames:       []string{"missing-first", "secret2"},
			secretsInRegistry: []*corev1.Secret{adoptedSecret("secret2")},
			expectNextCalled:  false,
			expectPatchedSecret: types.NamespacedName{
				Namespace: testNamespace,
				Name:      "missing-first",
			},
		},
		{
			name:              "second secret missing - does not call next, sets MissingSecret condition",
			secretNames:       []string{"secret1", "missing-second"},
			secretsInRegistry: []*corev1.Secret{adoptedSecret("secret1")},
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
			name:              "unlabelled secret gets adopted - calls next",
			secretNames:       []string{"new-secret"},
			secretsInRegistry: []*corev1.Secret{unlabelledSecret("new-secret")},
			expectNextCalled:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, patchedCh := newTestControllerForSecretAdopter(t, testNamespace, tt.secretsInRegistry)

			// Use a real queue.Operations wired to a cancelable context so that
			// QueueOps.RequeueErr / RequeueAPIErr properly cancel secretCtx,
			// matching the canonical errors.Is(ctx.Err(), context.Canceled) check
			// used in secretAdopter.
			handlerCtx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			ops := queue.NewOperations(cancel, func(_ time.Duration) { cancel() }, cancel)

			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: testNamespace,
				},
			}

			ctx := handlerCtx
			ctx = QueueOps.WithValue(ctx, ops)
			ctx = CtxCacheNamespace.WithValue(ctx, testNamespace)
			ctx = CtxCluster.WithValue(ctx, cluster)
			ctx = CtxClusterNN.WithValue(ctx, types.NamespacedName{
				Namespace: testNamespace,
				Name:      clusterName,
			})
			ctx = CtxSecretNames.WithValue(ctx, tt.secretNames)

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
