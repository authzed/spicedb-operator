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

	"github.com/authzed/controller-idioms/adopt"
	"github.com/authzed/controller-idioms/cachekeys"
	"github.com/authzed/controller-idioms/component"
	"github.com/authzed/controller-idioms/fileinformer"
	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"
	"github.com/authzed/controller-idioms/manager"
	"github.com/authzed/controller-idioms/middleware"
	"github.com/authzed/controller-idioms/typed"
	"github.com/cespare/xxhash/v2"
	"github.com/go-logr/logr"
	"go.uber.org/atomic"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	_ "k8s.io/component-base/metrics/prometheus/workqueue" // for workqueue metric registration
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	"k8s.io/kubectl/pkg/util/openapi"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

// +kubebuilder:rbac:groups="authzed.com",resources=spicedbclusters,verbs=get;watch;list;create;update;patch;delete
// +kubebuilder:rbac:groups="authzed.com",resources=spicedbclusters/status,verbs=get;watch;list;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
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

var v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)

func localClusterNamespace(ns string) string {
	if ns == "" {
		return "local"
	}
	return strings.Join([]string{"local", ns}, "/")
}

func OwnedFactoryKey(namespace string) typed.FactoryKey {
	return typed.NewFactoryKey(v1alpha1.SpiceDBClusterResourceName, localClusterNamespace(namespace), "unfiltered")
}

func DependentFactoryKey(namespace string) typed.FactoryKey {
	return typed.NewFactoryKey(v1alpha1.SpiceDBClusterResourceName, localClusterNamespace(namespace), "dependents")
}

type Controller struct {
	*manager.OwnedResourceController
	namespaces  []string
	client      dynamic.Interface
	kclient     kubernetes.Interface
	resources   openapi.Resources
	mainHandler handler.Handler

	// config
	configLock     sync.RWMutex
	config         config.OperatorConfig
	lastConfigHash atomic.Uint64
}

