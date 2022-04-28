package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl/fake"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
)

func TestValidateConfigHandler(t *testing.T) {
	var nextKey handler.Key = "next"
	tests := []struct {
		name string

		rawConfig      map[string]string
		currentStatus  *v1alpha1.SpiceDBCluster
		existingSecret *corev1.Secret

		expectNext        handler.Key
		expectEvents      []string
		expectPatchStatus bool
		expectConditions  []string
		expectRequeue     bool
		expectDone        bool
	}{
		{
			name:          "valid config",
			currentStatus: &v1alpha1.SpiceDBCluster{},
			rawConfig: map[string]string{
				"datastoreEngine": "cockroachdb",
			},
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectNext: nextKey,
		},
		{
			name:          "invalid config",
			currentStatus: &v1alpha1.SpiceDBCluster{},
			rawConfig: map[string]string{
				"badkey": "cockroachdb",
			},
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectConditions:  []string{"ValidatingFailed"},
			expectEvents:      []string{"Warning InvalidSpiceDBConfig invalid config: datastoreEngine is a required field"},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name:          "invalid config, missing secret",
			currentStatus: &v1alpha1.SpiceDBCluster{},
			rawConfig: map[string]string{
				"datastoreEngine": "cockroachdb",
			},
			expectEvents:      []string{"Warning InvalidSpiceDBConfig invalid config: secret must be provided"},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name:          "invalid config, multiple reasons",
			currentStatus: &v1alpha1.SpiceDBCluster{},
			rawConfig: map[string]string{
				"nope": "cockroachdb",
			},
			expectEvents:      []string{"Warning InvalidSpiceDBConfig invalid config: [datastoreEngine is a required field, secret must be provided]"},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name: "detects when config is now valid",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:    "ValidatingFailed",
				Status:  metav1.ConditionTrue,
				Message: "invalid config: [datastoreEngine is a required field, secret must be provided]",
			}}}},
			rawConfig: map[string]string{
				"datastoreEngine": "cockroachdb",
			},
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectNext:        nextKey,
		},
		{
			name: "doesn't emit an event if validating failed for the same reasons",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:    "ValidatingFailed",
				Status:  metav1.ConditionTrue,
				Message: "Error validating config with secret hash \"\": [datastoreEngine is a required field, secret must be provided]",
			}}}},
			rawConfig: map[string]string{
				"nope": "cockroachdb",
			},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: false,
			expectDone:        true,
		},
		{
			name: "emits an event if validating failed for different reasons",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:    "ValidatingFailed",
				Status:  metav1.ConditionTrue,
				Message: "Error validating config with secret hash \"\": [datastoreEngine is a required field, secret must be provided]",
			}}}},
			rawConfig: map[string]string{
				"datastoreEngine": "cockroachdb",
			},
			expectEvents:      []string{"Warning InvalidSpiceDBConfig invalid config: secret must be provided"},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: true,
			expectDone:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeControlAll{}
			recorder := record.NewFakeRecorder(1)
			patchCalled := false

			ctx := context.Background()
			ctx = handlercontext.CtxSecret.WithValue(ctx, tt.existingSecret)
			ctx = handlercontext.CtxClusterNN.WithValue(ctx, types.NamespacedName{Namespace: "test", Name: "test"})
			ctx = handlercontext.CtxClusterStatus.WithValue(ctx, tt.currentStatus)

			var called handler.Key
			h := &ValidateConfigHandler{
				ControlDoneRequeue: ctrls,
				uid:                "uid",
				rawConfig:          tt.rawConfig,
				spiceDBImage:       "image",
				generation:         1,
				patchStatus: func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
					patchCalled = true
					return nil
				},
				recorder: recorder,
				next: handler.ContextHandlerFunc(func(ctx context.Context) {
					called = nextKey
				}),
			}
			h.Handle(ctx)

			cluster := handlercontext.CtxClusterStatus.MustValue(ctx)
			for _, c := range tt.expectConditions {
				require.True(t, meta.IsStatusConditionTrue(cluster.Status.Conditions, c))
			}
			require.Equal(t, len(tt.expectConditions), len(cluster.Status.Conditions))
			require.Equal(t, tt.expectPatchStatus, patchCalled)
			require.Equal(t, tt.expectNext, called)
			require.Equal(t, tt.expectRequeue, ctrls.RequeueCallCount() == 1)
			require.Equal(t, tt.expectDone, ctrls.DoneCallCount() == 1)
			ExpectEvents(t, recorder, tt.expectEvents)
		})
	}
}
