package controller

import (
	"context"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/pause"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func NewPauseHandler(
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error,
	next handler.ContextHandler,
) handler.Handler {
	return handler.NewHandler(pause.NewPauseContextHandler(
		QueueOps.Key,
		metadata.PausedControllerSelectorKey,
		CtxCluster,
		patchStatus,
		next,
	), "pauseCluster")
}
