package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/queue/fake"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func TestRunMigrationHandler(t *testing.T) {
	testHash := "hashhashhashhashhash"
	matchingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: testHash,
		}},
	}
	tests := []struct {
		name string

		clusterStatus *v1alpha1.SpiceDBCluster
		config        config.Config
		existingJobs  []*batchv1.Job
		migrationHash string
		jobApplyErr   error
		jobDeleteErr  error

		expectApply                  bool
		expectDelete                 bool
		expectNext                   bool
		expectCtxClusterStatus       *v1alpha1.ClusterStatus
		expectCtxCurrentMigrationJob *batchv1.Job
		expectRequeueErr             error
		expectRequeue                bool
		expectRequeueAfter           time.Duration
		expectDone                   bool
	}{
		{
			name:               "creates if no matching job",
			clusterStatus:      &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{}},
			existingJobs:       []*batchv1.Job{},
			migrationHash:      testHash,
			expectApply:        true,
			expectRequeueAfter: 5 * time.Second,
		},
		{
			name:          "no-ops if exactly 1 matching job",
			clusterStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{}},
			existingJobs:  []*batchv1.Job{matchingJob},
			migrationHash: testHash,
			expectNext:    true,
		},
		{
			name:          "deletes non-matching job and creates matching job",
			clusterStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{}},
			existingJobs: []*batchv1.Job{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBMigrationRequirementsKey: "nope",
				}},
			}},
			migrationHash: testHash,
			expectDelete:  true,
			expectApply:   true,
		},
		{
			name:          "deletes non-matching job and leaves existing matching job",
			clusterStatus: &v1alpha1.SpiceDBCluster{Status: v1alpha1.ClusterStatus{}},
			existingJobs: []*batchv1.Job{matchingJob, {
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					metadata.SpiceDBMigrationRequirementsKey: "nope",
				}},
			}},
			migrationHash: testHash,
			expectDelete:  true,
			expectNext:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeInterface{}
			applyCalled := false
			deleteCalled := false
			nextCalled := false

			ctx := CtxClusterStatus.WithValue(context.Background(), tt.clusterStatus)
			ctx = QueueOps.WithValue(ctx, ctrls)
			ctx = CtxConfig.WithValue(ctx, &tt.config)
			ctx = CtxJobs.WithBox(ctx)
			ctx = CtxJobs.WithValue(ctx, tt.existingJobs)
			ctx = CtxMigrationHash.WithValue(ctx, tt.migrationHash)

			h := &MigrationRunHandler{
				patchStatus: func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
					return nil
				},
				applyJob: func(ctx context.Context, job *applybatchv1.JobApplyConfiguration) error {
					applyCalled = true
					return tt.jobApplyErr
				},
				deleteJob: func(ctx context.Context, nn types.NamespacedName) error {
					deleteCalled = true
					return tt.jobDeleteErr
				},
				next: handler.ContextHandlerFunc(func(ctx context.Context) {
					nextCalled = true
					require.Equal(t, matchingJob, CtxCurrentMigrationJob.MustValue(ctx))
				}),
			}
			h.Handle(ctx)

			require.True(t, CtxClusterStatus.MustValue(ctx).IsStatusConditionTrue(v1alpha1.ConditionTypeMigrating))
			require.Equal(t, tt.expectApply, applyCalled)
			require.Equal(t, tt.expectDelete, deleteCalled)
			require.Equal(t, tt.expectNext, nextCalled)
			if tt.expectRequeueErr != nil {
				require.Equal(t, 1, ctrls.RequeueErrCallCount())
				require.Equal(t, tt.expectRequeueErr, ctrls.RequeueErrArgsForCall(0))
			}
			require.Equal(t, tt.expectRequeue, ctrls.RequeueCallCount() == 1)
			if tt.expectRequeueAfter != 0 {
				require.Equal(t, 1, ctrls.RequeueAfterCallCount())
				require.Equal(t, tt.expectRequeueAfter, ctrls.RequeueAfterArgsForCall(0))
			}
		})
	}
}
