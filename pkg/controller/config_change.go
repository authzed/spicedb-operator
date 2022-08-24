package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
)

type ConfigChangedHandler struct {
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func (c *ConfigChangedHandler) Handle(ctx context.Context) {
	cluster := CtxCluster.MustValue(ctx)
	secretHash := CtxSecretHash.Value(ctx)
	status := &v1alpha1.SpiceDBCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.SpiceDBClusterKind,
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: cluster.Namespace, Name: cluster.Name, Generation: cluster.Generation},
		Status:     *cluster.Status.DeepCopy(),
	}

	if cluster.GetGeneration() != status.Status.ObservedGeneration || secretHash != status.Status.SecretHash {
		klog.FromContext(ctx).V(4).Info("spicedb configuration changed")
		status.Status.ObservedGeneration = cluster.GetGeneration()
		status.Status.SecretHash = secretHash
		status.SetStatusCondition(v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := c.patchStatus(ctx, status); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
	}
	ctx = CtxClusterStatus.WithValue(ctx, status)
	c.next.Handle(ctx)
}
