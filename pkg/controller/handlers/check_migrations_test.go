package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl/fake"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
	"github.com/authzed/spicedb-operator/pkg/spicecluster"
)

func TestCheckMigrationsHandler(t *testing.T) {
	tests := []struct {
		name string

		config              spicecluster.Config
		migrationHash       string
		existingJobs        []*batchv1.Job
		existingDeployments []*appsv1.Deployment

		expectEvents     []string
		expectRequeueErr error
		expectNext       handler.Key
	}{
		{
			name:                "run migrations if no job, no deployment",
			config:              spicecluster.Config{MigrationConfig: spicecluster.MigrationConfig{TargetSpiceDBImage: "test"}},
			migrationHash:       "hash",
			existingJobs:        []*batchv1.Job{},
			existingDeployments: []*appsv1.Deployment{},
			expectEvents:        []string{"Normal RunningMigrations Running migration job for test"},
			expectNext:          HandlerMigrationRunKey,
		},
		{
			name:          "run migration if non-matching job and no deployment",
			migrationHash: "hash",
			config:        spicecluster.Config{MigrationConfig: spicecluster.MigrationConfig{TargetSpiceDBImage: "test"}},
			existingJobs: []*batchv1.Job{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "nope",
			}}}},
			existingDeployments: []*appsv1.Deployment{},
			expectEvents:        []string{"Normal RunningMigrations Running migration job for test"},
			expectNext:          HandlerMigrationRunKey,
		},
		{
			name:          "wait for migrations if matching job but no deployment",
			config:        spicecluster.Config{MigrationConfig: spicecluster.MigrationConfig{TargetSpiceDBImage: "test"}},
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
			config:              spicecluster.Config{SpiceConfig: spicecluster.SpiceConfig{SkipMigrations: true}},
			existingDeployments: []*appsv1.Deployment{{}},
			expectNext:          HandlerDeploymentKey,
		},
		{
			name:          "check deployment if deployment is up to date",
			migrationHash: "hash",
			config:        spicecluster.Config{MigrationConfig: spicecluster.MigrationConfig{TargetSpiceDBImage: "test"}},
			existingDeployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "hash",
			}}}},
			expectNext: HandlerDeploymentKey,
		},
		{
			name:          "check deployment if deployment is up to date job is up to date",
			config:        spicecluster.Config{MigrationConfig: spicecluster.MigrationConfig{TargetSpiceDBImage: "test"}},
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

			ctx := handlercontext.CtxConfig.WithValue(context.Background(), &tt.config)
			ctx = handlercontext.CtxClusterStatus.WithValue(ctx, &v1alpha1.SpiceDBCluster{})
			ctx = handlercontext.CtxJobs.WithValue(ctx, tt.existingJobs)
			ctx = handlercontext.CtxDeployments.WithValue(ctx, tt.existingDeployments)
			ctx = handlercontext.CtxMigrationHash.WithValue(ctx, "hash")

			recorder := record.NewFakeRecorder(1)

			var called handler.Key
			h := &MigrationCheckHandler{
				ControlRequeueErr: ctrls,
				recorder:          recorder,
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
