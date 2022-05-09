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

	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
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
				metadata.OwnerLabelKey: "test",
			},
		},
	}
	tests := []struct {
		name                string
		secretName          string
		secretsInIndex      []*corev1.Secret
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
			name:                "secret needs adopting",
			secretName:          "secret",
			secretsInIndex:      []*corev1.Secret{},
			secretApplyResult:   testSecret,
			expectApply:         true,
			expectEvents:        []string{"Normal SecretAdoptedBySpiceDB Secret was referenced as the secret source for a SpiceDBCluster; it has been labelled to mark it as part of the configuration for that controller."},
			expectRequeue:       true,
			expectCtxSecretHash: "n65dh5fbh65h659h5d5h659h57fh68h99h5d4h6fh5d9h5bdh586hd8h5c9h5d8h698h5b9h669hdbh656h59h667h58fh77h558h596q",
			expectCtxSecret:     testSecret,
		},
		{
			name:                "secret already adopted",
			secretName:          "secret",
			secretsInIndex:      []*corev1.Secret{testSecret},
			secretApplyResult:   testSecret,
			expectApply:         false,
			expectEvents:        []string{},
			expectNext:          true,
			expectCtxSecretHash: "n65dh5fbh65h659h5d5h659h57fh68h99h5d4h6fh5d9h5bdh586hd8h5c9h5d8h698h5b9h669hdbh656h59h667h58fh77h558h596q",
			expectCtxSecret:     testSecret,
		},
		{
			name:                "transient error adopting secret",
			secretName:          "secret",
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
						metadata.OwnerLabelKey: "test",
					},
				},
			}},
			expectApply:         true,
			expectCtxSecretHash: "n65dh5fbh65h659h5d5h659h57fh68h99h5d4h6fh5d9h5bdh586hd8h5c9h5d8h698h5b9h669hdbh656h59h667h58fh77h558h596q",
			expectCtxSecret:     testSecret,
			expectNext:          true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeControlAll{}
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{metadata.OwningClusterIndex: metadata.GetClusterKeyFromLabel})
			IndexAddUnstructured(t, indexer, tt.secretsInIndex)
			recorder := record.NewFakeRecorder(1)
			applyCalled := false
			nextCalled := false
			s := &SecretAdopterHandler{
				ControlAll:    ctrls,
				secretName:    tt.secretName,
				recorder:      recorder,
				secretIndexer: indexer,
				secretApplyFunc: func(ctx context.Context, secret *applycorev1.SecretApplyConfiguration, opts metav1.ApplyOptions) (result *corev1.Secret, err error) {
					applyCalled = true
					return tt.secretApplyResult, tt.secretApplyErr
				},
				next: handler.ContextHandlerFunc(func(ctx context.Context) {
					nextCalled = true
					require.Equal(t, tt.expectCtxSecret, handlercontext.CtxSecret.Value(ctx))
					require.Equal(t, tt.expectCtxSecretHash, handlercontext.CtxSecretHash.Value(ctx))
				}),
			}
			ctx := handlercontext.CtxClusterNN.WithValue(context.Background(), types.NamespacedName{
				Namespace: "test",
				Name:      "test",
			})
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
