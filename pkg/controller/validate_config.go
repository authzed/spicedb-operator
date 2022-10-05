package controller

import (
	"context"
	"encoding/json"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
)

const EventInvalidSpiceDBConfig = "InvalidSpiceDBConfig"

type ValidateConfigHandler struct {
	recorder           record.EventRecorder
	patchStatus        func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	getDeploymentImage func(ctx context.Context, nn types.NamespacedName) (string, error)
	next               handler.ContextHandler
}

func (c *ValidateConfigHandler) Handle(ctx context.Context) {
	currentStatus := CtxClusterStatus.MustValue(ctx)
	nn := CtxClusterNN.MustValue(ctx)
	cluster := CtxCluster.MustValue(ctx)
	rawConfig := cluster.Spec.Config
	if rawConfig == nil {
		rawConfig = json.RawMessage("")
	}

	secret := CtxSecret.Value(ctx)
	if secret != nil {
		secretHash, err := hash.SecureObject(secret.Data)
		if err != nil {
			QueueOps.RequeueErr(ctx, err)
			return
		}
		ctx = CtxSecretHash.WithValue(ctx, secretHash)
	}

	operatorConfig := CtxOperatorConfig.MustValue(ctx)
	currentImage, err := c.getDeploymentImage(ctx, types.NamespacedName{Namespace: nn.Namespace, Name: config.DeploymentName(nn.Name)})
	if err != nil {
		logr.FromContextOrDiscard(ctx).Error(err, "error fetching current deployment - normal if cluster is not yet created")
	}
	validatedConfig, warning, err := config.NewConfig(nn, cluster.UID, currentImage, operatorConfig, rawConfig, secret)
	if err != nil {
		failedCondition := v1alpha1.NewInvalidConfigCondition(CtxSecretHash.Value(ctx), err)
		if existing := currentStatus.FindStatusCondition(v1alpha1.ConditionValidatingFailed); existing != nil && existing.Message == failedCondition.Message {
			QueueOps.Done(ctx)
			return
		}
		currentStatus.Status.ObservedGeneration = currentStatus.GetGeneration()
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeValidating)
		currentStatus.SetStatusCondition(failedCondition)
		if err := c.patchStatus(ctx, currentStatus); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
		c.recorder.Eventf(currentStatus, corev1.EventTypeWarning, EventInvalidSpiceDBConfig, "invalid config: %v", err)
		// if the config is invalid, there's no work to do until it has changed
		QueueOps.Done(ctx)
		return
	}

	var warningCondition *metav1.Condition
	if warning != nil {
		cond := v1alpha1.NewConfigWarningCondition(warning)
		warningCondition = &cond
	}

	migrationHash, err := hash.SecureObject(validatedConfig.MigrationConfig)
	if err != nil {
		QueueOps.RequeueErr(ctx, err)
		return
	}
	ctx = CtxMigrationHash.WithValue(ctx, migrationHash)

	// Remove invalid config status and set image and hash
	if currentStatus.IsStatusConditionTrue(v1alpha1.ConditionValidatingFailed) ||
		currentStatus.IsStatusConditionTrue(v1alpha1.ConditionTypeValidating) ||
		currentStatus.Status.Image != validatedConfig.TargetSpiceDBImage ||
		currentStatus.Status.TargetMigrationHash != migrationHash ||
		currentStatus.IsStatusConditionChanged(v1alpha1.ConditionTypeConfigWarnings, warningCondition) {
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionValidatingFailed)
		currentStatus.Status.Image = validatedConfig.TargetSpiceDBImage
		currentStatus.Status.TargetMigrationHash = migrationHash
		currentStatus.Status.ObservedGeneration = currentStatus.GetGeneration()
		if warningCondition != nil {
			currentStatus.SetStatusCondition(*warningCondition)
		} else {
			currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeConfigWarnings)
		}
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeValidating)
		if err := c.patchStatus(ctx, currentStatus); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
	}

	ctx = CtxConfig.WithValue(ctx, validatedConfig)
	ctx = CtxClusterStatus.WithValue(ctx, currentStatus)
	c.next.Handle(ctx)
}
