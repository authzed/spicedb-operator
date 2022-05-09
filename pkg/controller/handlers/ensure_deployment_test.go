package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl/fake"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func TestEnsureDeploymentHandler(t *testing.T) {
	var nextKey handler.Key = "next"
	tests := []struct {
		name string

		migrationHash       string
		existingDeployments []*appsv1.Deployment
		currentStatus       *v1alpha1.SpiceDBCluster

		expectNext        handler.Key
		expectStatus      *v1alpha1.SpiceDBCluster
		expectRequeueErr  error
		expectApply       bool
		expectDelete      bool
		expectPatchStatus bool
	}{
		{
			name:          "creates if no deployments",
			migrationHash: "testtesttesttest",
			expectApply:   true,
			expectNext:    nextKey,
		},
		{
			name:                "creates if no matching deployment",
			migrationHash:       "testtesttesttest",
			existingDeployments: []*appsv1.Deployment{{}},
			expectApply:         true,
			expectNext:          nextKey,
		},
		{
			name:          "no-ops if one matching deployment",
			migrationHash: "testtesttesttest",
			existingDeployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBConfigKey: "n687h59dh569h79h54bh67fh67bh7q",
			}}}},
			expectNext: nextKey,
		},
		{
			name:          "deletes extra deployments if a matching deployment exists",
			migrationHash: "testtesttesttest",
			existingDeployments: []*appsv1.Deployment{{}, {ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBConfigKey: "n687h59dh569h79h54bh67fh67bh7q",
			}}}},
			expectDelete: true,
			expectNext:   nextKey,
		},
		{
			name: "removes migrating condition if present",
			currentStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{{
				Type:   v1alpha1.ConditionTypeMigrating,
				Status: metav1.ConditionTrue,
			}}}},
			migrationHash:     "testtesttesttest",
			expectApply:       true,
			expectNext:        nextKey,
			expectPatchStatus: true,
			expectStatus:      &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{Conditions: []metav1.Condition{}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeControlAll{}
			applyCalled := false
			deleteCalled := false
			patchCalled := false

			if tt.currentStatus == nil {
				tt.currentStatus = &v1alpha1.SpiceDBCluster{}
			}
			if tt.expectStatus == nil {
				tt.expectStatus = &v1alpha1.SpiceDBCluster{}
			}

			ctx := handlercontext.CtxConfig.WithValue(context.Background(), &config.Config{MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"}})
			ctx = handlercontext.CtxClusterStatus.WithValue(ctx, tt.currentStatus)
			ctx = handlercontext.CtxMigrationHash.WithValue(ctx, tt.migrationHash)
			ctx = handlercontext.CtxDeployments.WithValue(ctx, tt.existingDeployments)

			var called handler.Key
			h := &DeploymentHandler{
				ControlAll: ctrls,
				applyDeployment: func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error) {
					applyCalled = true
					return nil, nil
				},
				deleteDeployment: func(ctx context.Context, name string) error {
					deleteCalled = true
					return nil
				},
				patchStatus: func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
					patchCalled = true
					return nil
				},
				next: handler.ContextHandlerFunc(func(ctx context.Context) {
					called = nextKey
				}),
			}
			h.Handle(ctx)

			require.Equal(t, tt.expectStatus, handlercontext.CtxClusterStatus.MustValue(ctx))
			require.Equal(t, tt.expectApply, applyCalled)
			require.Equal(t, tt.expectDelete, deleteCalled)
			require.Equal(t, tt.expectPatchStatus, patchCalled)
			require.Equal(t, tt.expectNext, called)
			if tt.expectRequeueErr != nil {
				require.Equal(t, 1, ctrls.RequeueErrCallCount())
				require.Equal(t, tt.expectRequeueErr, ctrls.RequeueErrArgsForCall(0))
			}
		})
	}
}
