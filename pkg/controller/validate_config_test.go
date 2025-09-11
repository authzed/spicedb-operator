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
	"github.com/authzed/spicedb-operator/pkg/updates"
)

func TestValidateConfigHandler(t *testing.T) {
	var nextKey handler.Key = "next"
	tests := []struct {
		name string

		cluster        *v1alpha1.SpiceDBCluster
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
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
					"datastoreEngine": "cockroachdb",
					"tlsSecretName":   "secret"
				}`)},
				Status: v1alpha1.ClusterStatus{
					Image:                "image:v1",
					Migration:            "head",
					TargetMigrationHash:  "69066f71d9cf4a1c",
					CurrentMigrationHash: "69066f71d9cf4a1c",
					CurrentVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
					},
					AvailableVersions: []v1alpha1.SpiceDBVersion{},
				},
			},
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: false,
			expectStatusImage: "image:v1",
			expectNext:        nextKey,
		},
		{
			name: "valid config, new target migrationhash",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
					"image": "image:v1",
					"datastoreEngine": "cockroachdb",
					"tlsSecretName":   "secret"
				}`)},
				Status: v1alpha1.ClusterStatus{
					Image:                "image:v1",
					CurrentMigrationHash: "old",
				},
			},
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectStatusImage: "image:v1",
			expectNext:        nextKey,
		},
		{
			name: "valid config with label warnings",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
					"datastoreEngine": "cockroachdb",
					"extraPodLabels":  "wrong:format",
					"extraPodAnnotations":  "annotation:bad"
				}`)},
				Status: v1alpha1.ClusterStatus{Image: "image"},
			},
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectConditions:  []string{"ConfigurationWarning"},
			expectStatusImage: "image:v1",
			expectNext:        nextKey,
		},
		{
			name: "invalid config, missing secret",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
				"datastoreEngine": "cockroachdb"
			}`)},
			},
			expectEvents:      []string{"Warning InvalidSpiceDBConfig invalid config: secret must be provided"},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name: "invalid config, multiple reasons",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
					"nope": "cockroachdb"
				}`)},
			},
			expectEvents:      []string{"Warning InvalidSpiceDBConfig invalid config: [datastoreEngine is a required field, couldn't find channel for datastore \"\": no channel found for datastore \"\", no update found in channel, secret must be provided]"},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name: "detects when config is now valid",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
					"datastoreEngine": "cockroachdb",
					"tlsSecretName":   "secret"
				}`)},
				Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
					Type:    "ValidatingFailed",
					Status:  metav1.ConditionTrue,
					Message: "invalid config: [datastoreEngine is a required field, secret must be provided]",
				}}},
			},
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectStatusImage: "image:v1",
			expectNext:        nextKey,
		},
		{
			name: "detects when warnings are fixed",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
					"datastoreEngine":     "cockroachdb",
					"tlsSecretName":       "secret",
					"extraPodLabels":      "correct=format,good=value",
					"extraPodAnnotations": "annotation=works"
				}`)},
				Status: v1alpha1.ClusterStatus{Image: "image", Conditions: []metav1.Condition{{
					Type:    "ConfigurationWarning",
					Status:  metav1.ConditionTrue,
					Message: "[couldn't parse extra pod label \"wrong:format\": values should be of the form k=v,k2=v2, couldn't parse extra pod annotation \"annotation:bad\": values should be of the form k=v,k2=v2, no TLS configured, consider setting \"tlsSecretName\"]",
				}}},
			},
			existingSecret: &corev1.Secret{
				Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("testtest"),
				},
			},
			expectPatchStatus: true,
			expectStatusImage: "image:v1",
			expectNext:        nextKey,
		},
		{
			name: "doesn't emit an event if validating failed for the same reasons",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
						"nope":     "cockroachdb"
					}`)},
				Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
					Type:    "ValidatingFailed",
					Status:  metav1.ConditionTrue,
					Message: "Error validating config with secret hash \"\": [datastoreEngine is a required field, couldn't find channel for datastore \"\": no channel found for datastore \"\", no update found in channel, secret must be provided]",
				}}},
			},
			expectConditions:  []string{"ValidatingFailed"},
			expectPatchStatus: false,
			expectDone:        true,
		},
		{
			name: "emits an event if validating failed for different reasons",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{Config: json.RawMessage(`{
						"datastoreEngine":     "cockroachdb"
					}`)},
				Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
					Type:    "ValidatingFailed",
					Status:  metav1.ConditionTrue,
					Message: "Error validating config with secret hash \"\": [datastoreEngine is a required field, secret must be provided]",
				}}},
			},
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
			ctx = CtxCluster.WithValue(ctx, tt.cluster)
			ctx = CtxCluster.WithValue(ctx, tt.cluster)
			ctx = CtxOperatorConfig.WithValue(ctx, &config.OperatorConfig{
				ImageName: "image",
				UpdateGraph: updates.UpdateGraph{
					Channels: []updates.Channel{
						{
							Name:     "cockroachdb",
							Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
							Nodes: []updates.State{
								{ID: "v1", Tag: "v1"},
							},
							Edges: map[string][]string{"v1": {}},
						},
					},
				},
			})
			var called handler.Key
			h := &ValidateConfigHandler{
				patchStatus: func(_ context.Context, _ *v1alpha1.SpiceDBCluster) error {
					patchCalled = true
					return nil
				},
				recorder: recorder,
				next: handler.ContextHandlerFunc(func(_ context.Context) {
					called = nextKey
				}),
			}
			h.Handle(ctx)

			cluster := CtxCluster.MustValue(ctx)
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