func NewController(ctx context.Context, registry *typed.Registry, dclient dynamic.Interface, kclient kubernetes.Interface, resources openapi.Resources, configFilePath string, broadcaster record.EventBroadcaster, namespaces []string) (*Controller, error) {
	// If no namespaces are provided, watch all namespaces
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}

	c := Controller{
		client:     dclient,
		kclient:    kclient,
		resources:  resources,
		namespaces: namespaces,
	}
	c.OwnedResourceController = manager.NewOwnedResourceController(
		textlogger.NewLogger(textlogger.NewConfig()),
		v1alpha1.SpiceDBClusterResourceName,
		v1alpha1ClusterGVR,
		QueueOps,
		registry,
		broadcaster,
		c.syncOwnedResource,
	)

	fileInformerFactory, err := fileinformer.NewFileInformerFactory(textlogger.NewLogger(textlogger.NewConfig()))
	if err != nil {
		return nil, err
	}

	if len(configFilePath) > 0 {
		inf := fileInformerFactory.ForResource(fileinformer.FileGroupVersion.WithResource(configFilePath)).Informer()
		if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(_ any) { c.loadConfig(configFilePath) },
			UpdateFunc: func(_, _ any) { c.loadConfig(configFilePath) },
			DeleteFunc: func(_ any) { c.loadConfig(configFilePath) },
		}); err != nil {
			return nil, err
		}
	} else {
		logr.FromContextOrDiscard(ctx).V(3).Info("no operator configuration provided", "path", configFilePath)
	}

	ownedInformerFactories := make([]dynamicinformer.DynamicSharedInformerFactory, 0, len(namespaces))
	for _, ns := range namespaces {
		ownedInformerFactory := registry.MustNewFilteredDynamicSharedInformerFactory(
			OwnedFactoryKey(ns),
			dclient,
			0,
			ns,
			nil,
		)
		if _, err := ownedInformerFactory.ForResource(v1alpha1ClusterGVR).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { c.enqueue(v1alpha1ClusterGVR, obj) },
			UpdateFunc: func(_, obj any) { c.enqueue(v1alpha1ClusterGVR, obj) },
			// Delete is not used right now, we rely on ownerrefs to clean up
		}); err != nil {
			return nil, err
		}
		ownedInformerFactories = append(ownedInformerFactories, ownedInformerFactory)
	}

	externalInformerFactories := make([]dynamicinformer.DynamicSharedInformerFactory, 0, len(namespaces))
	for _, ns := range namespaces {
		externalInformerFactory := registry.MustNewFilteredDynamicSharedInformerFactory(
			DependentFactoryKey(ns),
			dclient,
			0,
			ns,
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
			if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc:    func(obj any) { c.syncExternalResource(obj) },
				UpdateFunc: func(_, obj any) { c.syncExternalResource(obj) },
				DeleteFunc: func(obj any) { c.syncExternalResource(obj) },
			}); err != nil {
				return nil, err
			}
		}
		externalInformerFactories = append(externalInformerFactories, externalInformerFactory)
	}

	// start informers
	for _, ownedInformerFactory := range ownedInformerFactories {
		ownedInformerFactory.Start(ctx.Done())
	}

	for _, externalInformerFactory := range externalInformerFactories {
		externalInformerFactory.Start(ctx.Done())
	}

	fileInformerFactory.Start(ctx.Done())

	// wait for caches to sync
	for _, ownedInformerFactory := range ownedInformerFactories {
		ownedInformerFactory.WaitForCacheSync(ctx.Done())
	}

	for _, externalInformerFactory := range externalInformerFactories {
		externalInformerFactory.WaitForCacheSync(ctx.Done())
	}
	fileInformerFactory.WaitForCacheSync(ctx.Done())

	// Build mainHandler handler
	mw := middleware.NewHandlerLoggingMiddleware(4)
	chain := middleware.ChainWithMiddleware(mw)
	parallel := middleware.ParallelWithMiddleware(mw)

	deploymentHandlerChain := chain(
		c.ensureDeployment,
		c.cleanupJob,
	).Handler(HandlerDeploymentKey)

	waitForMigrationsChain := c.waitForMigrationsHandler(
		deploymentHandlerChain,
		c.selfPauseCluster(handler.NoopHandler),
	).WithID(HandlerWaitForMigrationsKey)

	c.mainHandler = chain(
		c.pauseCluster,
		c.secretAdopter,
		c.checkConfigChanged,
		c.validateConfig,
		parallel(
			c.ensureServiceAccount,
			c.ensureRole,
			c.ensureService,
		),
		c.ensureRoleBinding,
		CtxDeployments.BoxBuilder("deploymentsPre"),
		CtxJobs.BoxBuilder("jobsPre"),
		parallel(
			c.getDeployments,
			c.getJobs,
		),
		c.checkMigrations(
			deploymentHandlerChain,
			chain(
				c.runMigration,
				waitForMigrationsChain.Builder(),
			).Handler(HandlerMigrationRunKey),
			waitForMigrationsChain,
		).Builder(),
	).Handler("controller")

	return &c, nil
}

