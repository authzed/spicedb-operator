package handlers

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
)

const EventInvalidSpiceDBConfig = "InvalidSpiceDBConfig"

type ValidateConfigHandler struct {
	libctrl.ControlAll
	rawConfig    json.RawMessage
	spiceDBImage string
	uid          types.UID
	generation   int64
	recorder     record.EventRecorder

	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func NewValidateConfigHandler(ctrls libctrl.HandlerControls, uid types.UID, rawConfig json.RawMessage, spicedbImage string, generation int64, patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error, recorder record.EventRecorder, next handler.Handler) handler.Handler {
	return handler.NewHandler(&ValidateConfigHandler{
		ControlAll:   ctrls,
		uid:          uid,
		rawConfig:    rawConfig,
		spiceDBImage: spicedbImage,
		generation:   generation,
		patchStatus:  patchStatus,
		recorder:     recorder,
		next:         next,
	}, "validateConfig")
}

func (c *ValidateConfigHandler) Handle(ctx context.Context) {
	currentStatus := handlercontext.CtxClusterStatus.MustValue(ctx)
	// TODO: unconditional status change can be a separate handler
	// config is either valid or invalid, remove validating condition
	if condition := currentStatus.FindStatusCondition(v1alpha1.ConditionTypeValidating); condition != nil {
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeValidating)
		if err := c.patchStatus(ctx, currentStatus); err != nil {
			c.Requeue()
			return
		}
	}

	nn := handlercontext.CtxClusterNN.MustValue(ctx)
	validatedConfig, warning, err := config.NewConfig(nn, c.uid, c.spiceDBImage, c.rawConfig, handlercontext.CtxSecret.Value(ctx))
	if err != nil {
		failedCondition := v1alpha1.NewInvalidConfigCondition("", err)
		if existing := currentStatus.FindStatusCondition(v1alpha1.ConditionValidatingFailed); existing != nil && existing.Message == failedCondition.Message {
			c.Done()
			return
		}
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

	// Remove invalid config status and set image
	if currentStatus.IsStatusConditionTrue(v1alpha1.ConditionValidatingFailed) ||
		currentStatus.Status.Image != validatedConfig.TargetSpiceDBImage ||
		currentStatus.IsStatusConditionChanged(v1alpha1.ConditionTypeConfigWarnings, warningCondition) {
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionValidatingFailed)
		currentStatus.Status.Image = validatedConfig.TargetSpiceDBImage
		if warningCondition != nil {
			currentStatus.SetStatusCondition(*warningCondition)
		} else {
			currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeConfigWarnings)
		}
		if err := c.patchStatus(ctx, currentStatus); err != nil {
			c.RequeueAPIErr(err)
			return
		}
	}

	ctx = handlercontext.CtxConfig.WithValue(ctx, validatedConfig)
	ctx = handlercontext.CtxClusterStatus.WithValue(ctx, currentStatus)
	c.next.Handle(ctx)
}
