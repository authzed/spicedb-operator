package handlers

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	status := &v1alpha1.SpiceDBCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.SpiceDBClusterKind,
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: c.cluster.Namespace, Name: c.cluster.Name, Generation: c.cluster.Generation},
		Status:     *c.cluster.Status.DeepCopy(),
	}

	if c.cluster.GetGeneration() != status.Status.ObservedGeneration || secretHash != status.Status.SecretHash {
		status.Status.ObservedGeneration = c.cluster.GetGeneration()
		status.Status.SecretHash = secretHash
		status.SetStatusCondition(v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := c.patchStatus(ctx, status); err != nil {
			c.RequeueErr(err)
			return
		}
	}
	ctx = handlercontext.CtxClusterStatus.WithValue(ctx, status)
	c.next.Handle(ctx)
}