func (c *Controller) loadConfig(path string) {
	if len(path) == 0 {
		return
	}

	logger := textlogger.NewLogger(textlogger.NewConfig())
	logger.V(3).Info("loading config", "path", path)

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
	var cfg config.OperatorConfig
	if err := decoder.Decode(&cfg); err != nil {
		panic(err)
	}

	if h := xxhash.Sum64(contents); h != c.lastConfigHash.Load() {
		func() {
			c.configLock.Lock()
			defer c.configLock.Unlock()
			c.config = cfg
		}()
		c.lastConfigHash.Store(h)
	} else {
		// config hasn't changed
		logger.V(4).Info("config hasn't changed", "old hash", c.lastConfigHash.Load(), "new hash", h)
		return
	}

	logger.V(3).Info("updated config", "path", path, "config", c.config)

	// requeue all clusters
	for _, ns := range c.namespaces {
		lister := typed.MustListerForKey[*v1alpha1.SpiceDBCluster](c.Registry, typed.NewRegistryKey(OwnedFactoryKey(ns), v1alpha1ClusterGVR))
		clusters, err := lister.List(labels.Everything())
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		for _, cluster := range clusters {
			c.enqueue(v1alpha1ClusterGVR, cluster)
		}
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
	// configure watch namespace; used to lookup the right caches to use
	if len(c.namespaces) == 1 && c.namespaces[0] == corev1.NamespaceAll {
		ctx = CtxCacheNamespace.WithValue(ctx, corev1.NamespaceAll)
	} else {
		ctx = CtxCacheNamespace.WithValue(ctx, namespace)
	}

	cluster, err := typed.MustListerForKey[*v1alpha1.SpiceDBCluster](c.Registry, typed.NewRegistryKey(OwnedFactoryKey(CtxCacheNamespace.Value(ctx)), v1alpha1ClusterGVR)).ByNamespace(namespace).Get(name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("syncOwnedResource called on unknown object (%s::%s/%s): %w", gvr.String(), namespace, name, err))
		QueueOps.Done(ctx)
		return
	}

	logger := textlogger.NewLogger(textlogger.NewConfig()).WithValues(
		"syncID", middleware.NewSyncID(5),
		"controller", c.Name(),
		"obj", klog.KObj(cluster).MarshalLog(),
	)
	ctx = logr.NewContext(ctx, logger)

	ctx = CtxCluster.WithValue(ctx, cluster.DeepCopy())
	ctx = CtxClusterNN.WithValue(ctx, cluster.NamespacedName())
	ctx = CtxSecretNN.WithValue(ctx, types.NamespacedName{
		Name:      cluster.Spec.SecretRef,
		Namespace: cluster.Namespace,
	})

	c.configLock.RLock()
	cfg := c.config.Copy()
	ctx = CtxOperatorConfig.WithValue(ctx, &cfg)
	c.configLock.RUnlock()

	logger.V(4).Info("syncing owned object", "gvr", gvr)

	c.Handle(ctx)
}

// syncExternalResource is called when a dependent resource is updated:
// It queues the owning SpiceDBCluster for reconciliation based on the labels.
// No other reconciliation should take place here; we keep a single state
// machine for SpiceDBCluster with an entrypoint in the mainHandler Handler
func (c *Controller) syncExternalResource(obj interface{}) {
	objMeta, err := meta.Accessor(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := textlogger.NewLogger(textlogger.NewConfig()).WithValues(
		"syncID", middleware.NewSyncID(5),
		"controller", c.Name(),
		"obj", klog.KObj(objMeta),
	)
	logger.V(4).Info("syncing external object")

	keys, err := adopt.OwnerKeysFromMeta(metadata.OwnerAnnotationKeyPrefix)(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, k := range keys {
		c.Queue.AddRateLimited(cachekeys.GVRMetaNamespaceKeyer(v1alpha1ClusterGVR, k))
	}
}

// Handle inspects the current SpiceDBCluster object and ensures
// the desired state is persisted on the cluster.
func (c *Controller) Handle(ctx context.Context) {
	c.mainHandler.Handle(ctx)
}

func (c *Controller) ensureDeployment(next ...handler.Handler) handler.Handler {
	return handler.NewTypeHandler(&DeploymentHandler{
		applyDeployment: func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error) {
			logr.FromContextOrDiscard(ctx).V(4).Info("updating deployment", "namespace", *dep.Namespace, "name", *dep.Name)
			return c.kclient.AppsV1().Deployments(*dep.Namespace).Apply(ctx, dep, metadata.ApplyForceOwned)
		},
		deleteDeployment: func(ctx context.Context, nn types.NamespacedName) error {
			logr.FromContextOrDiscard(ctx).V(4).Info("deleting deployment", "namespace", nn.Namespace, "name", nn.Name)
			return c.kclient.AppsV1().Deployments(nn.Namespace).Delete(ctx, nn.Name, metav1.DeleteOptions{})
		},
		getDeploymentPods: func(ctx context.Context) []*corev1.Pod {
			return component.NewIndexedComponent(
				typed.MustIndexerForKey[*corev1.Pod](c.Registry, typed.NewRegistryKey(DependentFactoryKey(CtxCacheNamespace.Value(ctx)), corev1.SchemeGroupVersion.WithResource("pods"))),
				metadata.OwningClusterIndex,
				func(ctx context.Context) labels.Selector {
					return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentSpiceDBLabelValue)
				},
			).List(ctx, CtxClusterNN.MustValue(ctx))
		},
		patchStatus: c.PatchStatus,
		next:        handler.Handlers(next).MustOne(),
	})
}

