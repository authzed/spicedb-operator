package cluster

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

const (
	OwnerLabelKey                   = "authzed.com/cluster"
	ComponentLabelKey               = "authzed.com/cluster-component"
	ComponentSpiceDBLabelValue      = "spicedb"
	ComponentMigrationJobLabelValue = "migration-job"
	ComponentServiceAccountLabel    = "spicedb-serviceaccount"
	ComponentRoleLabel              = "spicedb-role"
	ComponentServiceLabel           = "spicedb-service"
	ComponentRoleBindingLabel       = "spicedb-rolebinding"
	SpiceDBMigrationRequirementsKey = "authzed.com/spicedb-migration"
	SpiceDBConfigKey                = "authzed.com/spicedb-configuration"
)

var (
	v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)
	spiceDBClusterGR   = v1alpha1ClusterGVR.GroupResource()

	forceOwned = metav1.ApplyOptions{FieldManager: "spicedb-operator", Force: true}

	handlerSelfPauseKey         handler.Key = "selfPause"
	handlerDeploymentKey        handler.Key = "deploymentChain"
	handlerMigrationRunKey      handler.Key = "runMigration"
	handlerWaitForMigrationsKey handler.Key = "waitForMigrationChain"
)

// TODO: wait for a specific RV to be seen, with a timeout
// TODO: event emitting handler middleware
// TODO: status set/unset middleware

type SpiceDBClusterHandler struct {
	done         func()
	requeue      func(duration time.Duration)
	cluster      *v1alpha1.SpiceDBCluster
	client       dynamic.Interface
	kclient      kubernetes.Interface
	informers    map[schema.GroupVersionResource]dynamicinformer.DynamicSharedInformerFactory
	recorder     record.EventRecorder
	spiceDBImage string
}

// Handle inspects the current SpiceDBCluster object and ensures
// the desired state is persisted on the cluster.
func (r *SpiceDBClusterHandler) Handle(ctx context.Context) {
	klog.V(4).Infof("syncing cluster %s/%s", r.cluster.Namespace, r.cluster.Name)

	middleware := []libctrl.Middleware{
		libctrl.MakeMiddleware(SyncIDMiddleware),
		libctrl.MakeMiddleware(KlogMiddleware(klog.KObj(r.cluster))),
	}
	chain := libctrl.ChainWithMiddleware(middleware...)
	parallel := libctrl.ParallelWithMiddleware(middleware...)

	deploymentHandlerChain := chain(
		r.ensureDeployment,
		r.cleanupJob,
	).Handler(handlerDeploymentKey)

	waitForMigrationsChain := r.waitForMigrationsHandler(
		deploymentHandlerChain,
		r.selfPauseCluster(handler.NoopHandler),
	).WithID(handlerWaitForMigrationsKey)

	chain(
		r.pauseCluster,
		r.secretAdopter,
		r.checkConfigChanged,
		r.validateConfig,
		parallel(
			r.ensureServiceAccount,
			r.ensureRole,
			r.ensureRoleBinding,
			r.ensureService,
		),
		ctxDeployments.HandleBuilder("deploymentsPre"),
		ctxJobs.HandleBuilder("jobsPre"),
		parallel(
			r.getDeployments,
			r.getJobs,
		),
		r.checkMigrations(
			deploymentHandlerChain,
			chain(
				r.runMigration,
				waitForMigrationsChain.Builder(),
			).Handler(handlerMigrationRunKey),
			waitForMigrationsChain,
		).Builder(),
	).Handler("cluster").Handle(ctx)
}

