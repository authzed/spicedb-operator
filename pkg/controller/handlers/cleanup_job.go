package handlers

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

type JobCleanupHandler struct {
	getJobPods func(ctx context.Context) []*corev1.Pod
	getJobs    func(ctx context.Context) []*batchv1.Job
	deleteJob  func(ctx context.Context, name string) error
	deletePod  func(ctx context.Context, name string) error
}

func NewJobCleanupHandler(
	getJobPods func(ctx context.Context) []*corev1.Pod,
	getJobs func(ctx context.Context) []*batchv1.Job,
	deleteJob func(ctx context.Context, name string) error,
	deletePod func(ctx context.Context, name string) error,
) handler.Handler {
	return handler.NewHandler(&JobCleanupHandler{
		getJobPods: getJobPods,
		getJobs:    getJobs,
		deleteJob:  deleteJob,
		deletePod:  deletePod,
	}, "cleanupJob")
}

func (s *JobCleanupHandler) Handle(ctx context.Context) {
	jobs := s.getJobs(ctx)
	pods := s.getJobPods(ctx)
	deployment := *CtxCurrentSpiceDeployment.MustValue(ctx)
	if deployment.Annotations == nil || len(jobs)+len(pods) == 0 {
		CtxHandlerControls.Done(ctx)
		return
	}

	jobNames := make(map[string]struct{})
	for _, j := range jobs {
		jobNames[j.Name] = struct{}{}
		if j.Annotations == nil {
			continue
		}
		if libctrl.SecureHashEqual(
			j.Annotations[metadata.SpiceDBMigrationRequirementsKey],
			deployment.Annotations[metadata.SpiceDBMigrationRequirementsKey]) &&
			jobConditionHasStatus(j, batchv1.JobComplete, corev1.ConditionTrue) {
			if err := s.deleteJob(ctx, j.GetName()); err != nil {
				CtxHandlerControls.RequeueAPIErr(ctx, err)
				return
			}
		}
	}

	for _, p := range pods {
		labels := p.GetLabels()
		if labels == nil {
			continue
		}
		jobName, ok := labels["job-name"]
		if !ok {
			continue
		}
		if _, ok := jobNames[jobName]; ok {
			// job still exists
			CtxHandlerControls.Requeue(ctx)
			return
		}

		if err := s.deletePod(ctx, p.GetName()); err != nil {
			CtxHandlerControls.RequeueAPIErr(ctx, err)
			return
		}
	}

	CtxHandlerControls.Done(ctx)
}
