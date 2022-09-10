package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/adopt"
	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/typed"

	"github.com/authzed/spicedb-operator/pkg/metadata"
)

const EventSecretAdoptedBySpiceDBCluster = "SecretAdoptedBySpiceDB"

func NewSecretAdoptionHandler(recorder record.EventRecorder, getFromCache func(ctx context.Context) (*corev1.Secret, error), missingFunc func(ctx context.Context, err error), secretIndexer *typed.Indexer[*corev1.Secret], secretApplyFunc adopt.ApplyFunc[*corev1.Secret, *applycorev1.SecretApplyConfiguration], existsFunc func(ctx context.Context, name types.NamespacedName) error, next handler.Handler) handler.Handler {
	return handler.NewHandler(&adopt.AdoptionHandler[*corev1.Secret, *applycorev1.SecretApplyConfiguration]{
		OperationsContext:      QueueOps,
		ControllerFieldManager: metadata.FieldManager,
		AdopteeCtx:             CtxSecretNN,
		OwnerCtx:               CtxClusterNN,
		AdoptedCtx:             CtxSecret,
		ObjectAdoptedFunc: func(ctx context.Context, secret *corev1.Secret) {
			recorder.Eventf(secret, corev1.EventTypeNormal, EventSecretAdoptedBySpiceDBCluster, "Secret was referenced as the secret source for SpiceDBCluster %s; it has been labelled to mark it as part of the configuration for that controller.", CtxClusterNN.MustValue(ctx).String())
		},
		ObjectMissingFunc: missingFunc,
		GetFromCache:      getFromCache,
		Indexer:           secretIndexer,
		IndexName:         metadata.OwningClusterIndex,
		Labels:            map[string]string{metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue},
		NewPatch: func(nn types.NamespacedName) *applycorev1.SecretApplyConfiguration {
			return applycorev1.Secret(nn.Name, nn.Namespace)
		},
		OwnerAnnotationPrefix: metadata.OwnerAnnotationKeyPrefix,
		OwnerAnnotationKeyFunc: func(owner types.NamespacedName) string {
			return metadata.OwnerAnnotationKeyPrefix + owner.Name
		},
		OwnerFieldManagerFunc: func(owner types.NamespacedName) string {
			return "spicedbcluster-owner-" + owner.Namespace + "-" + owner.Name
		},
		ApplyFunc:  secretApplyFunc,
		ExistsFunc: existsFunc,
		Next:       next,
	}, "adoptSecret")
}