func (r *SpiceDBClusterHandler) ensureDeployment(next ...handler.Handler) handler.Handler {
	return handler.NewHandler(&deploymentHandler{
		HandlerControls: libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		nn:          r.cluster.NamespacedName(),
		next:        handler.Handlers(next).MustOne(),
		patchStatus: r.PatchStatus,
		applyDeployment: func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error) {
			return r.kclient.AppsV1().Deployments(r.cluster.Namespace).Apply(ctx, dep, forceOwned)
		},
		deleteDeployment: func(ctx context.Context, name string) error {
			return r.kclient.AppsV1().Deployments(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, "ensureDeployment")
}

func (r *SpiceDBClusterHandler) cleanupJob(...handler.Handler) handler.Handler {
	return handler.NewHandler(&jobCleanupHandler{
		HandlerControls: libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		getJobs: func(ctx context.Context) []*batchv1.Job {
			job := libctrl.NewComponent[*batchv1.Job](r.informers, batchv1.SchemeGroupVersion.WithResource("jobs"), OwningClusterIndex, SelectorForComponent(r.cluster.Name, ComponentMigrationJobLabelValue))
			return job.List(r.cluster.NamespacedName())
		},
		deleteJob: func(ctx context.Context, name string) error {
			return r.kclient.BatchV1().Jobs(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}, "cleanupJob")
}

func (r *SpiceDBClusterHandler) waitForMigrationsHandler(handlers ...handler.Handler) handler.Handler {
	return handler.NewHandler(&waitForMigrationsHandler{
		HandlerControls: libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueAfter(r.requeue),
		),
		nn:                    r.cluster.NamespacedName(),
		recorder:              r.recorder,
		patchStatus:           r.PatchStatus,
		generation:            r.cluster.Generation,
		selfPause:             handlerSelfPauseKey.MustFind(handlers).ContextHandler.(*libctrl.SelfPauseHandler[*v1alpha1.SpiceDBCluster]),
		nextDeploymentHandler: handlerDeploymentKey.MustFind(handlers),
	}, "waitForMigrations")
}

func (r SpiceDBClusterHandler) pauseCluster(next ...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewPauseHandler(
		libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		metadata.PausedControllerSelectorKey,
		r.cluster,
		r.PatchStatus,
		handler.Handlers(next).MustOne(),
	), "pauseCluster")
}

func (r *SpiceDBClusterHandler) selfPauseCluster(...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewSelfPauseHandler(
		libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		metadata.PausedControllerSelectorKey,
		r.cluster,
		r.cluster.UID,
		r.Patch,
		r.PatchStatus,
	), handlerSelfPauseKey)
}

func (r *SpiceDBClusterHandler) secretAdopter(next ...handler.Handler) handler.Handler {
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	return handler.NewHandler(&secretAdopterHandler{
		HandlerControls: libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		nn:              r.cluster.NamespacedName(),
		recorder:        r.recorder,
		secretName:      r.cluster.Spec.SecretRef,
		secretIndexer:   r.informers[secretsGVR].ForResource(secretsGVR).Informer().GetIndexer(),
		secretApplyFunc: r.kclient.CoreV1().Secrets(r.cluster.Namespace).Apply,
		next:            handler.Handlers(next).MustOne(),
	}, "adoptSecret")
}

func (r *SpiceDBClusterHandler) checkConfigChanged(next ...handler.Handler) handler.Handler {
	return handler.NewHandler(&configChangedHandler{
		currentStatus: r.cluster.NewStatusPatch(),
		nn:            r.cluster.NamespacedName(),
		obj:           r.cluster.GetObjectMeta(),
		status:        &r.cluster.Status,
		patchStatus:   r.PatchStatus,
		requeue: func() {
			r.requeue(0)
		},
		next: handler.Handlers(next).MustOne(),
	}, "checkConfigChanged")
}

func (r *SpiceDBClusterHandler) validateConfig(next ...handler.Handler) handler.Handler {
	return handler.NewHandler(&validateConfigHandler{
		HandlerControls: libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		nn:           r.cluster.NamespacedName(),
		uid:          r.cluster.UID,
		rawConfig:    r.cluster.Spec.Config,
		spiceDBImage: r.spiceDBImage,
		generation:   r.cluster.Generation,
		status:       &r.cluster.Status,
		patchStatus:  r.PatchStatus,
		recorder:     r.recorder,
		next:         handler.Handlers(next).MustOne(),
	}, "validateConfig")
}

func (r *SpiceDBClusterHandler) getDeployments(...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewComponentContextHandler[*appsv1.Deployment](
		libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		ctxDeployments,
		libctrl.NewComponent[*appsv1.Deployment](r.informers, appsv1.SchemeGroupVersion.WithResource("deployments"), OwningClusterIndex, SelectorForComponent(r.cluster.Name, ComponentSpiceDBLabelValue)),
		r.cluster.NamespacedName(),
		handler.NoopHandler,
	), "getDeployments")
}

