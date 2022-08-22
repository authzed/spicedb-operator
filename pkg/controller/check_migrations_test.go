package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/libctrl/fake"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func TestCheckMigrationsHandler(t *testing.T) {
	tests := []struct {
		name string

		config              config.Config
		migrationHash       string
		existingJobs        []*batchv1.Job
		existingDeployments []*appsv1.Deployment

		expectEvents     []string
		expectRequeueErr error
		expectNext       handler.Key
	}{
		{
			name:                "run migrations if no job, no deployment",
			config:              config.Config{MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"}},
			migrationHash:       "hash",
			existingJobs:        []*batchv1.Job{},
			existingDeployments: []*appsv1.Deployment{},
			expectEvents:        []string{"Normal RunningMigrations Running migration job for test"},
			expectNext:          HandlerMigrationRunKey,
		},
		{
			name:          "run migration if non-matching job and no deployment",
			migrationHash: "hash",
			config:        config.Config{MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"}},
			existingJobs: []*batchv1.Job{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "nope",
			}}}},
			existingDeployments: []*appsv1.Deployment{},
			expectEvents:        []string{"Normal RunningMigrations Running migration job for test"},
			expectNext:          HandlerMigrationRunKey,
		},
		{
			name:          "wait for migrations if matching job but no deployment",
			config:        config.Config{MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"}},
			migrationHash: "hash",
			existingJobs: []*batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
						metadata.SpiceDBMigrationRequirementsKey: "hash",
					}},
				}, {
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
						metadata.SpiceDBMigrationRequirementsKey: "nope",
					}},
				},
			},
			existingDeployments: []*appsv1.Deployment{},
			expectNext:          HandlerWaitForMigrationsKey,
		},
		{
			name:                "check deployment if skipMigrations = true",
			config:              config.Config{SpiceConfig: config.SpiceConfig{SkipMigrations: true}},
			existingDeployments: []*appsv1.Deployment{{}},
			expectNext:          HandlerDeploymentKey,
		},
		{
			name:          "check deployment if deployment is up to date",
			migrationHash: "hash",
			config:        config.Config{MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"}},
			existingDeployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "hash",
			}}}},
			expectNext: HandlerDeploymentKey,
		},
		{
			name:          "check deployment if deployment is up to date job is up to date",
			config:        config.Config{MigrationConfig: config.MigrationConfig{TargetSpiceDBImage: "test"}},
			migrationHash: "hash",
			existingJobs: []*batchv1.Job{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "hash",
			}}}},
			existingDeployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "hash",
			}}}},
			expectNext: HandlerDeploymentKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeControlAll{}

			ctx := CtxConfig.WithValue(context.Background(), &tt.config)
			ctx = CtxHandlerControls.WithValue(ctx, ctrls)
			ctx = CtxCluster.WithValue(ctx, &v1alpha1.SpiceDBCluster{})
			ctx = CtxJobs.WithValue(ctx, tt.existingJobs)
			ctx = CtxDeployments.WithValue(ctx, tt.existingDeployments)
			ctx = CtxMigrationHash.WithValue(ctx, "hash")

			recorder := record.NewFakeRecorder(1)

			var called handler.Key
			h := &MigrationCheckHandler{
				recorder: recorder,
				nextDeploymentHandler: handler.ContextHandlerFunc(func(ctx context.Context) {
					called = HandlerDeploymentKey
				}),
				nextWaitForJobHandler: handler.ContextHandlerFunc(func(ctx context.Context) {
					called = HandlerWaitForMigrationsKey
				}),
				nextMigrationRunHandler: handler.ContextHandlerFunc(func(ctx context.Context) {
					called = HandlerMigrationRunKey
				}),
			}
			h.Handle(ctx)

			require.Equal(t, tt.expectNext, called)
			ExpectEvents(t, recorder, tt.expectEvents)

			if tt.expectRequeueErr != nil {
				require.Equal(t, 1, ctrls.RequeueErrCallCount())
				require.Equal(t, tt.expectRequeueErr, ctrls.RequeueErrArgsForCall(0))
			}
		})
	}
}
