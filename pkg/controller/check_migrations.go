package controller

import (
	"context"

	"golang.org/x/exp/slices"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

const (
	EventRunningMigrations = "RunningMigrations"

	HandlerDeploymentKey        handler.Key = "deploymentChain"
	HandlerMigrationRunKey      handler.Key = "runMigration"
	HandlerWaitForMigrationsKey handler.Key = "waitForMigrationChain"
)

type MigrationCheckHandler struct {
	recorder record.EventRecorder

	nextMigrationRunHandler handler.ContextHandler
	nextWaitForJobHandler   handler.ContextHandler
	nextDeploymentHandler   handler.ContextHandler
}

func (m *MigrationCheckHandler) Handle(ctx context.Context) {
	migrationHash := CtxMigrationHash.MustValue(ctx)

	hasJob := false
	hasDeployment := false
	for _, d := range CtxDeployments.MustValue(ctx) {
		if d.Annotations != nil && hash.SecureEqual(d.Annotations[metadata.SpiceDBMigrationRequirementsKey], migrationHash) {
			hasDeployment = true
			break
		}
	}
	for _, j := range CtxJobs.MustValue(ctx) {
		if j.Annotations != nil && hash.SecureEqual(j.Annotations[metadata.SpiceDBMigrationRequirementsKey], migrationHash) {
			hasJob = true
			ctx = CtxCurrentMigrationJob.WithValue(ctx, j)
			break
		}
	}

	// don't handle migrations at all if `skipMigrations` is set, if the
	// `memory` datastore is used, or if the update graph says there are no
	// migrations for this step.
	config := CtxConfig.MustValue(ctx)
	if config.SkipMigrations || config.DatastoreEngine == "memory" {
		m.nextDeploymentHandler.Handle(ctx)
		return
	}
	status := CtxClusterStatus.MustValue(ctx).Status
	if status.CurrentVersion != nil && !slices.Contains(status.CurrentVersion.Attributes, v1alpha1.SpiceDBVersionAttributesMigration) {
		m.nextDeploymentHandler.Handle(ctx)
		return
	}

	// if there's no job and no (updated) deployment, create the job
	if !hasDeployment && !hasJob {
		m.recorder.Eventf(CtxClusterStatus.MustValue(ctx), corev1.EventTypeNormal, EventRunningMigrations, "Running migration job for %s", CtxConfig.MustValue(ctx).TargetSpiceDBImage)
		m.nextMigrationRunHandler.Handle(ctx)
		return
	}

	// if there's a job but no (updated) deployment, wait for the job
	if hasJob && !hasDeployment {
		m.nextWaitForJobHandler.Handle(ctx)
		return
	}

	// if the deployment is up to date, continue
	m.nextDeploymentHandler.Handle(ctx)
}
