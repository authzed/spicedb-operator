package cluster

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/server/healthz"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	_ "k8s.io/component-base/metrics/prometheus/workqueue" // for workqueue metric registration
	controllerhealthz "k8s.io/controller-manager/pkg/healthz"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/manager"
)

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen rbac:roleName=authzed-operator paths="../../pkg/..." output:rbac:dir=../../config/rbac

// +kubebuilder:rbac:groups="authzed.com",resources=stacks,verbs=get;watch;list;create;update;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;patch;

var (
	// OwnedResources are always synced unless they're marked unmanaged
	OwnedResources = []schema.GroupVersionResource{
		v1alpha1.SchemeGroupVersion.WithResource("stacks"),
	}

	// ExternalResources are not synced unless they're marked as managed
	ExternalResources = []schema.GroupVersionResource{
		appsv1.SchemeGroupVersion.WithResource("deployments"),
		corev1.SchemeGroupVersion.WithResource("secrets"),
	}
)

const OwningClusterIndex = "owning-cluster"

type Controller struct {
	queue          workqueue.RateLimitingInterface
	dependentQueue workqueue.RateLimitingInterface
	client         client.Client
	informers      map[schema.GroupVersionResource]dynamicinformer.DynamicSharedInformerFactory
	recorder       record.EventRecorder
	kclient        kubernetes.Interface
}

var _ manager.Controller = &Controller{}

func NewController(ctx context.Context, dclient dynamic.Interface, kclient kubernetes.Interface, cclient client.Client) (*Controller, error) {
	c := &Controller{
		kclient:        kclient,
		client:         cclient,
		queue:          workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "cluster_queue"),
		dependentQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "cluster_components_queue"),
		informers:      make(map[schema.GroupVersionResource]dynamicinformer.DynamicSharedInformerFactory),
	}

	ownedInformerFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dclient,
		0,
		metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			options.LabelSelector = NotUnmanagedSelector.String()
		},
	)
	externalInformerFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dclient,
		0,
		metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			options.LabelSelector = ManagedDependentSelector.String()
		},
	)

	for _, gvr := range OwnedResources {
		gvr := gvr
		ownedInformerFactory.ForResource(gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueue(gvr, obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueue(gvr, obj) },
			// Delete is not used right now, we rely on ownerrefs to clean up
		})
		c.informers[gvr] = ownedInformerFactory
	}

	for _, gvr := range ExternalResources {
		gvr := gvr
		inf := externalInformerFactory.ForResource(gvr).Informer()
		if err := inf.AddIndexers(cache.Indexers{OwningClusterIndex: GetStackKeyFromLabel}); err != nil {
			return nil, err
		}
		inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueue(gvr, obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueue(gvr, obj) },
			DeleteFunc: func(obj interface{}) { c.enqueue(gvr, obj) },
		})
		c.informers[gvr] = externalInformerFactory
	}

	// start informers
	ownedInformerFactory.Start(ctx.Done())
	externalInformerFactory.Start(ctx.Done())
	ownedInformerFactory.WaitForCacheSync(ctx.Done())
	externalInformerFactory.WaitForCacheSync(ctx.Done())

	return c, nil
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()
	defer c.dependentQueue.ShutDown()

	klog.Info("Starting Stack controller")
	defer klog.Info("Shutting down Stack controller")

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx, c.queue, c.syncOwnedResource) }, time.Second, ctx.Done())
		go wait.Until(func() { c.startWorker(ctx, c.dependentQueue, c.syncExternalResource) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (c *Controller) Name() string {
	return "cluster"
}

func (c *Controller) DebuggingHandler() http.Handler {
	return http.NotFoundHandler()
}

func (c *Controller) HealthChecker() controllerhealthz.UnnamedHealthChecker {
	return healthz.PingHealthz
}

func (c *Controller) ListerFor(gvr schema.GroupVersionResource) cache.GenericLister {
	factory, ok := c.informers[gvr]
	if !ok {
		utilruntime.HandleError(fmt.Errorf("ListerFor called with unknown GVR"))
		return nil
	}
	return factory.ForResource(gvr).Lister()
}

func (c *Controller) InformerFor(gvr schema.GroupVersionResource) cache.SharedIndexInformer {
	factory, ok := c.informers[gvr]
	if !ok {
		utilruntime.HandleError(fmt.Errorf("InformerFor called with unknown GVR"))
		return nil
	}
	return factory.ForResource(gvr).Informer()
}

func (c *Controller) getOwnedObjects(cluster *v1alpha1.AuthzedEnterpriseCluster, gvr schema.GroupVersionResource) ([]interface{}, error) {
	return c.informers[gvr].ForResource(gvr).Informer().GetIndexer().ByIndex(OwningClusterIndex, cluster.Namespace+"/"+cluster.Name)
}

func (c *Controller) enqueue(gvr schema.GroupVersionResource, obj interface{}) {
	key, err := GVRMetaNamespaceKeyFunc(gvr, obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

func (c *Controller) startWorker(ctx context.Context, queue workqueue.RateLimitingInterface, sync syncFunc) {
	for c.processNext(ctx, queue, sync) {
	}
}

func (c *Controller) processNext(ctx context.Context, queue workqueue.RateLimitingInterface, sync syncFunc) bool {
	k, quit := queue.Get()
	defer queue.Done(k)
	if quit {
		return false
	}
	key, ok := k.(string)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("non-string key found in queue, %T", key))
	}

	gvr, namespace, name, err := SplitGVRMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("error parsing key %q, skipping", key)
		return true
	}

	if err := sync(ctx, *gvr, namespace, name); err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to sync %q, err: %w", key, err))
		queue.AddRateLimited(key)
		return true
	}
	queue.Forget(key)
	return true
}

