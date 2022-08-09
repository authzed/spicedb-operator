package handlers

import (
	"context"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

const HandlerSelfPauseKey handler.Key = "nextSelfPause"

func NewSelfPauseHandler(cluster *v1alpha1.SpiceDBCluster,
	patch func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error,
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error,
) handler.Handler {
	return handler.NewHandler(libctrl.NewSelfPauseHandler(
		CtxHandlerControls.ContextKey,
		metadata.PausedControllerSelectorKey,
		CtxSelfPauseObject,
		cluster.UID,
		patch,
		patchStatus,
	), HandlerSelfPauseKey)
}
