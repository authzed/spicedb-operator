package controller

import (
	"context"
	"encoding/json"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/updates"
)

const EventInvalidSpiceDBConfig = "InvalidSpiceDBConfig"

type ValidateConfigHandler struct {
	recorder    record.EventRecorder
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
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
	operatorConfig := CtxOperatorConfig.MustValue(ctx)

	status := CtxClusterStatus.MustValue(ctx).Status

	rolloutInProgress := currentStatus.IsStatusConditionTrue(v1alpha1.ConditionTypeMigrating) ||
		currentStatus.IsStatusConditionTrue(v1alpha1.ConditionTypeRolling) ||
		currentStatus.Status.CurrentMigrationHash != currentStatus.Status.TargetMigrationHash

	validatedConfig, warning, err := config.NewConfig(nn, cluster.UID, cluster.Spec.Version, cluster.Spec.Channel, status.CurrentVersion, operatorConfig, rawConfig, secret, rolloutInProgress)
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

	computedStatus := v1alpha1.ClusterStatus{
		ObservedGeneration:   currentStatus.GetGeneration(),
		TargetMigrationHash:  migrationHash,
		CurrentMigrationHash: currentStatus.Status.CurrentMigrationHash,
		SecretHash:           currentStatus.Status.SecretHash,
		Image:                validatedConfig.TargetSpiceDBImage,
		Migration:            validatedConfig.TargetMigration,
		Phase:                validatedConfig.TargetPhase,
		CurrentVersion:       validatedConfig.SpiceDBVersion,
		Conditions:           currentStatus.GetStatusConditions(),
	}
	if validatedConfig.SpiceDBVersion != nil {
		computedStatus.AvailableVersions = c.getAvailableVersions(ctx, operatorConfig.UpdateGraph, *validatedConfig.SpiceDBVersion, validatedConfig.DatastoreEngine)
	}
	meta.RemoveStatusCondition(&computedStatus.Conditions, v1alpha1.ConditionValidatingFailed)
	meta.RemoveStatusCondition(&computedStatus.Conditions, v1alpha1.ConditionTypeValidating)
	if warningCondition != nil {
		meta.SetStatusCondition(&computedStatus.Conditions, *warningCondition)
	} else {
		meta.RemoveStatusCondition(&computedStatus.Conditions, v1alpha1.ConditionTypeConfigWarnings)
	}

	// Remove invalid config status and set image and hash
	if !currentStatus.Status.Equals(computedStatus) {
		currentStatus.Status = computedStatus
		if err := c.patchStatus(ctx, currentStatus); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
	}

	ctx = CtxConfig.WithValue(ctx, validatedConfig)
	ctx = CtxClusterStatus.WithValue(ctx, currentStatus)
	c.next.Handle(ctx)
}

func (c *ValidateConfigHandler) getAvailableVersions(ctx context.Context, graph updates.UpdateGraph, version v1alpha1.SpiceDBVersion, datastore string) []v1alpha1.SpiceDBVersion {
	logger := logr.FromContextOrDiscard(ctx)
	source, err := graph.SourceForChannel(version.Channel)
	if err != nil {
		logger.V(4).Error(err, "no source found for channel %q, can't compute available versions", version.Channel)
	}

	availableVersions := make([]v1alpha1.SpiceDBVersion, 0)
	nextWithoutMigrations := source.NextVersionWithoutMigrations(version.Name)
	latest := source.LatestVersion(version.Name)
	if len(nextWithoutMigrations) > 0 {
		nextDirectVersion := v1alpha1.SpiceDBVersion{
			Name:        nextWithoutMigrations,
			Channel:     version.Channel,
			Description: "direct update with no migrations",
		}
		if nextWithoutMigrations == latest {
			nextDirectVersion.Description += ", head of channel"
		}
		availableVersions = append(availableVersions, nextDirectVersion)
	}

	next := source.NextVersion(version.Name)
	if len(next) > 0 && next != nextWithoutMigrations {
		nextVersion := v1alpha1.SpiceDBVersion{
			Name:        next,
			Channel:     version.Channel,
			Description: "update will run a migration",
		}
		if next == latest {
			nextVersion.Description += ", head of channel"
		}
		availableVersions = append(availableVersions, nextVersion)
	}
	if len(latest) > 0 && next != latest && nextWithoutMigrations != latest {
		availableVersions = append(availableVersions, v1alpha1.SpiceDBVersion{
			Name:        latest,
			Channel:     version.Channel,
			Description: "head of the channel, multiple updates will run in sequence",
		})
	}

	// check for options in other channels (only show the safest update for
	// each available channel)
	for _, c := range graph.Channels {
		if c.Name == version.Channel {
			continue
		}
		if c.Metadata["datastore"] != datastore {
			continue
		}
		source, err := graph.SourceForChannel(c.Name)
		if err != nil {
			logger.V(4).Error(err, "no source found for channel %q, can't compute available versions", c.Name)
			continue
		}
		if next := source.NextVersionWithoutMigrations(version.Name); len(next) > 0 {
			availableVersions = append(availableVersions, v1alpha1.SpiceDBVersion{
				Name:        next,
				Channel:     c.Name,
				Description: "direct update with no migrations, different channel",
			})
			continue
		}
		if next := source.NextVersion(version.Name); len(next) > 0 {
			availableVersions = append(availableVersions, v1alpha1.SpiceDBVersion{
				Name:        next,
				Channel:     c.Name,
				Description: "update will run a migration, different channel",
			})
		}
	}

	return availableVersions
}
