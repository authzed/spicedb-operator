package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

const EventMigrationsComplete = "MigrationsCompleted"

type WaitForMigrationsHandler struct {
	recorder              record.EventRecorder
	nextSelfPause         handler.ContextHandler
	nextDeploymentHandler handler.ContextHandler
}

func (m *WaitForMigrationsHandler) Handle(ctx context.Context) {
	job := CtxCurrentMigrationJob.MustValue(ctx)

	// if migration failed entirely, pause so we can diagnose
	if c := findJobCondition(job, batchv1.JobFailed); c != nil && c.Status == corev1.ConditionTrue {
		currentStatus := CtxCluster.MustValue(ctx)
		config := CtxConfig.MustValue(ctx)
		err := fmt.Errorf("migration job failed: %s", c.Message)
		runtime.HandleError(err)
		currentStatus.SetStatusCondition(v1alpha1.NewMigrationFailedCondition(config.DatastoreEngine, "head", err))
		ctx = CtxSelfPauseObject.WithValue(ctx, currentStatus)
		m.nextSelfPause.Handle(ctx)
		return
	}

	// if done, go to the nextDeploymentHandler step
	if jobConditionHasStatus(job, batchv1.JobComplete, corev1.ConditionTrue) {
		m.recorder.Eventf(CtxCluster.MustValue(ctx), corev1.EventTypeNormal, EventMigrationsComplete, "Migrations completed for %s", CtxConfig.MustValue(ctx).TargetSpiceDBImage)
		m.nextDeploymentHandler.Handle(ctx)
		return
	}

	// otherwise, it's created but still running, just wait
	QueueOps.RequeueAfter(ctx, 5*time.Second)
}

func findJobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) *batchv1.JobCondition {
	if job == nil {
		return nil
	}
	conditions := job.Status.Conditions
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func jobConditionHasStatus(job *batchv1.Job, conditionType batchv1.JobConditionType, status corev1.ConditionStatus) bool {
	c := findJobCondition(job, conditionType)
	if c == nil {
		return false
	}
	return c.Status == status
}
