package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/cespare/xxhash/v2"
	"go.uber.org/atomic"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	_ "k8s.io/component-base/metrics/prometheus/workqueue" // for workqueue metric registration
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlers"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/adopt"
	"github.com/authzed/spicedb-operator/pkg/libctrl/cachekeys"
	"github.com/authzed/spicedb-operator/pkg/libctrl/manager"
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

type OperatorConfig struct {
	ImageName     string   `json:"imageName"`
	ImageTag      string   `json:"imageTag"`
	ImageDigest   string   `json:"imageDigest"`
	AllowedTags   []string `json:"allowedTags"`
	AllowedImages []string `json:"allowedImages"`
}

var (
	OwnedFactoryKey     = typed.NewFactoryKey(v1alpha1.SpiceDBClusterResourceName, "local", "unfiltered")
	DependentFactoryKey = typed.NewFactoryKey(v1alpha1.SpiceDBClusterResourceName, "local", "dependents")
)

type Controller struct {
	*manager.OwnedResourceController
	client  dynamic.Interface
	kclient kubernetes.Interface

	// config
	configLock     sync.RWMutex
	config         OperatorConfig
	lastConfigHash atomic.Uint64
}

func NewController(ctx context.Context, registry *typed.Registry, dclient dynamic.Interface, kclient kubernetes.Interface, configFilePath string, broadcaster record.EventBroadcaster) (*Controller, error) {
	c := Controller{
		client:  dclient,
		kclient: kclient,
	}
	c.OwnedResourceController = manager.NewOwnedResourceController(v1alpha1.SpiceDBClusterResourceName, v1alpha1ClusterGVR, registry, broadcaster, c.syncOwnedResource)

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
	ownedInformerFactory.ForResource(v1alpha1ClusterGVR).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(v1alpha1ClusterGVR, obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(v1alpha1ClusterGVR, obj) },
		// Delete is not used right now, we rely on ownerrefs to clean up
	})

	externalInformerFactory := registry.MustNewFilteredDynamicSharedInformerFactory(
		DependentFactoryKey,
		dclient,
		0,
		metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			options.LabelSelector = metadata.ManagedDependentSelector.String()
		},
	)

	for _, gvr := range []schema.GroupVersionResource{
		appsv1.SchemeGroupVersion.WithResource("deployments"),
		corev1.SchemeGroupVersion.WithResource("secrets"),
		corev1.SchemeGroupVersion.WithResource("serviceaccounts"),
		corev1.SchemeGroupVersion.WithResource("services"),
		corev1.SchemeGroupVersion.WithResource("pods"),
		batchv1.SchemeGroupVersion.WithResource("jobs"),
		rbacv1.SchemeGroupVersion.WithResource("roles"),
		rbacv1.SchemeGroupVersion.WithResource("rolebindings"),
	} {
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

	return &c, nil
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
	contents, err := io.ReadAll(file)
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

	klog.V(3).InfoS("updated config", "path", path, "config", c.config)

	// requeue all clusters
	lister := typed.ListerFor[*v1alpha1.SpiceDBCluster](c.Registry, typed.NewRegistryKey(OwnedFactoryKey, v1alpha1ClusterGVR))
	clusters, err := lister.List(labels.Everything())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	for _, cluster := range clusters {
		c.enqueue(v1alpha1ClusterGVR, cluster)
	}
}

func (c *Controller) enqueue(gvr schema.GroupVersionResource, obj interface{}) {
	key, err := cachekeys.GVRMetaNamespaceKeyFunc(gvr, obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.Queue.AddRateLimited(key)
}

// syncOwnedResource is called when SpiceDBCluster is updated
func (c *Controller) syncOwnedResource(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) {
	cluster, err := typed.ListerFor[*v1alpha1.SpiceDBCluster](c.Registry, typed.NewRegistryKey(OwnedFactoryKey, v1alpha1ClusterGVR)).ByNamespace(namespace).Get(name)
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
		registry: c.Registry,
		cluster:  cluster,
		client:   c.client,
		kclient:  c.kclient,
		recorder: c.Recorder,
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
		c.Queue.AddRateLimited(cachekeys.GVRMetaNamespaceKeyer(v1alpha1ClusterGVR, k))
	}
}
