package cluster

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
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

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/manager"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen rbac:roleName=spicedb-operator paths="../../pkg/..." output:rbac:dir=../../config/rbac

// +kubebuilder:rbac:groups="authzed.com",resources=spicedbclusters,verbs=get;watch;list;create;update;patch;delete
// +kubebuilder:rbac:groups="authzed.com",resources=spicedbclusters/status,verbs=get;watch;list;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update;patch;delete

func init() {
	utilruntime.Must(v1alpha1.AddToScheme(scheme.Scheme))
}

var (
	// OwnedResources are always synced unless they're marked unmanaged
	OwnedResources = []schema.GroupVersionResource{
		v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName),
	}

	// ExternalResources are not synced unless they're marked as managed
	ExternalResources = []schema.GroupVersionResource{
		appsv1.SchemeGroupVersion.WithResource("deployments"),
		corev1.SchemeGroupVersion.WithResource("secrets"),
		corev1.SchemeGroupVersion.WithResource("serviceaccounts"),
		corev1.SchemeGroupVersion.WithResource("services"),
		corev1.SchemeGroupVersion.WithResource("pods"),
		batchv1.SchemeGroupVersion.WithResource("jobs"),
		rbacv1.SchemeGroupVersion.WithResource("roles"),
		rbacv1.SchemeGroupVersion.WithResource("rolebindings"),
	}
)

const OwningClusterIndex = "owning-cluster"

type OperatorConfig struct {
	ImageName   string `json:"imageName"`
	ImageTag    string `json:"imageTag"`
	ImageDigest string `json:"imageDigest"`
}

type Controller struct {
	queue          workqueue.RateLimitingInterface
	dependentQueue workqueue.RateLimitingInterface
	client         dynamic.Interface
	informers      map[schema.GroupVersionResource]dynamicinformer.DynamicSharedInformerFactory
	recorder       record.EventRecorder
	kclient        kubernetes.Interface

	// config
	configFilePath string
	configWatcher  *fsnotify.Watcher
	configLock     sync.RWMutex
	config         OperatorConfig
}

var _ manager.Controller = &Controller{}

func NewController(ctx context.Context, dclient dynamic.Interface, kclient kubernetes.Interface, configFilePath string) (*Controller, error) {
	c := &Controller{
		kclient:        kclient,
		client:         dclient,
		queue:          workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "cluster_queue"),
		dependentQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "cluster_components_queue"),
		informers:      make(map[schema.GroupVersionResource]dynamicinformer.DynamicSharedInformerFactory),
		configFilePath: configFilePath,
	}
	if len(configFilePath) > 0 {
		var err error
		c.configWatcher, err = fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		if err := c.configWatcher.Add(configFilePath); err != nil {
			return nil, err
		}
	}

	ownedInformerFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dclient,
		0,
		metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			options.LabelSelector = metadata.NotPausedSelector.String()
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
			AddFunc:    func(obj interface{}) { c.enqueue(gvr, c.queue, obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueue(gvr, c.queue, obj) },
			// Delete is not used right now, we rely on ownerrefs to clean up
		})
		c.informers[gvr] = ownedInformerFactory
	}

	for _, gvr := range ExternalResources {
		gvr := gvr
		inf := externalInformerFactory.ForResource(gvr).Informer()
		if err := inf.AddIndexers(cache.Indexers{OwningClusterIndex: GetClusterKeyFromLabel}); err != nil {
			return nil, err
		}
		inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueue(gvr, c.dependentQueue, obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueue(gvr, c.dependentQueue, obj) },
			DeleteFunc: func(obj interface{}) { c.enqueue(gvr, c.dependentQueue, obj) },
		})
		c.informers[gvr] = externalInformerFactory
	}

	c.loadConfig()

	// start informers
	ownedInformerFactory.Start(ctx.Done())
	externalInformerFactory.Start(ctx.Done())
	ownedInformerFactory.WaitForCacheSync(ctx.Done())
	externalInformerFactory.WaitForCacheSync(ctx.Done())
	go c.watchConfig()

	return c, nil
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()
	defer c.dependentQueue.ShutDown()

	broadcaster := record.NewBroadcaster()
	defer broadcaster.Shutdown()

	broadcaster.StartStructuredLogging(0)
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: c.kclient.CoreV1().Events("")})
	c.recorder = broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "spicedb-operator"})

	for _, gvr := range OwnedResources {
		klog.Info(fmt.Sprintf("Starting %s controller", gvr.Resource))
		defer klog.Info(fmt.Sprintf("Stopping %s controller", gvr.Resource))
	}

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx, c.queue, c.syncOwnedResource) }, time.Second, ctx.Done())
		go wait.Until(func() { c.startWorker(ctx, c.dependentQueue, c.syncExternalResource) }, time.Second, ctx.Done())
	}

	<-ctx.Done()

	if c.configWatcher != nil {
		klog.Error(c.configWatcher.Close())
	}
}

