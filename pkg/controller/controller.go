package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"go.uber.org/atomic"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/component-base/metrics/legacyregistry"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	_ "k8s.io/component-base/metrics/prometheus/workqueue" // for workqueue metric registration
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlers"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/adopt"
	"github.com/authzed/spicedb-operator/pkg/libctrl/cachekeys"
	"github.com/authzed/spicedb-operator/pkg/libctrl/manager"
	ctrlmetrics "github.com/authzed/spicedb-operator/pkg/libctrl/metrics"
	"github.com/authzed/spicedb-operator/pkg/libctrl/middleware"
	"github.com/authzed/spicedb-operator/pkg/libctrl/typed"
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
	spiceDBClusterMetrics = ctrlmetrics.NewConditionStatusCollector[*v1alpha1.SpiceDBCluster]("spicedb_operator", "clusters", v1alpha1.SpiceDBClusterResourceName)

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

type OperatorConfig struct {
	ImageName     string   `json:"imageName"`
	ImageTag      string   `json:"imageTag"`
	ImageDigest   string   `json:"imageDigest"`
	AllowedTags   []string `json:"allowedTags"`
	AllowedImages []string `json:"allowedImages"`
}

type Controller struct {
	*manager.BasicController
	registry *typed.Registry
	queue    workqueue.RateLimitingInterface
	client   dynamic.Interface
	recorder record.EventRecorder
	kclient  kubernetes.Interface

	// config
	configLock     sync.RWMutex
	config         OperatorConfig
	lastConfigHash atomic.Uint64
}

var _ manager.Controller = &Controller{}

var (
	OwnedFactoryKey     = typed.NewFactoryKey(v1alpha1.SpiceDBClusterResourceName, "local", "unfiltered")
	DependentFactoryKey = typed.NewFactoryKey(v1alpha1.SpiceDBClusterResourceName, "local", "dependents")
)

func NewController(ctx context.Context, registry *typed.Registry, dclient dynamic.Interface, kclient kubernetes.Interface, configFilePath string) (*Controller, error) {
	c := &Controller{
		BasicController: manager.NewBasicController(v1alpha1.SpiceDBClusterResourceName),
		registry:        registry,
		kclient:         kclient,
		client:          dclient,
		queue:           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "cluster_queue"),
	}

	fileInformerFactory, err := libctrl.NewFileInformerFactory()
	if err != nil {
		return nil, err
	}

	if len(configFilePath) > 0 {
		inf := fileInformerFactory.ForResource(libctrl.FileGroupVersion.WithResource(configFilePath)).Informer()
		inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.loadConfig(configFilePath) },
			UpdateFunc: func(_, obj interface{}) { c.loadConfig(configFilePath) },
			DeleteFunc: func(obj interface{}) { c.loadConfig(configFilePath) },
		})
	} else {
		klog.V(3).InfoS("no operator configuration provided", "path", configFilePath)
	}

	ownedInformerFactory := registry.MustNewFilteredDynamicSharedInformerFactory(
		OwnedFactoryKey,
		dclient,
		0,
		metav1.NamespaceAll,
		nil,
	)
	externalInformerFactory := registry.MustNewFilteredDynamicSharedInformerFactory(
		DependentFactoryKey,
		dclient,
		0,
		metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			options.LabelSelector = metadata.ManagedDependentSelector.String()
		},
	)

	for _, gvr := range OwnedResources {
		gvr := gvr
		ownedInformerFactory.ForResource(gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueue(gvr, c.queue, obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueue(gvr, c.queue, obj) },
			// Delete is not used right now, we rely on ownerrefs to clean up
		})
	}

	for _, gvr := range ExternalResources {
		gvr := gvr
		inf := externalInformerFactory.ForResource(gvr).Informer()
		if err := inf.AddIndexers(cache.Indexers{metadata.OwningClusterIndex: metadata.GetClusterKeyFromMeta}); err != nil {
			return nil, err
		}
		inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.syncExternalResource(obj) },
			UpdateFunc: func(_, obj interface{}) { c.syncExternalResource(obj) },
			DeleteFunc: func(obj interface{}) { c.syncExternalResource(obj) },
		})
	}

	// start informers
	ownedInformerFactory.Start(ctx.Done())
	externalInformerFactory.Start(ctx.Done())
	fileInformerFactory.Start(ctx.Done())
	ownedInformerFactory.WaitForCacheSync(ctx.Done())
	externalInformerFactory.WaitForCacheSync(ctx.Done())
	fileInformerFactory.WaitForCacheSync(ctx.Done())

	// register with metrics collector
	lister := typed.ListerFor[*v1alpha1.SpiceDBCluster](c.registry, typed.NewRegistryKey(OwnedFactoryKey, v1alpha1ClusterGVR))
	spiceDBClusterMetrics.AddListerBuilder(func() ([]*v1alpha1.SpiceDBCluster, error) {
		return lister.List(labels.Everything())
	})

	return c, nil
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	legacyregistry.CustomMustRegister(spiceDBClusterMetrics)

	broadcaster := record.NewBroadcaster()
	defer broadcaster.Shutdown()

	broadcaster.StartStructuredLogging(0)
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: c.kclient.CoreV1().Events("")})
	c.recorder = broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "spicedb-operator"})

	for _, gvr := range OwnedResources {
		klog.V(3).InfoS("starting controller", "resource", gvr.Resource)
		defer klog.V(3).InfoS("stopping controller", "resource", gvr.Resource)
	}

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx, c.queue, c.syncOwnedResource) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (c *Controller) loadConfig(path string) {
	if len(path) == 0 {
		return
	}
	klog.V(3).InfoS("loading config", "path", path)
	file, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer func() {
		utilruntime.HandleError(file.Close())
	}()
	contents, err := ioutil.ReadAll(file)
	if err != nil {
		panic(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(contents), 100)
	var config OperatorConfig
	if err := decoder.Decode(&config); err != nil {
		panic(err)
	}

	if len(config.ImageName)+len(config.ImageTag) == 0 && len(config.ImageName)+len(config.ImageDigest) == 0 {
		panic(fmt.Errorf("unable to load config from %s", path))
	}

	if hash := xxhash.Sum64(contents); hash != c.lastConfigHash.Load() {
		func() {
			c.configLock.Lock()
			defer c.configLock.Unlock()
			c.config = config
		}()
		c.lastConfigHash.Store(hash)
	} else {
		// config hasn't changed
		return
	}

	klog.V(4).InfoS("updated config", "path", path, "config", c.config)

	// requeue all clusters
	lister := typed.ListerFor[*v1alpha1.SpiceDBCluster](c.registry, typed.NewRegistryKey(OwnedFactoryKey, v1alpha1ClusterGVR))
	clusters, err := lister.List(labels.Everything())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	for _, cluster := range clusters {
		c.enqueue(v1alpha1ClusterGVR, c.queue, cluster)
	}
}

func (c *Controller) enqueue(gvr schema.GroupVersionResource, queue workqueue.RateLimitingInterface, obj interface{}) {
	key, err := cachekeys.GVRMetaNamespaceKeyFunc(gvr, obj)
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
		return true
	}

	gvr, namespace, name, err := cachekeys.SplitGVRMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("error parsing key %q, skipping", key))
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

	ctx = handlers.CtxHandlerControls.WithValue(ctx, libctrl.NewHandlerControls(done, requeue))

	sync(ctx, *gvr, namespace, name)
	cancel()
	<-ctx.Done()

	return true
}