// syncFunc - an error returned here will do a rate-limited requeue of the object's key
type syncFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error

// syncOwnedResource is called when Stack is updated
func (c *Controller) syncOwnedResource(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	if gvr.GroupResource() != authzedClusterGR {
		return fmt.Errorf("syncOwnedResource called on unknown gvr: %s", gvr.String())
	}
	obj, err := c.ListerFor(gvr).ByNamespace(namespace).Get(name)
	if err != nil {
		return err
	}

	stack, ok := obj.(*v1alpha1.AuthzedEnterpriseCluster)
	if !ok {
		return fmt.Errorf("syncOwnedResource called with invalid object %T", stack)
	}

	klog.V(4).Infof("syncing %s %s", gvr, stack.ObjectMeta)
	return c.syncCluster(ctx, stack.DeepCopy())
}

// syncExternalResource is called when a dependent resource is updated;
// It queues the owning Stack for reconciliation based on the labels.
// No other reconciliation should take place here; we keep a single state
// machine for Stacks with an entrypoint in syncCluster
func (c *Controller) syncExternalResource(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	obj, err := c.ListerFor(gvr).ByNamespace(namespace).Get(name)
	if err != nil {
		return err
	}

	objMeta, err := meta.Accessor(obj)
	if err != nil {
		return err
	}

	klog.V(4).Infof("syncing %s %s", gvr, objMeta)

	labels := objMeta.GetLabels()
	stackKey, ok := labels[OwnerLabelKey]
	if !ok {
		return fmt.Errorf("synced %s %s/%s is managed by the operator but not associated with any cluster", obj.GetObjectKind(), objMeta.GetNamespace(), objMeta.GetName())
	}

	// stackKey must be namespace/name
	if len(strings.SplitN(stackKey, "/", 2)) != 2 {
		return fmt.Errorf("invalid cluster key label on %s %s/%s: %s", obj.GetObjectKind(), objMeta.GetNamespace(), objMeta.GetName(), stackKey)
	}

	c.queue.AddRateLimited(GVRMetaNamespaceKeyer(v1alpha1ClusterGVR, stackKey))
	return nil
}
