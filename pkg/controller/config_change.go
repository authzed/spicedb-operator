package controller

import (
	"context"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

type ConfigChangedHandler struct {
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func (c *ConfigChangedHandler) Handle(ctx context.Context) {
	cluster := CtxCluster.MustValue(ctx)
	secret := CtxSecret.Value(ctx)
	var secretHash string
	if secret != nil {
		ctx = CtxSecretHash.WithValue(ctx, hash.SecureObject(secret.Data))
	}
	status := &v1alpha1.SpiceDBCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.SpiceDBClusterKind,
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: cluster.Namespace, Name: cluster.Name, Generation: cluster.Generation},
		Status:     *cluster.Status.DeepCopy(),
	}

	preconditionsFailedCondition := cluster.FindStatusCondition(v1alpha1.ConditionTypePreconditionsFailed)
	if cluster.GetGeneration() != status.Status.ObservedGeneration || secretHash != status.Status.SecretHash ||
		(preconditionsFailedCondition != nil && preconditionsFailedCondition.Reason == v1alpha1.ConditionReasonMissingSecret) {
		logr.FromContextOrDiscard(ctx).V(4).Info("spicedb configuration changed")
		status.Status.ObservedGeneration = cluster.GetGeneration()
		status.Status.SecretHash = secretHash
		status.RemoveStatusCondition(v1alpha1.ConditionTypePreconditionsFailed)
		status.SetStatusCondition(v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := c.patchStatus(ctx, status); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
	}
	ctx = CtxClusterStatus.WithValue(ctx, status)
	c.next.Handle(ctx)
}
