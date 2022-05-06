package handlers

import (
	"context"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
)

type ConfigChangedHandler struct {
	libctrl.HandlerControls
	cluster     *v1alpha1.SpiceDBCluster
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func NewConfigChangedHandler(ctrls libctrl.HandlerControls, cluster *v1alpha1.SpiceDBCluster, patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error, next handler.Handler) handler.Handler {
	return handler.NewHandler(&ConfigChangedHandler{
		HandlerControls: ctrls,
		cluster:         cluster,
		patchStatus:     patchStatus,
		next:            next,
	}, "checkConfigChanged")
}

func (c *ConfigChangedHandler) Handle(ctx context.Context) {
	secretHash := handlercontext.CtxSecretHash.Value(ctx)
	if c.cluster.GetGeneration() != c.cluster.Status.ObservedGeneration || secretHash != c.cluster.Status.SecretHash {
		c.cluster.Status.ObservedGeneration = c.cluster.GetGeneration()
		c.cluster.Status.SecretHash = secretHash
		c.SetStatusCondition(v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := c.patchStatus(ctx, c.cluster); err != nil {
			c.RequeueErr(err)
			return
		}
	}
	ctx = handlercontext.CtxClusterStatus.WithValue(ctx, c.cluster)
	c.next.Handle(ctx)
}