// syncFunc processes a single object from an informer
type syncFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string)

// syncOwnedResource is called when SpiceDBCluster is updated
func (c *Controller) syncOwnedResource(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) {
	if gvr != OwnedResources[0] {
		utilruntime.HandleError(fmt.Errorf("syncOwnedResource called on unknown gvr: %s", gvr.String()))
		handlers.CtxHandlerControls.Done(ctx)
		return
	}

	cluster, err := typed.ListerFor[*v1alpha1.SpiceDBCluster](c.registry, typed.NewRegistryKey(OwnedFactoryKey, v1alpha1ClusterGVR)).ByNamespace(namespace).Get(name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("syncOwnedResource called on unknown object (%s::%s/%s): %w", gvr.String(), namespace, name, err))
		handlers.CtxHandlerControls.Done(ctx)
		return
	}

	logger := klog.LoggerWithValues(klog.Background(),
		"syncID", middleware.NewSyncID(5),
		"controller", c.Name(),
		"obj", klog.KObj(cluster),
	)
	ctx = klog.NewContext(ctx, logger)

	ctx = handlers.CtxClusterStatus.WithValue(ctx, cluster)

	logger.V(4).Info("syncing owned object", "gvr", gvr)

	r := SpiceDBClusterHandler{
		registry: c.registry,
		cluster:  cluster,
		client:   c.client,
		kclient:  c.kclient,
		recorder: c.recorder,
	}
	c.configLock.RLock()
	// TODO: pull in an image spec library
	if c.config.ImageName == "" {
		utilruntime.HandleError(errors.New("spicedb image name not specified"))
	}
	if len(c.config.ImageDigest) > 0 {
		r.defaultSpiceDBImage = strings.Join([]string{c.config.ImageName, c.config.ImageDigest}, "@")
	} else {
		r.defaultSpiceDBImage = strings.Join([]string{c.config.ImageName, c.config.ImageTag}, ":")
	}
	r.allowedSpiceDBImages = c.config.AllowedImages
	r.allowedSpiceDBTags = c.config.AllowedTags
	c.configLock.RUnlock()

	r.Handle(ctx)
}

// syncExternalResource is called when a dependent resource is updated:
// It queues the owning SpiceDBCluster for reconciliation based on the labels.
// No other reconciliation should take place here; we keep a single state
// machine for PermissionSystem with an entrypoint in the main Handler
func (c *Controller) syncExternalResource(obj interface{}) {
	objMeta, err := meta.Accessor(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	klog.V(4).InfoS("syncing external object", "obj", klog.KObj(objMeta))

	keys, err := adopt.OwnerKeysFromMeta(metadata.OwnerAnnotationKeyPrefix)(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, k := range keys {
		c.queue.AddRateLimited(cachekeys.GVRMetaNamespaceKeyer(v1alpha1ClusterGVR, k))
	}
}
