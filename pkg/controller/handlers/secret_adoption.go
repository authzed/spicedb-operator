package handlers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

const EventSecretAdoptedBySpiceDBCluster = "SecretAdoptedBySpiceDB"

type SecretApplyFunc func(ctx context.Context, secret *applycorev1.SecretApplyConfiguration, opts metav1.ApplyOptions) (result *corev1.Secret, err error)

// TODO: generic adoption handler
type SecretAdopterHandler struct {
	libctrl.ControlAll
	secretName string
	recorder   record.EventRecorder

	// TODO: component
	secretIndexer   cache.Indexer
	secretApplyFunc SecretApplyFunc
	allClusters     func(ctx context.Context) []*v1alpha1.SpiceDBCluster
	next            handler.ContextHandler
}

func NewSecretAdoptionHandler(ctrls libctrl.HandlerControls, recorder record.EventRecorder, secretName string, secretIndexer cache.Indexer, secretApplyFunc SecretApplyFunc, allClusters func(ctx context.Context) []*v1alpha1.SpiceDBCluster, next handler.Handler) handler.Handler {
	return handler.NewHandler(&SecretAdopterHandler{
		ControlAll:    ctrls,
		recorder:      recorder,
		secretName:    secretName,
		secretIndexer: secretIndexer,
		secretApplyFunc: func(ctx context.Context, secret *applycorev1.SecretApplyConfiguration, opts metav1.ApplyOptions) (result *corev1.Secret, err error) {
			klog.V(4).InfoS("updating secret", "namespace", *secret.Namespace, "name", *secret.Name)
			return secretApplyFunc(ctx, secret, opts)
		},
		allClusters: allClusters,
		next:        next,
	}, "adoptSecret")
}

func (s *SecretAdopterHandler) Handle(ctx context.Context) {
	if s.secretName == "" {
		s.next.Handle(ctx)
		return
	}
	nn := handlercontext.CtxClusterNN.MustValue(ctx)
	secrets, err := s.secretIndexer.ByIndex(metadata.OwningClusterIndex, nn.String())
	if err != nil {
		s.RequeueErr(err)
		return
	}

	// secret is not in cache, which means it's not labelled for the controller
	// fetch it and add the label to it.
	if len(secrets) == 0 {
		// annotate it for all clusters that reference it
		// TODO: server-side-apply should mean this isn't required because the keys are
		//  different, but in practice they still fight for ownership
		secret, err := s.secretApplyFunc(ctx,
			applycorev1.Secret(s.secretName, nn.Namespace).
				WithLabels(map[string]string{
					metadata.OperatorManagedLabelKey: metadata.OperatorManagedLabelValue,
				}).
				WithAnnotations(s.clusterAnnotationsForSecret(ctx, s.secretName, nn.Namespace)), metadata.ApplyForceOwned)
		if err != nil {
			s.RequeueAPIErr(err)
			return
		}
		s.recorder.Eventf(secret, corev1.EventTypeNormal, EventSecretAdoptedBySpiceDBCluster, "Secret was referenced as the secret source for SpiceDBCluster %s; it has been labelled to mark it as part of the configuration for that controller.", nn.String())
		s.Requeue()
		return
	}

	var matchingSecret *corev1.Secret
	extraSecrets := make([]*corev1.Secret, 0)

	for _, obj := range secrets {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			panic(fmt.Sprintf("unknown object type %T found in informer cache for %s/%s; should not be possible", obj, nn.Namespace, s.secretName))
		}

		var secret *corev1.Secret
		if err = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &secret); err != nil {
			s.RequeueErr(err)
			return
		}
		if secret.Name == s.secretName {
			matchingSecret = secret
		} else {
			extraSecrets = append(extraSecrets, secret)
		}
	}

	secretHash, err := libctrl.SecureHashObject(matchingSecret.Data)
	if err != nil {
		s.RequeueErr(err)
		return
	}

	// unlabel any non-matching secrets
	for _, old := range extraSecrets {
		klog.V(4).InfoS("marking secret unowned", "owner", nn.String(), "secret", old.GetName())

		annotations := s.clusterAnnotationsForSecret(ctx, old.GetName(), nn.Namespace)
		labels := old.GetLabels()
		if len(annotations) == 0 {
			delete(labels, metadata.OwnerLabelKey)
			delete(labels, metadata.OperatorManagedLabelKey)
		}

		secret := applycorev1.Secret(old.Name, old.Namespace).WithLabels(labels).WithAnnotations(annotations)
		if _, err := s.secretApplyFunc(ctx, secret, metadata.ApplyForceOwned); err != nil {
			s.RequeueAPIErr(err)
			return
		}
	}

	ctx = handlercontext.CtxSecretHash.WithValue(ctx, secretHash)
	ctx = handlercontext.CtxSecret.WithValue(ctx, matchingSecret)
	s.next.Handle(ctx)
}

func (s *SecretAdopterHandler) clusterAnnotationsForSecret(ctx context.Context, secretName string, namespace string) map[string]string {
	annotations := make(map[string]string, 0)
	for _, c := range s.allClusters(ctx) {
		if c.Namespace == namespace && c.Spec.SecretRef == secretName {
			annotations[metadata.OwnerAnnotationKeyPrefix+c.Name] = "owned"
		}
	}
	return annotations
}
