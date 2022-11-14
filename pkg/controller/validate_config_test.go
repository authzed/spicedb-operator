package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/queue/fake"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
)

func TestValidateConfigHandler(t *testing.T) {
	var nextKey handler.Key = "next"
	tests := []struct {
		name string

		rawConfig      json.RawMessage
		currentStatus  *v1alpha1.SpiceDBCluster
		existingSecret *corev1.Secret

		expectNext        handler.Key
		expectEvents      []string
		expectStatusImage string
		expectPatchStatus bool
		expectConditions  []string
		expectRequeue     bool
		expectDone        bool
	}{
		{
			name: "valid config, no changes, no warnings",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Image:                "image:tag",
				TargetMigrationHash:  "n5dbh8fh58dh5b7h79h599h64bh5dbq",
				CurrentMigrationHash: "n5dbh8fh58dh5b7h79h599h64bh5dbq",
			}},
			rawConfig: json.RawMessage(`{
				"datastoreEngine": "cockroachdb",
				"tlsSecretName":   "secret"
			}`),
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: false,
			expectStatusImage: "image:tag",
			expectNext:        nextKey,
		},
		{
			name: "valid config, new target migrationhash",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{
				Image:                "image:tag",
				CurrentMigrationHash: "old",
			}},
			rawConfig: json.RawMessage(`{
				"datastoreEngine": "cockroachdb",
				"tlsSecretName":   "secret"
			}`),
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectStatusImage: "image:tag",
			expectNext:        nextKey,
		},
		{
			name:          "valid config with label warnings",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Image: "image"}},
			rawConfig: json.RawMessage(`{
				"datastoreEngine": "cockroachdb",
				"extraPodLabels":  "wrong:format",
				"extraPodAnnotations":  "annotation:bad"
			}`),
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectConditions:  []string{"ConfigurationWarning"},
			expectStatusImage: "image:tag",
			expectNext:        nextKey,
		},
		{
			name:          "invalid config",
			currentStatus: &v1alpha1.SpiceDBCluster{},
			rawConfig: json.RawMessage(`{
				"badkey": "cockroachdb"
			}`),
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
			rawConfig: json.RawMessage(`{
				"datastoreEngine": "cockroachdb"
			}`),
			expectEvents:      []string{"Warning InvalidSpiceDBConfig invalid config: secret must be provided"},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name:          "invalid config, multiple reasons",
			currentStatus: &v1alpha1.SpiceDBCluster{},
			rawConfig: json.RawMessage(`{
				"nope": "cockroachdb"
			}`),
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
			rawConfig: json.RawMessage(`{
				"datastoreEngine": "cockroachdb",
				"tlsSecretName":   "secret"
			}`),
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectStatusImage: "image:tag",
			expectNext:        nextKey,
		},
		{
			name: "detects when warnings are fixed",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Image: "image", Conditions: []metav1.Condition{{
				Type:    "ConfigurationWarning",
				Status:  metav1.ConditionTrue,
				Message: "[couldn't parse extra pod label \"wrong:format\": values should be of the form k=v,k2=v2, couldn't parse extra pod annotation \"annotation:bad\": values should be of the form k=v,k2=v2, no TLS configured, consider setting \"tlsSecretName\"]",
			}}}},
			rawConfig: json.RawMessage(`{
				"datastoreEngine":     "cockroachdb",
				"tlsSecretName":       "secret",
				"extraPodLabels":      "correct=format,good=value",
				"extraPodAnnotations": "annotation=works"
			}`),
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectStatusImage: "image:tag",
			expectNext:        nextKey,
		},
		{
			name: "doesn't emit an event if validating failed for the same reasons",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:    "ValidatingFailed",
				Status:  metav1.ConditionTrue,
				Message: "Error validating config with secret hash \"\": [datastoreEngine is a required field, secret must be provided]",
			}}}},
			rawConfig: json.RawMessage(`{
				"nope": "cockroachdb"
			}`),
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
			rawConfig: json.RawMessage(`{
				"datastoreEngine": "cockroachdb"
			}`),
			expectEvents:      []string{"Warning InvalidSpiceDBConfig invalid config: secret must be provided"},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: true,
			expectDone:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeInterface{}
			recorder := record.NewFakeRecorder(1)
			patchCalled := false

			ctx := context.Background()
			ctx = QueueOps.WithValue(ctx, ctrls)
			ctx = CtxSecret.WithValue(ctx, tt.existingSecret)
			ctx = CtxClusterNN.WithValue(ctx, types.NamespacedName{Namespace: "test", Name: "test"})
			ctx = CtxClusterStatus.WithValue(ctx, tt.currentStatus)
			ctx = CtxCluster.WithValue(ctx, &v1alpha1.SpiceDBCluster{Spec: v1alpha1.ClusterSpec{Config: tt.rawConfig}})
			ctx = CtxOperatorConfig.WithValue(ctx, &config.OperatorConfig{ImageName: "image", ImageTag: "tag"})
			var called handler.Key
			h := &ValidateConfigHandler{
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

			cluster := CtxClusterStatus.MustValue(ctx)
			t.Log(cluster.Status.Conditions)
			for _, c := range tt.expectConditions {
				require.True(t, cluster.IsStatusConditionTrue(c))
			}
			require.Equal(t, len(tt.expectConditions), len(cluster.Status.Conditions))
			require.Equal(t, tt.expectStatusImage, cluster.Status.Image)
			require.Equal(t, tt.expectPatchStatus, patchCalled)
			require.Equal(t, tt.expectNext, called)
			require.Equal(t, tt.expectRequeue, ctrls.RequeueCallCount() == 1)
			require.Equal(t, tt.expectDone, ctrls.DoneCallCount() == 1)
			ExpectEvents(t, recorder, tt.expectEvents)
		})
	}
}