func (c *Controller) cleanupJob(...handler.Handler) handler.Handler {
	return handler.NewTypeHandler(&JobCleanupHandler{
		registry: c.Registry,
		getJobs: func(ctx context.Context) []*batchv1.Job {
			return component.NewIndexedComponent(
				typed.MustIndexerForKey[*batchv1.Job](c.Registry, typed.NewRegistryKey(DependentFactoryKey(CtxCacheNamespace.Value(ctx)), batchv1.SchemeGroupVersion.WithResource("jobs"))),
				metadata.OwningClusterIndex,
				func(ctx context.Context) labels.Selector {
					return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentMigrationJobLabelValue)
				}).List(ctx, CtxClusterNN.MustValue(ctx))
		},
		getJobPods: func(ctx context.Context) []*corev1.Pod {
			return component.NewIndexedComponent(
				typed.MustIndexerForKey[*corev1.Pod](c.Registry, typed.NewRegistryKey(DependentFactoryKey(CtxCacheNamespace.Value(ctx)), corev1.SchemeGroupVersion.WithResource("pods"))),
				metadata.OwningClusterIndex,
				func(ctx context.Context) labels.Selector {
					return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentMigrationJobLabelValue)
				},
			).List(ctx, CtxClusterNN.MustValue(ctx))
		},
		deleteJob: func(ctx context.Context, nn types.NamespacedName) error {
			logr.FromContextOrDiscard(ctx).V(4).Info("deleting job", "namespace", nn.Namespace, "name", nn.Name)
			backgroundPolicy := metav1.DeletePropagationBackground
			return c.kclient.BatchV1().Jobs(nn.Namespace).Delete(ctx, nn.Name, metav1.DeleteOptions{PropagationPolicy: &backgroundPolicy})
		},
		deletePod: func(ctx context.Context, nn types.NamespacedName) error {
			logr.FromContextOrDiscard(ctx).V(4).Info("deleting job pod", "namespace", nn.Namespace, "name", nn.Name)
			return c.kclient.CoreV1().Pods(nn.Namespace).Delete(ctx, nn.Name, metav1.DeleteOptions{})
		},
	})
}

func (c *Controller) waitForMigrationsHandler(next ...handler.Handler) handler.Handler {
	return handler.NewTypeHandler(&WaitForMigrationsHandler{
		recorder:              c.Recorder,
		nextSelfPause:         HandlerSelfPauseKey.MustFind(next),
		nextDeploymentHandler: HandlerDeploymentKey.MustFind(next),
	})
}

func (c *Controller) pauseCluster(next ...handler.Handler) handler.Handler {
	return NewPauseHandler(c.PatchStatus, handler.Handlers(next).MustOne())
}

func (c *Controller) selfPauseCluster(...handler.Handler) handler.Handler {
	return NewSelfPauseHandler(c.Patch, c.PatchStatus)
}

