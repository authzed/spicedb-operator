package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/queue/fake"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

func TestValidateSpecHandler(t *testing.T) {
	var nextKey handler.Key = "next"
	tests := []struct {
		name string

		cluster *v1alpha1.SpiceDBCluster

		expectNext        handler.Key
		expectEvents      []string
		expectPatchStatus bool
		expectConditions  []string
		expectRequeue     bool
		expectDone        bool
	}{
		{
			name: "neither SecretRef nor Credentials set - proceeds normally",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{},
			},
			expectNext: nextKey,
		},
		{
			name: "only SecretRef set - proceeds normally",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					SecretRef: "my-secret",
				},
			},
			expectNext: nextKey,
		},
		{
			name: "only Credentials set - proceeds normally",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{},
				},
			},
			expectNext: nextKey,
		},
		{
			name: "both SecretRef and Credentials set - sets InvalidConfig condition and stops",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					SecretRef:   "my-secret",
					Credentials: &v1alpha1.ClusterCredentials{},
				},
			},
			expectEvents:      []string{"Warning InvalidSpiceDBSpec invalid spec: secretName and credentials are mutually exclusive; set only one"},
			expectConditions:  []string{v1alpha1.ConditionValidatingFailed},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name: "credentials datastoreURI empty secretName without skip",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI: &v1alpha1.CredentialRef{SecretName: ""},
						PresharedKey: &v1alpha1.CredentialRef{SecretName: "psk"},
					},
				},
			},
			expectEvents:      []string{"Warning InvalidSpiceDBSpec invalid spec: credentials.datastoreURI: secretName must be set when skip is false"},
			expectConditions:  []string{v1alpha1.ConditionValidatingFailed},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name: "credentials presharedKey empty secretName without skip",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI: &v1alpha1.CredentialRef{SecretName: "db"},
						PresharedKey: &v1alpha1.CredentialRef{SecretName: ""},
					},
				},
			},
			expectEvents:      []string{"Warning InvalidSpiceDBSpec invalid spec: credentials.presharedKey: secretName must be set when skip is false"},
			expectConditions:  []string{v1alpha1.ConditionValidatingFailed},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name: "credentials migrationSecrets empty secretName without skip",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI:     &v1alpha1.CredentialRef{SecretName: "db"},
						PresharedKey:     &v1alpha1.CredentialRef{SecretName: "psk"},
						MigrationSecrets: &v1alpha1.CredentialRef{SecretName: ""},
					},
				},
			},
			expectEvents:      []string{"Warning InvalidSpiceDBSpec invalid spec: credentials.migrationSecrets: secretName must be set when skip is false"},
			expectConditions:  []string{v1alpha1.ConditionValidatingFailed},
			expectPatchStatus: true,
			expectDone:        true,
		},
		{
			name: "both set but condition already present with same message - no-op, done",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					SecretRef:   "my-secret",
					Credentials: &v1alpha1.ClusterCredentials{},
				},
				Status: v1alpha1.ClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:    v1alpha1.ConditionValidatingFailed,
							Status:  metav1.ConditionTrue,
							Reason:  "InvalidConfig",
							Message: `Error validating config with secret hash "": secretName and credentials are mutually exclusive; set only one`,
						},
					},
				},
			},
			expectConditions:  []string{v1alpha1.ConditionValidatingFailed},
			expectPatchStatus: false,
			expectDone:        true,
		},
		{
			name: "credentials datastoreURI empty secretName condition already present - no-op, done",
			cluster: &v1alpha1.SpiceDBCluster{
				Spec: v1alpha1.ClusterSpec{
					Credentials: &v1alpha1.ClusterCredentials{
						DatastoreURI: &v1alpha1.CredentialRef{SecretName: ""},
					},
				},
				Status: v1alpha1.ClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:    v1alpha1.ConditionValidatingFailed,
							Status:  metav1.ConditionTrue,
							Reason:  "InvalidConfig",
							Message: `Error validating config with secret hash "": credentials.datastoreURI: secretName must be set when skip is false`,
						},
					},
				},
			},
			expectConditions:  []string{v1alpha1.ConditionValidatingFailed},
			expectPatchStatus: false,
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
			ctx = CtxCluster.WithValue(ctx, tt.cluster)

			var called handler.Key
			h := &ValidateSpecHandler{
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
			require.Equal(t, tt.expectPatchStatus, patchCalled)
			require.Equal(t, tt.expectNext, called)
			require.Equal(t, tt.expectRequeue, ctrls.RequeueCallCount() == 1)
			require.Equal(t, tt.expectDone, ctrls.DoneCallCount() == 1)
			ExpectEvents(t, recorder, tt.expectEvents)
		})
	}
}