func (r *SpiceDBClusterHandler) getJobs(...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewComponentContextHandler[*batchv1.Job](
		libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		ctxJobs,
		libctrl.NewComponent[*batchv1.Job](r.informers, batchv1.SchemeGroupVersion.WithResource("jobs"), OwningClusterIndex, SelectorForComponent(r.cluster.Name, ComponentMigrationJobLabelValue)),
		r.cluster.NamespacedName(),
		handler.NoopHandler,
	), "getJobs")
}

func (r *SpiceDBClusterHandler) runMigration(next ...handler.Handler) handler.Handler {
	return handler.NewHandler(&migrationRunHandler{
		HandlerControls: libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueAfter(r.requeue),
		),
		nn:        r.cluster.NamespacedName(),
		secretRef: r.cluster.Spec.SecretRef,
		getJobs: func(ctx context.Context) []*batchv1.Job {
			job := libctrl.NewComponent[*batchv1.Job](r.informers, batchv1.SchemeGroupVersion.WithResource("jobs"), OwningClusterIndex, SelectorForComponent(r.cluster.Name, ComponentMigrationJobLabelValue))
			return job.List(r.cluster.NamespacedName())
		},
		applyJob: func(ctx context.Context, job *applybatchv1.JobApplyConfiguration) error {
			_, err := r.kclient.BatchV1().Jobs(r.cluster.Namespace).Apply(ctx, job, forceOwned)
			return err
		},
		deleteJob: func(ctx context.Context, name string) error {
			return r.kclient.BatchV1().Jobs(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		patchStatus: r.PatchStatus,
		next:        handler.Handlers(next).MustOne(),
	}, "createMigrationJob")
}

func (r *SpiceDBClusterHandler) checkMigrations(next ...handler.Handler) handler.Handler {
	return handler.NewHandler(&migrationCheckHandler{
		HandlerControls: libctrl.HandlerControlsWith(
			libctrl.WithDone(r.done),
			libctrl.WithRequeueImmediate(r.requeue),
		),
		recorder:                r.recorder,
		nextMigrationRunHandler: handlerMigrationRunKey.MustFind(next),
		nextWaitForJobHandler:   handlerWaitForMigrationsKey.MustFind(next),
		nextDeploymentHandler:   handlerDeploymentKey.MustFind(next),
	}, "checkMigrations")
}

func newEnsureClusterComponent[K metav1.Object, A libctrl.Annotator[A]](
	r *SpiceDBClusterHandler,
	component *libctrl.Component[K],
	applyObj func(ctx context.Context, apply A) (K, error),
	deleteObject func(ctx context.Context, name string) error,
	newObj func(ctx context.Context) A,
) *libctrl.EnsureComponentByHash[K, A] {
	return libctrl.NewEnsureComponentByHash[K, A](
		libctrl.NewHashableComponent[K](*component, libctrl.NewObjectHash(), "authzed.com/cluster-component-hash"),
		r.cluster.NamespacedName(),
		libctrl.HandlerControlsWith(libctrl.WithDone(r.done), libctrl.WithRequeueImmediate(r.requeue)),
		applyObj,
		deleteObject,
		newObj)
}

func (r *SpiceDBClusterHandler) ensureServiceAccount(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewComponent[*corev1.ServiceAccount](r.informers, corev1.SchemeGroupVersion.WithResource("serviceaccounts"), OwningClusterIndex, SelectorForComponent(r.cluster.Name, ComponentServiceAccountLabel)),
		func(ctx context.Context, apply *applycorev1.ServiceAccountApplyConfiguration) (*corev1.ServiceAccount, error) {
			return r.kclient.CoreV1().ServiceAccounts(r.cluster.Namespace).Apply(ctx, apply, forceOwned)
		},
		func(ctx context.Context, name string) error {
			return r.kclient.CoreV1().ServiceAccounts(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applycorev1.ServiceAccountApplyConfiguration {
			return ctxConfig.MustValue(ctx).serviceAccount()
		},
	), "ensureServiceAccount")
}

func (r *SpiceDBClusterHandler) ensureRole(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewComponent[*rbacv1.Role](r.informers, rbacv1.SchemeGroupVersion.WithResource("roles"), OwningClusterIndex, SelectorForComponent(r.cluster.Name, ComponentRoleLabel)),
		func(ctx context.Context, apply *applyrbacv1.RoleApplyConfiguration) (*rbacv1.Role, error) {
			return r.kclient.RbacV1().Roles(r.cluster.Namespace).Apply(ctx, apply, forceOwned)
		},
		func(ctx context.Context, name string) error {
			return r.kclient.RbacV1().Roles(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applyrbacv1.RoleApplyConfiguration {
			return ctxConfig.MustValue(ctx).role()
		},
	), "ensureRole")
}

func (r *SpiceDBClusterHandler) ensureRoleBinding(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewComponent[*rbacv1.RoleBinding](r.informers, rbacv1.SchemeGroupVersion.WithResource("rolebindings"), OwningClusterIndex, SelectorForComponent(r.cluster.Name, ComponentRoleBindingLabel)),
		func(ctx context.Context, apply *applyrbacv1.RoleBindingApplyConfiguration) (*rbacv1.RoleBinding, error) {
			return r.kclient.RbacV1().RoleBindings(r.cluster.Namespace).Apply(ctx, apply, forceOwned)
		},
		func(ctx context.Context, name string) error {
			return r.kclient.RbacV1().RoleBindings(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applyrbacv1.RoleBindingApplyConfiguration {
			return ctxConfig.MustValue(ctx).roleBinding()
		},
	), "ensureRoleBinding")
}

func (r *SpiceDBClusterHandler) ensureService(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewComponent[*corev1.Service](r.informers, corev1.SchemeGroupVersion.WithResource("services"), OwningClusterIndex, SelectorForComponent(r.cluster.Name, ComponentServiceLabel)),
		func(ctx context.Context, apply *applycorev1.ServiceApplyConfiguration) (*corev1.Service, error) {
			return r.kclient.CoreV1().Services(r.cluster.Namespace).Apply(ctx, apply, forceOwned)
		},
		func(ctx context.Context, name string) error {
			return r.kclient.CoreV1().Services(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applycorev1.ServiceApplyConfiguration {
			return ctxConfig.MustValue(ctx).service()
		},
	), "ensureService")
}

// TODO: generic adoption handler
type secretAdopterHandler struct {
	libctrl.HandlerControls
	nn         types.NamespacedName
	secretName string
	recorder   record.EventRecorder

	// TODO: component
	secretIndexer   cache.Indexer
	secretApplyFunc func(ctx context.Context, secret *applycorev1.SecretApplyConfiguration, opts metav1.ApplyOptions) (result *corev1.Secret, err error)
	next            handler.ContextHandler
}

func (s *secretAdopterHandler) Handle(ctx context.Context) {
	if s.secretName == "" {
		s.next.Handle(ctx)
		return
	}
	secrets, err := s.secretIndexer.ByIndex(OwningClusterIndex, s.nn.String())
	if err != nil {
		s.Requeue()
		return
	}
	var secret *corev1.Secret
	switch len(secrets) {
	case 0:
		// secret is not in cache, which means it's not labelled for the cluster
		// fetch it and add the label to it.
		secret, err = s.secretApplyFunc(ctx, applycorev1.Secret(s.secretName, s.nn.Namespace).WithLabels(map[string]string{
			OwnerLabelKey: s.nn.Name,
		}), forceOwned)
		s.recorder.Eventf(secret, corev1.EventTypeNormal, EventSecretAdoptedBySpiceDBCluster, "Secret was referenced as the secret source for a SpiceDBCluster; it has been labelled to mark it as part of the configuration for that cluster.")
	case 1:
		var ok bool
		secret, ok = secrets[0].(*corev1.Secret)
		if !ok {
			err = fmt.Errorf("non-secret object found in secret informer cache for %s/%s; should not be possible", s.nn.Namespace, s.secretName)
		}
	default:
		err = fmt.Errorf("more than one secret found for %s/%s; should not be possible", s.nn.Namespace, s.secretName)
	}
	if err != nil {
		s.RequeueErr(err)
		return
	}
	secretHash, err := libctrl.SecureHashObject(secret)
	if err != nil {
		s.RequeueErr(err)
		return
	}
	ctx = ctxSecretHash.WithValue(ctx, secretHash)
	ctx = ctxSecret.WithValue(ctx, secret)
	s.next.Handle(ctx)
}

type configChangedHandler struct {
	nn            types.NamespacedName
	currentStatus *v1alpha1.SpiceDBCluster
	obj           metav1.Object
	status        *v1alpha1.ClusterStatus
	requeue       func()
	patchStatus   func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next          handler.ContextHandler
}

func (c *configChangedHandler) Handle(ctx context.Context) {
	secretHash := ctxSecretHash.Value(ctx)
	if c.obj.GetGeneration() != c.status.ObservedGeneration || secretHash != c.status.SecretHash {
		c.currentStatus.Status.ObservedGeneration = c.obj.GetGeneration()
		c.currentStatus.Status.SecretHash = secretHash
		meta.SetStatusCondition(&c.currentStatus.Status.Conditions, v1alpha1.NewValidatingConfigCondition(secretHash))
		if err := c.patchStatus(ctx, c.currentStatus); err != nil {
			c.requeue()
			return
		}
	}
	ctx = ctxClusterStatus.WithValue(ctx, c.currentStatus)
	c.next.Handle(ctx)
}

type validateConfigHandler struct {
	libctrl.HandlerControls
	rawConfig    map[string]string
	spiceDBImage string
	nn           types.NamespacedName
	uid          types.UID
	status       *v1alpha1.ClusterStatus
	generation   int64
	recorder     record.EventRecorder

	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next        handler.ContextHandler
}

func (c *validateConfigHandler) Handle(ctx context.Context) {
	currentStatus := ctxClusterStatus.MustValue(ctx)
	// config is either valid or invalid, remove validating condition
	if condition := meta.FindStatusCondition(c.status.Conditions, v1alpha1.ConditionTypeValidating); condition != nil {
		meta.RemoveStatusCondition(&currentStatus.Status.Conditions, v1alpha1.ConditionTypeValidating)
		if err := c.patchStatus(ctx, currentStatus); err != nil {
			c.Requeue()
			return
		}
	}
	config, err := NewConfig(c.nn, c.uid, c.spiceDBImage, c.rawConfig, ctxSecret.Value(ctx))
	if err != nil {
		meta.SetStatusCondition(&currentStatus.Status.Conditions, v1alpha1.NewInvalidConfigCondition("", err))
		if err := c.patchStatus(ctx, currentStatus); err != nil {
			c.Requeue()
			return
		}
		c.recorder.Eventf(currentStatus, corev1.EventTypeWarning, EventInvalidSpiceDBConfig, "invalid config: %v", err)
		// if the config is invalid, there's no work to do until it has changed
		c.Done()
		return
	}

	ctx = ctxConfig.WithValue(ctx, config)
	ctx = ctxClusterStatus.WithValue(ctx, currentStatus)
	c.next.Handle(ctx)
}

type migrationCheckHandler struct {
	libctrl.HandlerControls
	recorder record.EventRecorder

	nextMigrationRunHandler handler.ContextHandler
	nextWaitForJobHandler   handler.ContextHandler
	nextDeploymentHandler   handler.ContextHandler
}

// TODO: maybe this could be generalized as some sort of "Hash Handoff" flow
func (m *migrationCheckHandler) Handle(ctx context.Context) {
	deployments := ctxDeployments.MustValue(ctx)
	jobs := ctxJobs.MustValue(ctx)

	migrationHash, err := libctrl.SecureHashObject(ctxConfig.MustValue(ctx).MigrationConfig)
	if err != nil {
		m.RequeueErr(err)
		return
	}
	ctx = ctxMigrationHash.WithValue(ctx, migrationHash)

	hasJob := false
	hasDeployment := false
	for _, d := range deployments {
		if d.Annotations != nil && libctrl.SecureHashEqual(d.Annotations[SpiceDBMigrationRequirementsKey], migrationHash) {
			hasDeployment = true
			break
		}
	}
	for _, j := range jobs {
		if j.Annotations != nil && libctrl.SecureHashEqual(j.Annotations[SpiceDBMigrationRequirementsKey], migrationHash) {
			hasJob = true
			ctx = ctxCurrentMigrationJob.WithValue(ctx, j)
			break
		}
	}

	// if there's no job and no (updated) deployment, create the job
	if !hasDeployment && !hasJob {
		m.recorder.Eventf(ctxClusterStatus.MustValue(ctx), corev1.EventTypeNormal, EventRunningMigrations, "Running migration job for %s", ctxConfig.MustValue(ctx).TargetSpiceDBImage)
		m.nextMigrationRunHandler.Handle(ctx)
		return
	}

	// if there's a job but no (updated) deployment, wait for the job
	if hasJob && !hasDeployment {
		m.nextWaitForJobHandler.Handle(ctx)
		return
	}

	// if the deployment is up to date, continue
	m.nextDeploymentHandler.Handle(ctx)
}

// TODO: see if the config hashing can be generalized / unified with the object hashing
type migrationRunHandler struct {
	libctrl.HandlerControls
	nn          types.NamespacedName
	secretRef   string
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	recorder    record.EventBroadcaster

	getJobs   func(ctx context.Context) []*batchv1.Job
	applyJob  func(ctx context.Context, job *applybatchv1.JobApplyConfiguration) error
	deleteJob func(ctx context.Context, name string) error
	next      handler.ContextHandler
}

func (m *migrationRunHandler) Handle(ctx context.Context) {
	currentStatus := ctxClusterStatus.MustValue(ctx)
	config := ctxConfig.MustValue(ctx)
	meta.SetStatusCondition(&currentStatus.Status.Conditions, v1alpha1.NewMigratingCondition(config.DatastoreEngine, "head"))
	if err := m.patchStatus(ctx, currentStatus); err != nil {
		m.RequeueErr(err)
		return
	}
	ctx = ctxClusterStatus.WithValue(ctx, currentStatus)

	jobs := ctxJobs.MustValue(ctx)
	migrationHash := ctxMigrationHash.Value(ctx)

	matchingObjs := make([]*batchv1.Job, 0)
	extraObjs := make([]*batchv1.Job, 0)
	for _, o := range jobs {
		annotations := o.GetAnnotations()
		if annotations == nil {
			extraObjs = append(extraObjs, o)
		}
		if subtle.ConstantTimeCompare([]byte(annotations[SpiceDBMigrationRequirementsKey]), []byte(migrationHash)) == 1 {
			matchingObjs = append(matchingObjs, o)
		} else {
			extraObjs = append(extraObjs, o)
		}
	}

	if len(matchingObjs) == 0 {
		// apply if no matching object in cluster
		err := m.applyJob(ctx, ctxConfig.MustValue(ctx).migrationJob(migrationHash))
		if err != nil {
			m.RequeueErr(err)
			return
		}
	}

	// delete extra objects
	for _, o := range extraObjs {
		if err := m.deleteJob(ctx, o.GetName()); err != nil {
			m.RequeueErr(err)
			return
		}
	}

	// job with correct hash exists
	if len(matchingObjs) > 1 {
		ctx = ctxCurrentMigrationJob.WithValue(ctx, matchingObjs[0])
		m.next.Handle(ctx)
		return
	}

	// if we had to create a job, requeue after a wait since the job takes time
	m.RequeueAfter(5 * time.Second)
}

type waitForMigrationsHandler struct {
	libctrl.HandlerControls
	nn                    types.NamespacedName
	recorder              record.EventRecorder
	generation            int64
	patchStatus           func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	selfPause             *libctrl.SelfPauseHandler[*v1alpha1.SpiceDBCluster]
	nextDeploymentHandler handler.ContextHandler
}

func (m *waitForMigrationsHandler) Handle(ctx context.Context) {
	job := ctxCurrentMigrationJob.MustValue(ctx)
	// if migration failed entirely, pause so we can diagnose
	if c := findJobCondition(job, batchv1.JobFailed); c != nil && c.Status == corev1.ConditionTrue {
		currentStatus := ctxClusterStatus.MustValue(ctx)
		config := ctxConfig.MustValue(ctx)
		err := fmt.Errorf("migration job failed: %s", c.Message)
		utilruntime.HandleError(err)
		meta.SetStatusCondition(&currentStatus.Status.Conditions, v1alpha1.NewMigrationFailedCondition(config.DatastoreEngine, "head", err))
		m.selfPause.Object = currentStatus
		m.selfPause.Handle(ctx)
		return
	}

	// if done, go to the nextDeploymentHandler step
	if jobConditionHasStatus(job, batchv1.JobComplete, corev1.ConditionTrue) {
		m.recorder.Eventf(ctxClusterStatus.MustValue(ctx), corev1.EventTypeNormal, EventMigrationsComplete, "Migrations completed for %s", ctxConfig.MustValue(ctx).TargetSpiceDBImage)
		m.nextDeploymentHandler.Handle(ctx)
		return
	}

	// otherwise, it's created but still running, just wait
	m.RequeueAfter(5 * time.Second)
}

type deploymentHandler struct {
	libctrl.HandlerControls
	nn               types.NamespacedName
	deleteDeployment func(ctx context.Context, name string) error
	applyDeployment  func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error)
	patchStatus      func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next             handler.ContextHandler
}

