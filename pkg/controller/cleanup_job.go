package controller

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/authzed/controller-idioms/hash"
	"github.com/authzed/controller-idioms/typed"

	"github.com/authzed/spicedb-operator/pkg/metadata"
)

type JobCleanupHandler struct {
	registry   *typed.Registry
	getJobPods func(ctx context.Context) []*corev1.Pod
	getJobs    func(ctx context.Context) []*batchv1.Job
	deleteJob  func(ctx context.Context, nn types.NamespacedName) error
	deletePod  func(ctx context.Context, nn types.NamespacedName) error
}

func (s *JobCleanupHandler) Handle(ctx context.Context) {
	jobs := s.getJobs(ctx)
	pods := s.getJobPods(ctx)
	deployment := *CtxCurrentSpiceDeployment.MustValue(ctx)
	if deployment.Annotations == nil || len(jobs)+len(pods) == 0 {
		QueueOps.Done(ctx)
		return
	}

	jobNames := make(map[string]struct{})
	for _, j := range jobs {
		jobNames[j.Name] = struct{}{}
		if j.Annotations == nil {
			continue
		}
		if hash.SecureEqual(
			j.Annotations[metadata.SpiceDBMigrationRequirementsKey],
			deployment.Annotations[metadata.SpiceDBMigrationRequirementsKey]) &&
			jobConditionHasStatus(j, batchv1.JobComplete, corev1.ConditionTrue) {
			if err := s.deleteJob(ctx, types.NamespacedName{
				Namespace: j.GetNamespace(),
				Name:      j.GetName(),
			}); err != nil {
				QueueOps.RequeueAPIErr(ctx, err)
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
			QueueOps.Requeue(ctx)
			return
		}

		if err := s.deletePod(ctx, types.NamespacedName{
			Namespace: p.GetNamespace(),
			Name:      p.GetName(),
		}); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
	}

	QueueOps.Done(ctx)
}
