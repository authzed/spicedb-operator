package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/libctrl/fake"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func TestSecretAdopterHandler(t *testing.T) {
	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "secret",
			Namespace: "test",
			Labels: map[string]string{
				metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
			},
			Annotations: map[string]string{
				metadata.OwnerAnnotationKeyPrefix + "test": "owned",
			},
		},
	}
	cluster := &v1alpha1.SpiceDBCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test",
		},
		Spec: v1alpha1.ClusterSpec{SecretRef: "secret"},
	}
	tests := []struct {
		name                string
		secretName          string
		secretsInIndex      []*corev1.Secret
		allClusters         []*v1alpha1.SpiceDBCluster
		secretApplyInput    *applycorev1.SecretApplyConfiguration
		secretApplyResult   *corev1.Secret
		secretApplyErr      error
		expectApply         bool
		expectEvents        []string
		expectNext          bool
		expectRequeueErr    error
		expectRequeueAPIErr error
		expectRequeue       bool
		expectCtxSecretHash string
		expectCtxSecret     *corev1.Secret
	}{
		{
			name:       "no secret",
			secretName: "",
			expectNext: true,
		},
		{
			name:           "secret needs adopting",
			secretName:     "secret",
			secretsInIndex: []*corev1.Secret{},
			allClusters:    []*v1alpha1.SpiceDBCluster{cluster},
			secretApplyInput: applycorev1.Secret("secret", "test").
				WithLabels(map[string]string{
					metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
				}).
				WithAnnotations(map[string]string{
					metadata.OwnerAnnotationKeyPrefix + "test": "owned",
				}),
			secretApplyResult:   testSecret,
			expectApply:         true,
			expectEvents:        []string{"Normal SecretAdoptedBySpiceDB Secret was referenced as the secret source for SpiceDBCluster test/test; it has been labelled to mark it as part of the configuration for that controller."},
			expectRequeue:       true,
			expectCtxSecretHash: "n697h65ch575h5dfhf4h566h5d4h557h694h576h655h68bh6hcch577h9fh685h9hd5h66bhb9h596h646h644hch64dh668h88q",
			expectCtxSecret:     testSecret,
		},
		{
			name:           "secret already adopted",
			secretName:     "secret",
			secretsInIndex: []*corev1.Secret{testSecret},
			allClusters:    []*v1alpha1.SpiceDBCluster{cluster},
			secretApplyInput: applycorev1.Secret("secret", "test").
				WithLabels(map[string]string{
					metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
				}).
				WithAnnotations(map[string]string{
					metadata.OwnerAnnotationKeyPrefix + "test": "owned",
				}),
			secretApplyResult:   testSecret,
			expectApply:         false,
			expectEvents:        []string{},
			expectNext:          true,
			expectCtxSecretHash: "n697h65ch575h5dfhf4h566h5d4h557h694h576h655h68bh6hcch577h9fh685h9hd5h66bhb9h596h646h644hch64dh668h88q",
			expectCtxSecret:     testSecret,
		},
		{
			name:           "secret used by multiple clusters",
			secretName:     "secret",
			secretsInIndex: []*corev1.Secret{},
			allClusters: []*v1alpha1.SpiceDBCluster{cluster, {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test2",
					Namespace: "test",
				},
				Spec: v1alpha1.ClusterSpec{SecretRef: "secret"},
			}, {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test3",
					Namespace: "test",
				},
			}},
			secretApplyInput: applycorev1.Secret("secret", "test").
				WithLabels(map[string]string{
					metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
				}).
				WithAnnotations(map[string]string{
					metadata.OwnerAnnotationKeyPrefix + "test":  "owned",
					metadata.OwnerAnnotationKeyPrefix + "test2": "owned",
				}),
			secretApplyResult: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret",
					Namespace: "test",
					Labels: map[string]string{
						metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
					},
					Annotations: map[string]string{
						metadata.OwnerAnnotationKeyPrefix + "test":  "owned",
						metadata.OwnerAnnotationKeyPrefix + "test2": "owned",
					},
				},
			},
			expectApply:         true,
			expectEvents:        []string{"Normal SecretAdoptedBySpiceDB Secret was referenced as the secret source for SpiceDBCluster test/test; it has been labelled to mark it as part of the configuration for that controller."},
			expectRequeue:       true,
			expectCtxSecretHash: "n697h65ch575h5dfhf4h566h5d4h557h694h576h655h68bh6hcch577h9fh685h9hd5h66bhb9h596h646h644hch64dh668h88q",
			expectCtxSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret",
					Namespace: "test",
					Labels: map[string]string{
						metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
					},
					Annotations: map[string]string{
						metadata.OwnerAnnotationKeyPrefix + "test":  "owned",
						metadata.OwnerAnnotationKeyPrefix + "test2": "owned",
					},
				},
			},
		},
		{
			name:        "transient error adopting secret",
			secretName:  "secret",
			allClusters: []*v1alpha1.SpiceDBCluster{cluster},
			secretApplyInput: applycorev1.Secret("secret", "test").
				WithLabels(map[string]string{
					metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
				}).
				WithAnnotations(map[string]string{
					metadata.OwnerAnnotationKeyPrefix + "test": "owned",
				}),
			secretApplyErr:      apierrors.NewTooManyRequestsError("server having issues"),
			expectApply:         true,
			expectRequeueAPIErr: apierrors.NewTooManyRequestsError("server having issues"),
		},
		{
			name:       "multiple secrets",
			secretName: "secret",
			secretsInIndex: []*corev1.Secret{testSecret, {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret2",
					Namespace: "test",
					Labels: map[string]string{
						metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
					},
					Annotations: map[string]string{
						metadata.OwnerAnnotationKeyPrefix + "test": "owned",
					},
				},
			}},
			allClusters: []*v1alpha1.SpiceDBCluster{cluster},
			secretApplyInput: applycorev1.Secret("secret2", "test").
				WithLabels(map[string]string{}).
				WithAnnotations(map[string]string{}),
			expectApply:         true,
			expectCtxSecretHash: "n697h65ch575h5dfhf4h566h5d4h557h694h576h655h68bh6hcch577h9fh685h9hd5h66bhb9h596h646h644hch64dh668h88q",
			expectCtxSecret:     testSecret,
			expectNext:          true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeControlAll{}
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{metadata.OwningClusterIndex: metadata.GetClusterKeyFromMeta})
			IndexAddUnstructured(t, indexer, tt.secretsInIndex)
			recorder := record.NewFakeRecorder(1)
			applyCalled := false
			nextCalled := false
			s := &SecretAdopterHandler{
				secretName:    tt.secretName,
				recorder:      recorder,
				secretIndexer: indexer,
				secretApplyFunc: func(ctx context.Context, secret *applycorev1.SecretApplyConfiguration, opts metav1.ApplyOptions) (result *corev1.Secret, err error) {
					applyCalled = true
					require.Equal(t, tt.secretApplyInput, secret)
					return tt.secretApplyResult, tt.secretApplyErr
				},
				allClusters: func(ctx context.Context) []*v1alpha1.SpiceDBCluster {
					return tt.allClusters
				},
				next: handler.ContextHandlerFunc(func(ctx context.Context) {
					nextCalled = true
					require.Equal(t, tt.expectCtxSecret, CtxSecret.Value(ctx))
					require.Equal(t, tt.expectCtxSecretHash, CtxSecretHash.Value(ctx))
				}),
			}
			ctx := CtxClusterNN.WithValue(context.Background(), types.NamespacedName{
				Namespace: "test",
				Name:      "test",
			})
			ctx = CtxHandlerControls.WithValue(ctx, ctrls)
			s.Handle(ctx)

			require.Equal(t, tt.expectApply, applyCalled)
			ExpectEvents(t, recorder, tt.expectEvents)
			require.Equal(t, tt.expectNext, nextCalled)
			if tt.expectRequeueErr != nil {
				require.Equal(t, 1, ctrls.RequeueErrCallCount())
				require.Equal(t, tt.expectRequeueErr, ctrls.RequeueErrArgsForCall(0))
			}
			if tt.expectRequeueAPIErr != nil {
				require.Equal(t, 1, ctrls.RequeueAPIErrCallCount())
				require.Equal(t, tt.expectRequeueAPIErr, ctrls.RequeueAPIErrArgsForCall(0))
			}
			require.Equal(t, tt.expectRequeue, ctrls.RequeueCallCount() == 1)
		})
	}
}

func ExpectEvents(t *testing.T, recorder *record.FakeRecorder, expected []string) {
	close(recorder.Events)
	events := make([]string, 0)
	for e := range recorder.Events {
		events = append(events, e)
	}
	require.ElementsMatch(t, expected, events)
}

func IndexAddUnstructured[K runtime.Object](t *testing.T, indexer cache.Indexer, objs []K) {
	for _, s := range objs {
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(s)
		require.NoError(t, err)
		require.NoError(t, indexer.Add(&unstructured.Unstructured{Object: u}))
	}
}
