package handlers

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
)

type ConfigChangedHandler struct {
	libctrl.HandlerControls
	currentStatus *v1alpha1.SpiceDBCluster
	obj           metav1.Object
	status        *v1alpha1.ClusterStatus
	patchStatus   func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next          handler.ContextHandler
}

func NewConfigChangedHandler(ctrls libctrl.HandlerControls, currentStatus *v1alpha1.SpiceDBCluster, obj metav1.Object, status *v1alpha1.ClusterStatus, patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error, next handler.Handler) handler.Handler {
	return handler.NewHandler(&ConfigChangedHandler{
		HandlerControls: ctrls,
		currentStatus:   currentStatus,
		obj:             obj,
		status:          status,
		patchStatus:     patchStatus,
		next:            next,
	}, "checkConfigChanged")
}

func (c *ConfigChangedHandler) Handle(ctx context.Context) {
	secretHash := handlercontext.CtxSecretHash.Value(ctx)
	if c.obj.GetGeneration() != c.status.ObservedGeneration || secretHash != c.status.SecretHash {
		c.currentStatus.Status.ObservedGeneration = c.obj.GetGeneration()
		c.currentStatus.Status.SecretHash = secretHash
		meta.SetStatusCondition(&c.currentStatus.Status.Conditions, v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := c.patchStatus(ctx, c.currentStatus); err != nil {
			c.RequeueErr(err)
			return
		}
	}
	ctx = handlercontext.CtxClusterStatus.WithValue(ctx, c.currentStatus)
	c.next.Handle(ctx)
}
