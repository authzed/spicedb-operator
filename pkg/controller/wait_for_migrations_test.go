package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/libctrl/fake"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
)

func TestWaitForMigrationsHandler(t *testing.T) {
	tests := []struct {
		name string

		migrationJob *batchv1.Job

		expectNext         handler.Key
		expectRequeueAfter time.Duration
		expectEvents       []string
	}{
		{
			name:               "job is still running, requeue with delay",
			migrationJob:       &batchv1.Job{},
			expectRequeueAfter: 5 * time.Second,
		},
		{
			name: "job is done, check deployments",
			migrationJob: &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}}}},
			expectEvents: []string{
				"Normal MigrationsCompleted Migrations completed for test",
			},
			expectNext: HandlerDeploymentKey,
		},
		{
			name: "job failed, pause reconciliation",
			migrationJob: &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobFailed,
				Status: corev1.ConditionTrue,
			}}}},
			expectNext: HandlerSelfPauseKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeControlAll{}

			ctx := CtxConfig.WithValue(context.Background(), &config.Config{MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"}})
			ctx = CtxHandlerControls.WithValue(ctx, ctrls)
			ctx = CtxClusterStatus.WithValue(ctx, &v1alpha1.SpiceDBCluster{})
			ctx = CtxCurrentMigrationJob.WithValue(ctx, tt.migrationJob)

			recorder := record.NewFakeRecorder(1)

			var called handler.Key
			h := &WaitForMigrationsHandler{
				recorder: recorder,
				nextSelfPause: handler.ContextHandlerFunc(func(ctx context.Context) {
					called = HandlerSelfPauseKey
				}),
				nextDeploymentHandler: handler.ContextHandlerFunc(func(ctx context.Context) {
					called = HandlerDeploymentKey
				}),
			}
			h.Handle(ctx)

			require.Equal(t, tt.expectNext, called)
			ExpectEvents(t, recorder, tt.expectEvents)

			if tt.expectRequeueAfter != 0 {
				require.Equal(t, 1, ctrls.RequeueAfterCallCount())
				require.Equal(t, tt.expectRequeueAfter, ctrls.RequeueAfterArgsForCall(0))
			}
		})
	}
}
