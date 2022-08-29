package controller

import (
	"context"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/pause"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

const HandlerSelfPauseKey handler.Key = "nextSelfPause"

func NewSelfPauseHandler(
	patch func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error,
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error,
) handler.Handler {
	return handler.NewHandler(pause.NewSelfPauseHandler(
		QueueOps.Key,
		metadata.PausedControllerSelectorKey,
		CtxSelfPauseObject,
		patch,
		patchStatus,
	), HandlerSelfPauseKey)
}
