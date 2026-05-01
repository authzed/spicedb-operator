package controller

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/handler"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

const EventInvalidSpiceDBSpec = "InvalidSpiceDBSpec"

// ValidateSpecHandler checks for invalid combinations of spec fields before
// any other processing takes place. It rejects the case where both SecretRef
// and Credentials are set (mutually exclusive), and rejects any CredentialRef
// that has Skip: false but an empty SecretName.
type ValidateSpecHandler struct {
	recorder    record.EventRecorder
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func (c *ValidateSpecHandler) Handle(ctx context.Context) {
	cluster := CtxCluster.MustValue(ctx)

	if cluster.Spec.SecretRef != "" && cluster.Spec.Credentials != nil {
		failedCondition := v1alpha1.NewInvalidConfigCondition("", errors.New("secretName and credentials are mutually exclusive; set only one"))
		if existing := cluster.FindStatusCondition(v1alpha1.ConditionValidatingFailed); existing != nil && existing.Message == failedCondition.Message {
			QueueOps.Done(ctx)
			return
		}
		cluster.Status.ObservedGeneration = cluster.GetGeneration()
		cluster.RemoveStatusCondition(v1alpha1.ConditionTypeValidating)
		cluster.SetStatusCondition(failedCondition)
		if err := c.patchStatus(ctx, cluster); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
		c.recorder.Eventf(cluster, corev1.EventTypeWarning, EventInvalidSpiceDBSpec, "invalid spec: secretName and credentials are mutually exclusive; set only one")
		QueueOps.Done(ctx)
		return
	}

	if cluster.Spec.Credentials != nil {
		type credField struct {
			name string
			ref  *v1alpha1.CredentialRef
		}
		fields := []credField{
			{"credentials.datastoreURI", cluster.Spec.Credentials.DatastoreURI},
			{"credentials.presharedKey", cluster.Spec.Credentials.PresharedKey},
			{"credentials.migrationSecrets", cluster.Spec.Credentials.MigrationSecrets},
		}
		for _, f := range fields {
			if f.ref != nil && !f.ref.Skip && f.ref.SecretName == "" {
				msg := f.name + ": secretName must be set when skip is false"
				failedCondition := v1alpha1.NewInvalidConfigCondition("", errors.New(msg))
				if existing := cluster.FindStatusCondition(v1alpha1.ConditionValidatingFailed); existing != nil && existing.Message == failedCondition.Message {
					QueueOps.Done(ctx)
					return
				}
				cluster.Status.ObservedGeneration = cluster.GetGeneration()
				cluster.RemoveStatusCondition(v1alpha1.ConditionTypeValidating)
				cluster.SetStatusCondition(failedCondition)
				if err := c.patchStatus(ctx, cluster); err != nil {
					QueueOps.RequeueAPIErr(ctx, err)
					return
				}
				c.recorder.Eventf(cluster, corev1.EventTypeWarning, EventInvalidSpiceDBSpec, "invalid spec: %s", msg)
				QueueOps.Done(ctx)
				return
			}
		}
	}

	c.next.Handle(ctx)
}
