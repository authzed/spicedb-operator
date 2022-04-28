package handlers

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
)

const EventMigrationsComplete = "MigrationsCompleted"

type WaitForMigrationsHandler struct {
	libctrl.ControlRequeueAfter
	recorder              record.EventRecorder
	nextSelfPause         handler.ContextHandler
	nextDeploymentHandler handler.ContextHandler
}

func NewWaitForMigrationsHandler(ctrls libctrl.HandlerControls, recorder record.EventRecorder, next handler.Handlers) handler.Handler {
	return handler.NewHandler(&WaitForMigrationsHandler{
		ControlRequeueAfter:   ctrls,
		recorder:              recorder,
		nextSelfPause:         HandlerSelfPauseKey.MustFind(next),
		nextDeploymentHandler: HandlerDeploymentKey.MustFind(next),
	}, "waitForMigrations")
}

func (m *WaitForMigrationsHandler) Handle(ctx context.Context) {
	job := handlercontext.CtxCurrentMigrationJob.MustValue(ctx)

	// if migration failed entirely, pause so we can diagnose
	if c := findJobCondition(job, batchv1.JobFailed); c != nil && c.Status == corev1.ConditionTrue {
		currentStatus := handlercontext.CtxClusterStatus.MustValue(ctx)
		config := handlercontext.CtxConfig.MustValue(ctx)
		err := fmt.Errorf("migration job failed: %s", c.Message)
		runtime.HandleError(err)
		meta.SetStatusCondition(&currentStatus.Status.Conditions, v1alpha1.NewMigrationFailedCondition(config.DatastoreEngine, "head", err))
		handlercontext.CtxSelfPauseObject.WithValue(ctx, currentStatus)
		m.nextSelfPause.Handle(ctx)
		return
	}

	// if done, go to the nextDeploymentHandler step
	if jobConditionHasStatus(job, batchv1.JobComplete, corev1.ConditionTrue) {
		m.recorder.Eventf(handlercontext.CtxClusterStatus.MustValue(ctx), corev1.EventTypeNormal, EventMigrationsComplete, "Migrations completed for %s", handlercontext.CtxConfig.MustValue(ctx).TargetSpiceDBImage)
		m.nextDeploymentHandler.Handle(ctx)
		return
	}

	// otherwise, it's created but still running, just wait
	m.RequeueAfter(5 * time.Second)
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
