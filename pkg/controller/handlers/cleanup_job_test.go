package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/authzed/spicedb-operator/pkg/libctrl/fake"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func TestCleanupJobsHandler(t *testing.T) {
	tests := []struct {
		name string

		existingDeployment *appsv1.Deployment
		existingJobPods    []*corev1.Pod
		existingJobs       []*batchv1.Job

		expectDeletedPods []string
		expectDeletedJobs []string
		expectRequeueErr  error
		expectRequeue     bool
		expectDone        bool
	}{
		{
			name:               "don't clean up if deployment has no annotations",
			existingDeployment: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: nil}},
			existingJobs:       []*batchv1.Job{{}},
			existingJobPods:    []*corev1.Pod{{}},
			expectDone:         true,
		},
		{
			name:               "don't clean up if no jobs and no pods",
			existingDeployment: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"an": "annotation"}}},
			existingJobs:       []*batchv1.Job{},
			existingJobPods:    []*corev1.Pod{},
			expectDone:         true,
		},
		{
			name: "deletes completed jobs with matching migration hash",
			existingDeployment: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "test",
			}}},
			existingJobs: []*batchv1.Job{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mjob",
					Annotations: map[string]string{
						metadata.SpiceDBMigrationRequirementsKey: "test",
					},
				},
				Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				}}},
			}},
			expectDeletedJobs: []string{"mjob"},
			expectDone:        true,
		},
		{
			name: "doesn't delete completed jobs without migration hash",
			existingDeployment: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "test",
			}}},
			existingJobs: []*batchv1.Job{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mjob",
					Annotations: map[string]string{
						metadata.SpiceDBMigrationRequirementsKey: "different",
					},
				},
				Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				}}},
			}},
			expectDone: true,
		},
		{
			name: "doesn't delete incomplete jobs with matching migration hash",
			existingDeployment: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				metadata.SpiceDBMigrationRequirementsKey: "test",
			}}},
			existingJobs: []*batchv1.Job{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mjob",
					Annotations: map[string]string{
						metadata.SpiceDBMigrationRequirementsKey: "test",
					},
				},
				Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionFalse,
				}}},
			}},
			expectDone: true,
		},
		{
			name:               "deletes pods with missing jobs",
			existingDeployment: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"a": "test"}}},
			existingJobPods: []*corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mpod",
					Labels: map[string]string{
						"job-name": "mjob",
					},
				},
			}},
			expectDeletedPods: []string{"mpod"},
			expectDone:        true,
		},
		{
			name:               "doesn't delete pods with extant jobs",
			existingDeployment: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"a": "test"}}},
			existingJobs: []*batchv1.Job{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mjob",
					Annotations: map[string]string{
						metadata.SpiceDBMigrationRequirementsKey: "test",
					},
				},
				Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionFalse,
				}}},
			}},
			existingJobPods: []*corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mpod",
					Labels: map[string]string{
						"job-name": "mjob",
					},
				},
			}},
			expectRequeue: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := &fake.FakeControlAll{}

			ctx := context.Background()
			ctx = CtxHandlerControls.WithValue(ctx, ctrls)
			ctx = CtxCurrentSpiceDeployment.WithValue(ctx, tt.existingDeployment)

			if tt.expectDeletedJobs == nil {
				tt.expectDeletedJobs = make([]string, 0)
			}
			if tt.expectDeletedPods == nil {
				tt.expectDeletedPods = make([]string, 0)
			}
			deletedPods := make([]string, 0)
			deletedJobs := make([]string, 0)
			h := &JobCleanupHandler{
				getJobs: func(ctx context.Context) []*batchv1.Job {
					return tt.existingJobs
				},
				getJobPods: func(ctx context.Context) []*corev1.Pod {
					return tt.existingJobPods
				},
				deletePod: func(ctx context.Context, name string) error {
					deletedPods = append(deletedPods, name)
					return nil
				},
				deleteJob: func(ctx context.Context, name string) error {
					deletedJobs = append(deletedJobs, name)
					return nil
				},
			}
			h.Handle(ctx)

			require.Equal(t, tt.expectRequeue, ctrls.RequeueCallCount() == 1)
			require.Equal(t, tt.expectDone, ctrls.DoneCallCount() == 1)
			require.Equal(t, tt.expectDeletedJobs, deletedJobs)
			require.Equal(t, tt.expectDeletedPods, deletedPods)
			if tt.expectRequeueErr != nil {
				require.Equal(t, 1, ctrls.RequeueErrCallCount())
				require.Equal(t, tt.expectRequeueErr, ctrls.RequeueErrArgsForCall(0))
			}
		})
	}
}