func (c *Controller) secretAdopter(next ...handler.Handler) handler.Handler {
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		NewSecretAdoptionHandler(
			c.Recorder,
			func(ctx context.Context) (*corev1.Secret, error) {
				return typed.MustListerForKey[*corev1.Secret](c.Registry, typed.NewRegistryKey(DependentFactoryKey(CtxCacheNamespace.Value(ctx)), secretsGVR)).ByNamespace(CtxSecretNN.MustValue(ctx).Namespace).Get(CtxSecretNN.MustValue(ctx).Name)
			},
			func(ctx context.Context, err error) {
				cluster := CtxCluster.MustValue(ctx)
				status := &v1alpha1.SpiceDBCluster{
					TypeMeta: metav1.TypeMeta{
						Kind:       v1alpha1.SpiceDBClusterKind,
						APIVersion: v1alpha1.SchemeGroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{Namespace: cluster.Namespace, Name: cluster.Name},
					Status:     *cluster.Status.DeepCopy(),
				}
				status.Status.ObservedGeneration = cluster.GetGeneration()
				status.SetStatusCondition(v1alpha1.NewMissingSecretCondition(types.NamespacedName{
					Namespace: cluster.Namespace,
					Name:      cluster.Spec.SecretRef,
				}))
				if err := c.PatchStatus(ctx, status); err != nil {
					QueueOps.RequeueAPIErr(ctx, err)
				}
				// keep checking to see if the secret is added
				QueueOps.RequeueErr(ctx, err)
			},
			typed.MustIndexerForKey[*corev1.Secret](c.Registry, typed.NewRegistryKey(DependentFactoryKey(CtxCacheNamespace.Value(ctx)), secretsGVR)),
			func(ctx context.Context, secret *applycorev1.SecretApplyConfiguration, options metav1.ApplyOptions) (*corev1.Secret, error) {
				return c.kclient.CoreV1().Secrets(*secret.Namespace).Apply(ctx, secret, options)
			},
			func(ctx context.Context, nn types.NamespacedName) error {
				_, err := c.kclient.CoreV1().Secrets(nn.Namespace).Get(ctx, nn.Name, metav1.GetOptions{})
				return err
			},
			handler.Handlers(next).MustOne(),
		).Handle(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "adoptSecret")
}

func (c *Controller) checkConfigChanged(next ...handler.Handler) handler.Handler {
	return handler.NewTypeHandler(&ConfigChangedHandler{
		patchStatus: c.PatchStatus,
		next:        handler.Handlers(next).MustOne(),
	})
}

func (c *Controller) validateConfig(next ...handler.Handler) handler.Handler {
	return handler.NewTypeHandler(&ValidateConfigHandler{
		patchStatus: c.PatchStatus,
		recorder:    c.Recorder,
		resources:   c.resources,
		next:        handler.Handlers(next).MustOne(),
	})
}

func (c *Controller) getDeployments(next ...handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		component.NewComponentContextHandler[*appsv1.Deployment](
			CtxDeployments,
			component.NewIndexedComponent(
				typed.MustIndexerForKey[*appsv1.Deployment](c.Registry, typed.NewRegistryKey(DependentFactoryKey(CtxCacheNamespace.Value(ctx)), appsv1.SchemeGroupVersion.WithResource("deployments"))),
				metadata.OwningClusterIndex,
				func(ctx context.Context) labels.Selector {
					return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentSpiceDBLabelValue)
				}),
			CtxClusterNN,
			handler.NoopHandler,
		).Handle(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "getDeployments")
}

func (c *Controller) getJobs(next ...handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		component.NewComponentContextHandler[*batchv1.Job](
			CtxJobs,
			component.NewIndexedComponent(
				typed.MustIndexerForKey[*batchv1.Job](c.Registry, typed.NewRegistryKey(DependentFactoryKey(CtxCacheNamespace.Value(ctx)), batchv1.SchemeGroupVersion.WithResource("jobs"))),
				metadata.OwningClusterIndex,
				func(ctx context.Context) labels.Selector {
					return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentMigrationJobLabelValue)
				}),
			CtxClusterNN,
			handler.NoopHandler,
		).Handle(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "getJobs")
}