func (m *deploymentHandler) Handle(ctx context.Context) {
	currentStatus := ctxClusterStatus.MustValue(ctx)
	// remove migrating condition if present
	if meta.IsStatusConditionTrue(currentStatus.Status.Conditions, v1alpha1.ConditionTypeMigrating) {
		meta.RemoveStatusCondition(&currentStatus.Status.Conditions, v1alpha1.ConditionTypeMigrating)
		if err := m.patchStatus(ctx, currentStatus); err != nil {
			m.RequeueErr(err)
			return
		}
		ctx = ctxClusterStatus.WithValue(ctx, currentStatus)
	}

	migrationHash := ctxMigrationHash.Value(ctx)
	config := ctxConfig.MustValue(ctx)
	spiceDBConfigHash, err := libctrl.HashObject(config.SpiceConfig)
	if err != nil {
		m.RequeueErr(err)
		return
	}
	ctx = ctxSpiceConfigHash.WithValue(ctx, spiceDBConfigHash)

	matchingObjs := make([]*appsv1.Deployment, 0)
	extraObjs := make([]*appsv1.Deployment, 0)
	for _, o := range ctxDeployments.MustValue(ctx) {
		annotations := o.GetAnnotations()
		if annotations == nil {
			extraObjs = append(extraObjs, o)
		}
		if libctrl.SecureHashEqual(annotations[SpiceDBMigrationRequirementsKey], migrationHash) &&
			libctrl.HashEqual(annotations[SpiceDBConfigKey], spiceDBConfigHash) {
			matchingObjs = append(matchingObjs, o)
		} else {
			extraObjs = append(extraObjs, o)
		}
	}

	if len(matchingObjs) == 1 {
		ctx = ctxCurrentSpiceDeployment.WithValue(ctx, matchingObjs[0])
	}
	// deployment with correct hash exists
	if len(matchingObjs) == 0 {
		// apply if no matching object in cluster
		deployment, err := m.applyDeployment(ctx, ctxConfig.MustValue(ctx).deployment(migrationHash, spiceDBConfigHash))
		if err != nil {
			m.RequeueErr(err)
			return
		}
		ctx = ctxCurrentSpiceDeployment.WithValue(ctx, deployment)
	}

	// delete extra objects
	for _, o := range extraObjs {
		if err := m.deleteDeployment(ctx, o.GetName()); err != nil {
			m.RequeueErr(err)
			return
		}
	}

	m.next.Handle(ctx)
}

type jobCleanupHandler struct {
	libctrl.HandlerControls
	getJobs   func(ctx context.Context) []*batchv1.Job
	deleteJob func(ctx context.Context, name string) error
}

// cleans up jobs that have completed and match the migration level of the current deployment
// (other jobs will be cleaned up by the previous job sync, this is only concerned with cleaning up "latest" jobs)
func (s *jobCleanupHandler) Handle(ctx context.Context) {
	jobs := s.getJobs(ctx)
	deployment := *ctxCurrentSpiceDeployment.MustValue(ctx)
	if deployment.Annotations == nil || len(jobs) == 0 {
		s.Done()
		return
	}

	for _, j := range jobs {
		if j.Annotations == nil {
			continue
		}
		if libctrl.SecureHashEqual(
			j.Annotations[SpiceDBMigrationRequirementsKey],
			deployment.Annotations[SpiceDBMigrationRequirementsKey]) &&
			jobConditionHasStatus(j, batchv1.JobComplete, corev1.ConditionTrue) {
			if err := s.deleteJob(ctx, j.GetName()); err != nil {
				s.RequeueErr(err)
				return
			}
		}
	}
	s.Done()
}
