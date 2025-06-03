package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubectl/pkg/util/openapi"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
)

const EventInvalidSpiceDBConfig = "InvalidSpiceDBConfig"

type ValidateConfigHandler struct {
	recorder    record.EventRecorder
	resources   openapi.Resources
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func (c *ValidateConfigHandler) Handle(ctx context.Context) {
	cluster := CtxCluster.MustValue(ctx)
	secret := CtxSecret.Value(ctx)
	operatorConfig := CtxOperatorConfig.MustValue(ctx)

	validatedConfig, warning, err := config.NewConfig(cluster, operatorConfig, secret, c.resources)
	if err != nil {
		failedCondition := v1alpha1.NewInvalidConfigCondition(CtxSecretHash.Value(ctx), err)
		if existing := cluster.FindStatusCondition(v1alpha1.ConditionValidatingFailed); existing != nil && existing.Message == failedCondition.Message {
			QueueOps.Done(ctx)
			return
		}
		cluster.Status.ObservedGeneration = cluster.GetGeneration()
		cluster.RemoveStatusCondition(v1alpha1.ConditionTypeValidating)
		cluster.SetStatusCondition(failedCondition)
		if err := c.patchStatus(ctx, cluster); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
		c.recorder.Eventf(cluster, corev1.EventTypeWarning, EventInvalidSpiceDBConfig, "invalid config: %v", err)
		// if the config is invalid, there's no work to do until it has changed
		QueueOps.Done(ctx)
		return
	}

	var warningCondition *metav1.Condition
	if warning != nil {
		cond := v1alpha1.NewConfigWarningCondition(warning)
		warningCondition = &cond
	}

	migrationHash := hash.SecureObject(validatedConfig.MigrationConfig)
	ctx = CtxMigrationHash.WithValue(ctx, migrationHash)

	computedStatus := v1alpha1.ClusterStatus{
		ObservedGeneration:   cluster.GetGeneration(),
		TargetMigrationHash:  migrationHash,
		CurrentMigrationHash: cluster.Status.CurrentMigrationHash,
		SecretHash:           cluster.Status.SecretHash,
		Image:                validatedConfig.TargetSpiceDBImage,
		Migration:            validatedConfig.TargetMigration,
		Phase:                validatedConfig.TargetPhase,
		CurrentVersion:       validatedConfig.SpiceDBVersion,
		Conditions:           *cluster.GetStatusConditions(),
	}
	if version := validatedConfig.SpiceDBVersion; version != nil {
		computedStatus.AvailableVersions, err = operatorConfig.AvailableVersions(validatedConfig.DatastoreEngine, *version)
		if err != nil {
			QueueOps.RequeueErr(ctx, err)
			return
		}
	}
	meta.RemoveStatusCondition(&computedStatus.Conditions, v1alpha1.ConditionValidatingFailed)
	meta.RemoveStatusCondition(&computedStatus.Conditions, v1alpha1.ConditionTypeValidating)
	if warningCondition != nil {
		meta.SetStatusCondition(&computedStatus.Conditions, *warningCondition)
	} else {
		meta.RemoveStatusCondition(&computedStatus.Conditions, v1alpha1.ConditionTypeConfigWarnings)
	}

	// Remove invalid config status and set image and hash
	if !cluster.Status.Equals(computedStatus) {
		cluster.Status = computedStatus
		if err := c.patchStatus(ctx, cluster); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
	}

	ctx = CtxConfig.WithValue(ctx, validatedConfig)
	ctx = CtxCluster.WithValue(ctx, cluster)
	c.next.Handle(ctx)
}
