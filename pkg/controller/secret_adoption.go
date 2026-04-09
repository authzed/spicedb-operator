package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/authzed/controller-idioms/adopt"
	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/typed"
	"github.com/authzed/controller-idioms/typedctx"

	"github.com/authzed/spicedb-operator/pkg/metadata"
)

// adoptionAwareIndexer wraps a cache.Indexer and filters ByIndex results so
// that other currently-valid adoptees are not misidentified as stale extras.
//
// When secretAdopter runs N AdoptionHandlers sequentially (one per secret),
// each handler's cleanup loop removes labels from any indexed secret that isn't
// its own adoptee. Without this wrapper, handler[i] strips the label from
// handler[j]'s adoptee on every reconcile, creating an infinite re-adoption loop.
//
// This wrapper ensures each handler sees only:
//   - Its own adoptee (so adoption proceeds normally), and
//   - Truly stale secrets (not in validNames) so they are still cleaned up.
//
// Other currently-valid adoptees are hidden from each handler's ByIndex call.
type adoptionAwareIndexer struct {
	cache.Indexer
	currentAdoptee string
	namespace      string
	validNames     map[string]struct{}
}

func newAdoptionAwareIndexer(base cache.Indexer, currentAdoptee, namespace string, allNames []string) *adoptionAwareIndexer {
	valid := make(map[string]struct{}, len(allNames))
	for _, n := range allNames {
		valid[n] = struct{}{}
	}
	return &adoptionAwareIndexer{
		Indexer:        base,
		currentAdoptee: currentAdoptee,
		namespace:      namespace,
		validNames:     valid,
	}
}

func (a *adoptionAwareIndexer) ByIndex(indexName, indexedValue string) ([]interface{}, error) {
	objs, err := a.Indexer.ByIndex(indexName, indexedValue)
	if err != nil {
		return nil, err
	}
	filtered := make([]interface{}, 0, len(objs))
	for _, obj := range objs {
		m, err := meta.Accessor(obj)
		if err != nil {
			continue
		}
		name := m.GetName()
		ns := m.GetNamespace()
		// Objects in other namespaces are passed through unchanged.
		if ns != a.namespace {
			filtered = append(filtered, obj)
			continue
		}
		// Always include the current adoptee.
		// Include stale secrets (not in validNames) so cleanup removes them.
		// Exclude other currently-valid adoptees to prevent incorrect cleanup.
		if name == a.currentAdoptee {
			filtered = append(filtered, obj)
		} else if _, isValid := a.validNames[name]; !isValid {
			filtered = append(filtered, obj)
		}
	}
	return filtered, nil
}

const EventSecretAdoptedBySpiceDBCluster = "SecretAdoptedBySpiceDB"

func NewSecretAdoptionHandler(recorder record.EventRecorder, getFromCache func(ctx context.Context) (*corev1.Secret, error), missingFunc func(ctx context.Context, err error), secretIndexer *typed.Indexer[*corev1.Secret], secretApplyFunc adopt.ApplyFunc[*corev1.Secret, *applycorev1.SecretApplyConfiguration], existsFunc func(ctx context.Context, name types.NamespacedName) error, next handler.Handler) handler.Handler {
	ctxSecret := typedctx.WithDefault[*corev1.Secret](nil)
	return handler.NewHandler(&adopt.AdoptionHandler[*corev1.Secret, *applycorev1.SecretApplyConfiguration]{
		OperationsContext:      QueueOps,
		ControllerFieldManager: metadata.FieldManager,
		AdopteeCtx:             CtxSecretNN,
		OwnerCtx:               CtxClusterNN,
		AdoptedCtx:             ctxSecret,
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