func (c *Controller) runMigration(next ...handler.Handler) handler.Handler {
	return handler.NewTypeHandler(&MigrationRunHandler{
		applyJob: func(ctx context.Context, job *applybatchv1.JobApplyConfiguration) error {
			_, err := c.kclient.BatchV1().Jobs(*job.Namespace).Apply(ctx, job, metadata.ApplyForceOwned)
			return err
		},
		deleteJob: func(ctx context.Context, nn types.NamespacedName) error {
			return c.kclient.BatchV1().Jobs(nn.Namespace).Delete(ctx, nn.Name, metav1.DeleteOptions{})
		},
		patchStatus: c.PatchStatus,
		next:        handler.Handlers(next).MustOne(),
	})
}

func (c *Controller) checkMigrations(next ...handler.Handler) handler.Handler {
	return handler.NewTypeHandler(&MigrationCheckHandler{
		recorder:                c.Recorder,
		nextMigrationRunHandler: HandlerMigrationRunKey.MustFind(next),
		nextWaitForJobHandler:   HandlerWaitForMigrationsKey.MustFind(next),
		nextDeploymentHandler:   HandlerDeploymentKey.MustFind(next),
	})
}

func (c *Controller) ensureServiceAccount(next ...handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		component.NewEnsureComponentByHash(
			component.NewHashableComponent(
				component.NewIndexedComponent(
					typed.MustIndexerForKey[*corev1.ServiceAccount](
						c.Registry,
						typed.NewRegistryKey(
							DependentFactoryKey(CtxCacheNamespace.Value(ctx)),
							corev1.SchemeGroupVersion.WithResource("serviceaccounts"),
						)),
					metadata.OwningClusterIndex,
					func(ctx context.Context) labels.Selector {
						return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentServiceAccountLabel)
					}),
				hash.NewObjectHash(), "authzed.com/controller-component-hash"),
			CtxClusterNN,
			QueueOps,
			func(ctx context.Context, apply *applycorev1.ServiceAccountApplyConfiguration) (*corev1.ServiceAccount, error) {
				logr.FromContextOrDiscard(ctx).V(4).Info("applying serviceaccount", "namespace", *apply.Namespace, "name", *apply.Name)
				return c.kclient.CoreV1().ServiceAccounts(*apply.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
			},
			func(ctx context.Context, nn types.NamespacedName) error {
				logr.FromContextOrDiscard(ctx).V(4).Info("deleting serviceaccount", "namespace", nn.Namespace, "name", nn.Name)
				return c.kclient.CoreV1().ServiceAccounts(nn.Namespace).Delete(ctx, nn.Name, metav1.DeleteOptions{})
			},
			func(ctx context.Context) *applycorev1.ServiceAccountApplyConfiguration {
				return CtxConfig.MustValue(ctx).ServiceAccount()
			}).Handle(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "ensureServiceAccount")
}

func (c *Controller) ensureRole(next ...handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		component.NewEnsureComponentByHash(
			component.NewHashableComponent(
				component.NewIndexedComponent(
					typed.MustIndexerForKey[*rbacv1.Role](
						c.Registry,
						typed.NewRegistryKey(
							DependentFactoryKey(CtxCacheNamespace.Value(ctx)),
							rbacv1.SchemeGroupVersion.WithResource("roles"),
						)),
					metadata.OwningClusterIndex,
					func(ctx context.Context) labels.Selector {
						return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentRoleLabel)
					}),
				hash.NewObjectHash(), "authzed.com/controller-component-hash"),
			CtxClusterNN,
			QueueOps,
			func(ctx context.Context, apply *applyrbacv1.RoleApplyConfiguration) (*rbacv1.Role, error) {
				logr.FromContextOrDiscard(ctx).V(4).Info("applying role", "namespace", *apply.Namespace, "name", *apply.Name)
				return c.kclient.RbacV1().Roles(*apply.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
			},
			func(ctx context.Context, nn types.NamespacedName) error {
				logr.FromContextOrDiscard(ctx).V(4).Info("deleting role", "namespace", nn.Namespace, "name", nn.Name)
				return c.kclient.RbacV1().Roles(nn.Namespace).Delete(ctx, nn.Name, metav1.DeleteOptions{})
			},
			func(ctx context.Context) *applyrbacv1.RoleApplyConfiguration {
				return CtxConfig.MustValue(ctx).Role()
			}).Handle(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "ensureRole")
}

