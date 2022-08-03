package handlers

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/spicecluster"
)

const EventInvalidSpiceDBConfig = "InvalidSpiceDBConfig"

type ValidateConfigHandler struct {
	libctrl.ControlAll
	rawConfig           json.RawMessage
	defaultSpiceDBImage string
	allowedImages       []string
	allowedTags         []string
	uid                 types.UID
	generation          int64
	recorder            record.EventRecorder

	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func NewValidateConfigHandler(ctrls libctrl.HandlerControls, uid types.UID, rawConfig json.RawMessage, spicedbImage string, allowedImages, allowedTags []string, generation int64, patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error, recorder record.EventRecorder, next handler.Handler) handler.Handler {
	return handler.NewHandler(&ValidateConfigHandler{
		ControlAll:          ctrls,
		uid:                 uid,
		rawConfig:           rawConfig,
		defaultSpiceDBImage: spicedbImage,
		allowedImages:       allowedImages,
		allowedTags:         allowedTags,
		generation:          generation,
		patchStatus:         patchStatus,
		recorder:            recorder,
		next:                next,
	}, "validateConfig")
}

func (c *ValidateConfigHandler) Handle(ctx context.Context) {
	currentStatus := handlercontext.CtxClusterStatus.MustValue(ctx)
	nn := handlercontext.CtxClusterNN.MustValue(ctx)
	validatedConfig, warning, err := spicecluster.NewConfig(nn, c.uid, c.defaultSpiceDBImage, c.allowedImages, c.allowedTags, c.rawConfig, handlercontext.CtxSecret.Value(ctx))
	if err != nil {
		failedCondition := v1alpha1.NewInvalidConfigCondition("", err)
		if existing := currentStatus.FindStatusCondition(v1alpha1.ConditionValidatingFailed); existing != nil && existing.Message == failedCondition.Message {
			c.Done()
			return
		}
		currentStatus.Status.ObservedGeneration = currentStatus.GetGeneration()
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeValidating)
		currentStatus.SetStatusCondition(failedCondition)
		if err := c.patchStatus(ctx, currentStatus); err != nil {
			c.RequeueAPIErr(err)
			return
		}
		c.recorder.Eventf(currentStatus, corev1.EventTypeWarning, EventInvalidSpiceDBConfig, "invalid config: %v", err)
		// if the config is invalid, there's no work to do until it has changed
		c.Done()
		return
	}

	var warningCondition *metav1.Condition
	if warning != nil {
		cond := v1alpha1.NewConfigWarningCondition(warning)
		warningCondition = &cond
	}

	migrationHash, err := libctrl.SecureHashObject(validatedConfig.MigrationConfig)
	if err != nil {
		c.RequeueErr(err)
		return
	}
	ctx = handlercontext.CtxMigrationHash.WithValue(ctx, migrationHash)

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
			c.RequeueAPIErr(err)
			return
		}
	}

	ctx = handlercontext.CtxConfig.WithValue(ctx, validatedConfig)
	ctx = handlercontext.CtxClusterStatus.WithValue(ctx, currentStatus)
	c.next.Handle(ctx)
}