func (c *Controller) Name() string {
	return v1alpha1.SpiceDBClusterResourceName
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

func (c *Controller) watchConfig() {
	if len(c.configFilePath) == 0 {
		return
	}
	for {
		select {
		case event, ok := <-c.configWatcher.Events:
			if !ok {
				return
			}
			if !(event.Op&fsnotify.Write == fsnotify.Write ||
				event.Op&fsnotify.Create == fsnotify.Create ||
				event.Op&fsnotify.Rename == fsnotify.Rename ||
				// chmod is the event from a configmap reload in kube
				event.Op&fsnotify.Chmod == fsnotify.Chmod) {
				return
			}
			c.loadConfig()

			// requeue all clusters when config changes
			clusters, err := c.ListerFor(v1alpha1ClusterGVR).List(labels.Everything())
			if err != nil {
				utilruntime.HandleError(err)
				return
			}
			for _, cluster := range clusters {
				c.enqueue(v1alpha1ClusterGVR, c.queue, cluster)
			}
		case err, ok := <-c.configWatcher.Errors:
			if !ok {
				return
			}
			utilruntime.HandleError(fmt.Errorf("error watching config file: %w", err))
		}
	}
}

func (c *Controller) loadConfig() {
	if len(c.configFilePath) == 0 {
		return
	}
	file, err := os.Open(c.configFilePath)
	if err != nil {
		panic(err)
	}
	defer func() {
		utilruntime.HandleError(file.Close())
	}()
	decoder := yaml.NewYAMLOrJSONDecoder(file, 100)
	c.configLock.Lock()
	defer c.configLock.Unlock()
	if err := decoder.Decode(&c.config); err != nil {
		panic(err)
	}
	if len(c.config.ImageName)+len(c.config.ImageTag)+len(c.config.ImageDigest) == 0 {
		panic(fmt.Errorf("unable to load config from %s", c.configFilePath))
	}
}

func (c *Controller) enqueue(gvr schema.GroupVersionResource, queue workqueue.RateLimitingInterface, obj interface{}) {
	key, err := GVRMetaNamespaceKeyFunc(gvr, obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	queue.Add(key)
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

	ctx, cancel := context.WithCancel(ctx)

	done := func() {
		cancel()
		c.queue.Forget(key)
	}
	requeue := func(after time.Duration) {
		cancel()
		if after == 0 {
			c.queue.AddRateLimited(key)
			return
		}
		c.queue.AddAfter(key, after)
	}

	go sync(ctx, *gvr, namespace, name, done, requeue)
	<-ctx.Done()

	return true
}

// syncFunc - an error returned here will do a rate-limited requeue of the object's key
type syncFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, done func(), requeue func(duration time.Duration))

// syncOwnedResource is called when SpiceDBCluster is updated
func (c *Controller) syncOwnedResource(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, done func(), requeue func(duration time.Duration)) {
	if gvr.GroupResource() != spiceDBClusterGR {
		utilruntime.HandleError(fmt.Errorf("syncOwnedResource called on unknown gvr: %s", gvr.String()))
		done()
		return
	}
	obj, err := c.ListerFor(gvr).ByNamespace(namespace).Get(name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("syncOwnedResource called on unknown object (%s::%s/%s): %w", gvr.String(), namespace, name, err))
		done()
		return
	}

	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("syncOwnedResource called with invalid object %T", obj))
		done()
		return
	}

	var cluster v1alpha1.SpiceDBCluster
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &cluster); err != nil {
		utilruntime.HandleError(fmt.Errorf("syncOwnedResource called with invalid object: %w", err))
		done()
		return
	}

	klog.V(4).Infof("syncing %s %s", gvr, klog.KObj(&cluster))

	r := SpiceDBClusterHandler{
		done:      done,
		requeue:   requeue,
		cluster:   &cluster,
		client:    c.client,
		kclient:   c.kclient,
		informers: c.informers,
		recorder:  c.recorder,
	}
	c.configLock.RLock()
	// TODO: pull in an image spec library
	if len(c.config.ImageDigest) > 0 {
		r.spiceDBImage = strings.Join([]string{c.config.ImageName, c.config.ImageDigest}, "@")
	} else {
		r.spiceDBImage = strings.Join([]string{c.config.ImageName, c.config.ImageTag}, ":")
	}
	c.configLock.RUnlock()

	r.Handle(ctx)
}

// syncExternalResource is called when a dependent resource is updated;
// It queues the owning SpiceDBCluster for reconciliation based on the labels.
// No other reconciliation should take place here; we keep a single state
// machine for SpiceDBClusters with an entrypoint in syncCluster
func (c *Controller) syncExternalResource(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, done func(), requeue func(duration time.Duration)) {
	obj, err := c.ListerFor(gvr).ByNamespace(namespace).Get(name)
	if err != nil {
		utilruntime.HandleError(err)
		done()
		return
	}

	objMeta, err := meta.Accessor(obj)
	if err != nil {
		utilruntime.HandleError(err)
		done()
		return
	}

	klog.V(4).Infof("syncing %s %s", gvr, objMeta)

	clusterLabels := objMeta.GetLabels()
	clusterName, ok := clusterLabels[OwnerLabelKey]
	if !ok {
		utilruntime.HandleError(fmt.Errorf("synced %s %s/%s is managed by the operator but not associated with any cluster", obj.GetObjectKind(), objMeta.GetNamespace(), objMeta.GetName()))
		done()
		return
	}

	nn := types.NamespacedName{Name: clusterName, Namespace: objMeta.GetNamespace()}
	c.queue.AddRateLimited(GVRMetaNamespaceKeyer(v1alpha1ClusterGVR, nn.String()))
}