func (c *Controller) ensureRoleBinding(next ...handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		component.NewEnsureComponentByHash(
			component.NewHashableComponent(
				component.NewIndexedComponent(
					typed.MustIndexerForKey[*rbacv1.RoleBinding](
						c.Registry,
						typed.NewRegistryKey(
							DependentFactoryKey(CtxCacheNamespace.Value(ctx)),
							rbacv1.SchemeGroupVersion.WithResource("rolebindings"),
						)),
					metadata.OwningClusterIndex,
					func(ctx context.Context) labels.Selector {
						return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentRoleBindingLabel)
					}),
				hash.NewObjectHash(), "authzed.com/controller-component-hash"),
			CtxClusterNN,
			QueueOps,
			func(ctx context.Context, apply *applyrbacv1.RoleBindingApplyConfiguration) (*rbacv1.RoleBinding, error) {
				logr.FromContextOrDiscard(ctx).V(4).Info("applying rolebinding", "namespace", *apply.Namespace, "name", *apply.Name)
				return c.kclient.RbacV1().RoleBindings(*apply.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
			},
			func(ctx context.Context, nn types.NamespacedName) error {
				logr.FromContextOrDiscard(ctx).V(4).Info("deleting rolebinding", "namespace", nn.Namespace, "name", nn.Name)
				return c.kclient.RbacV1().RoleBindings(nn.Namespace).Delete(ctx, nn.Name, metav1.DeleteOptions{})
			},
			func(ctx context.Context) *applyrbacv1.RoleBindingApplyConfiguration {
				return CtxConfig.MustValue(ctx).RoleBinding()
			},
		).Handle(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "ensureRoleBinding")
}

func (c *Controller) ensureService(next ...handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		component.NewEnsureComponentByHash(
			component.NewHashableComponent(
				component.NewIndexedComponent(
					typed.MustIndexerForKey[*corev1.Service](
						c.Registry,
						typed.NewRegistryKey(
							DependentFactoryKey(CtxCacheNamespace.Value(ctx)),
							corev1.SchemeGroupVersion.WithResource("services"),
						)),
					metadata.OwningClusterIndex,
					func(ctx context.Context) labels.Selector {
						return metadata.SelectorForComponent(CtxClusterNN.MustValue(ctx).Name, metadata.ComponentServiceLabel)
					}),
				hash.NewObjectHash(), "authzed.com/controller-component-hash"),
			CtxClusterNN,
			QueueOps,
			func(ctx context.Context, apply *applycorev1.ServiceApplyConfiguration) (*corev1.Service, error) {
				logr.FromContextOrDiscard(ctx).V(4).Info("applying service", "namespace", *apply.Namespace, "name", *apply.Name)
				return c.kclient.CoreV1().Services(*apply.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
			},
			func(ctx context.Context, nn types.NamespacedName) error {
				logr.FromContextOrDiscard(ctx).V(4).Info("deleting service", "namespace", nn.Namespace, "name", nn.Name)
				return c.kclient.CoreV1().Services(nn.Namespace).Delete(ctx, nn.Name, metav1.DeleteOptions{})
			},
			func(ctx context.Context) *applycorev1.ServiceApplyConfiguration {
				return CtxConfig.MustValue(ctx).Service()
			}).Handle(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "ensureService")
}
