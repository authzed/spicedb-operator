package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/libctrl/middleware"
	"github.com/authzed/spicedb-operator/pkg/libctrl/typed"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

var v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)

// TODO: wait for a specific RV to be seen, with a timeout

type SpiceDBClusterHandler struct {
	registry             *typed.Registry
	cluster              *v1alpha1.SpiceDBCluster
	client               dynamic.Interface
	kclient              kubernetes.Interface
	recorder             record.EventRecorder
	defaultSpiceDBImage  string
	allowedSpiceDBImages []string
	allowedSpiceDBTags   []string
}

// Handle inspects the current SpiceDBCluster object and ensures
// the desired state is persisted on the cluster.
func (r *SpiceDBClusterHandler) Handle(ctx context.Context) {
	ctx = CtxClusterNN.WithValue(ctx, r.cluster.NamespacedName())
	mw := middleware.NewHandlerLoggingMiddleware(4)
	chain := libctrl.ChainWithMiddleware(mw)
	parallel := libctrl.ParallelWithMiddleware(mw)

	deploymentHandlerChain := chain(
		r.ensureDeployment,
		r.cleanupJob,
	).Handler(HandlerDeploymentKey)

	waitForMigrationsChain := r.waitForMigrationsHandler(
		deploymentHandlerChain,
		r.selfPauseCluster(handler.NoopHandler),
	).WithID(HandlerWaitForMigrationsKey)

	ctx = CtxSecretNN.WithValue(ctx, types.NamespacedName{
		Name:      r.cluster.Spec.SecretRef,
		Namespace: r.cluster.Namespace,
	})

	chain(
		r.pauseCluster,
		r.secretAdopter,
		r.checkConfigChanged,
		r.validateConfig,
		parallel(
			r.ensureServiceAccount,
			r.ensureRole,
			r.ensureService,
		),
		r.ensureRoleBinding,
		CtxDeployments.HandleBuilder("deploymentsPre"),
		CtxJobs.HandleBuilder("jobsPre"),
		parallel(
			r.getDeployments,
			r.getJobs,
		),
		r.checkMigrations(
			deploymentHandlerChain,
			chain(
				r.runMigration,
				waitForMigrationsChain.Builder(),
			).Handler(HandlerMigrationRunKey),
			waitForMigrationsChain,
		).Builder(),
	).Handler("controller").Handle(ctx)
}

func (r *SpiceDBClusterHandler) ensureDeployment(next ...handler.Handler) handler.Handler {
	return NewEnsureDeploymentHandler(
		func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error) {
			klog.V(4).InfoS("updating deployment", "namespace", *dep.Namespace, "name", *dep.Name)
			return r.kclient.AppsV1().Deployments(r.cluster.Namespace).Apply(ctx, dep, metadata.ApplyForceOwned)
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting deployment", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.AppsV1().Deployments(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		r.PatchStatus,
		handler.Handlers(next).MustOne(),
	)
}

func (r *SpiceDBClusterHandler) cleanupJob(...handler.Handler) handler.Handler {
	return NewJobCleanupHandler(
		func(ctx context.Context) []*corev1.Pod {
			return libctrl.NewIndexedComponent[*corev1.Pod](
				typed.IndexerFor[*corev1.Pod](r.registry, typed.NewRegistryKey(DependentFactoryKey, corev1.SchemeGroupVersion.WithResource("pods"))),
				metadata.OwningClusterIndex,
				metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentMigrationJobLabelValue),
			).List(r.cluster.NamespacedName())
		},
		func(ctx context.Context) []*batchv1.Job {
			job := libctrl.NewIndexedComponent[*batchv1.Job](
				typed.IndexerFor[*batchv1.Job](r.registry, typed.NewRegistryKey(DependentFactoryKey, batchv1.SchemeGroupVersion.WithResource("jobs"))),
				metadata.OwningClusterIndex,
				metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentMigrationJobLabelValue))
			return job.List(r.cluster.NamespacedName())
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting job", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.BatchV1().Jobs(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting job pod", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.CoreV1().Pods(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		})
}

func (r *SpiceDBClusterHandler) waitForMigrationsHandler(next ...handler.Handler) handler.Handler {
	return NewWaitForMigrationsHandler(r.recorder, next)
}

func (r *SpiceDBClusterHandler) pauseCluster(next ...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewPauseHandler(
		CtxHandlerControls.ContextKey,
		metadata.PausedControllerSelectorKey,
		CtxCluster,
		r.PatchStatus,
		handler.Handlers(next).MustOne(),
	), "pauseCluster")
}

func (r *SpiceDBClusterHandler) selfPauseCluster(...handler.Handler) handler.Handler {
	return NewSelfPauseHandler(r.cluster, r.Patch, r.PatchStatus)
}

func (r *SpiceDBClusterHandler) secretAdopter(next ...handler.Handler) handler.Handler {
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	return NewSecretAdoptionHandler(
		r.recorder,
		func(ctx context.Context) (*corev1.Secret, error) {
			return typed.ListerFor[*corev1.Secret](r.registry, typed.NewRegistryKey(DependentFactoryKey, secretsGVR)).ByNamespace(CtxSecretNN.MustValue(ctx).Namespace).Get(CtxSecretNN.MustValue(ctx).Name)
		},
		typed.IndexerFor[*corev1.Secret](r.registry, typed.NewRegistryKey(DependentFactoryKey, secretsGVR)),
		r.kclient.CoreV1().Secrets(r.cluster.Namespace).Apply,
		handler.Handlers(next).MustOne(),
	)
}

func (r *SpiceDBClusterHandler) checkConfigChanged(next ...handler.Handler) handler.Handler {
	return NewConfigChangedHandler(
		r.cluster,
		r.PatchStatus,
		handler.Handlers(next).MustOne(),
	)
}

func (r *SpiceDBClusterHandler) validateConfig(next ...handler.Handler) handler.Handler {
	return NewValidateConfigHandler(
		r.cluster.UID,
		r.cluster.Spec.Config,
		r.defaultSpiceDBImage,
		r.allowedSpiceDBImages,
		r.allowedSpiceDBTags,
		r.cluster.Generation,
		r.PatchStatus,
		r.recorder,
		handler.Handlers(next).MustOne(),
	)
}

func (r *SpiceDBClusterHandler) getDeployments(...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewComponentContextHandler[*appsv1.Deployment](
		CtxDeployments,
		libctrl.NewIndexedComponent[*appsv1.Deployment](
			typed.IndexerFor[*appsv1.Deployment](r.registry, typed.NewRegistryKey(DependentFactoryKey, appsv1.SchemeGroupVersion.WithResource("deployments"))),
			metadata.OwningClusterIndex,
			metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentSpiceDBLabelValue)),
		r.cluster.NamespacedName(),
		handler.NoopHandler,
	), "getDeployments")
}

func (r *SpiceDBClusterHandler) getJobs(...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewComponentContextHandler[*batchv1.Job](
		CtxJobs,
		libctrl.NewIndexedComponent[*batchv1.Job](
			typed.IndexerFor[*batchv1.Job](r.registry, typed.NewRegistryKey(DependentFactoryKey, batchv1.SchemeGroupVersion.WithResource("jobs"))),
			metadata.OwningClusterIndex,
			metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentMigrationJobLabelValue)),
		r.cluster.NamespacedName(),
		handler.NoopHandler,
	), "getJobs")
}

func (r *SpiceDBClusterHandler) runMigration(next ...handler.Handler) handler.Handler {
	return NewMigrationRunHandler(
		r.PatchStatus,
		func(ctx context.Context, job *applybatchv1.JobApplyConfiguration) error {
			_, err := r.kclient.BatchV1().Jobs(r.cluster.Namespace).Apply(ctx, job, metadata.ApplyForceOwned)
			return err
		},
		func(ctx context.Context, name string) error {
			return r.kclient.BatchV1().Jobs(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		handler.Handlers(next).MustOne(),
	)
}

func (r *SpiceDBClusterHandler) checkMigrations(next ...handler.Handler) handler.Handler {
	return NewMigrationCheckHandler(r.recorder, next)
}

func newEnsureClusterComponent[K libctrl.KubeObject, A libctrl.Annotator[A]](
	r *SpiceDBClusterHandler,
	component *libctrl.Component[K],
	applyObj func(ctx context.Context, apply A) (K, error),
	deleteObject func(ctx context.Context, name string) error,
	newObj func(ctx context.Context) A,
) *libctrl.EnsureComponentByHash[K, A] {
	return libctrl.NewEnsureComponentByHash[K, A](
		libctrl.NewHashableComponent[K](*component, libctrl.NewObjectHash(), "authzed.com/controller-component-hash"),
		r.cluster.NamespacedName(),
		CtxHandlerControls.ContextKey,
		applyObj,
		deleteObject,
		newObj)
}

func (r *SpiceDBClusterHandler) ensureServiceAccount(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewIndexedComponent[*corev1.ServiceAccount](
			typed.IndexerFor[*corev1.ServiceAccount](
				r.registry,
				typed.NewRegistryKey(DependentFactoryKey, corev1.SchemeGroupVersion.WithResource("serviceaccounts"))),
			metadata.OwningClusterIndex,
			metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentServiceAccountLabel)),
		func(ctx context.Context, apply *applycorev1.ServiceAccountApplyConfiguration) (*corev1.ServiceAccount, error) {
			klog.V(4).InfoS("applying serviceaccount", "namespace", *apply.Namespace, "name", *apply.Name)
			return r.kclient.CoreV1().ServiceAccounts(r.cluster.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting serviceaccount", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.CoreV1().ServiceAccounts(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applycorev1.ServiceAccountApplyConfiguration {
			return CtxConfig.MustValue(ctx).ServiceAccount()
		},
	), "ensureServiceAccount")
}

func (r *SpiceDBClusterHandler) ensureRole(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewIndexedComponent[*rbacv1.Role](
			typed.IndexerFor[*rbacv1.Role](
				r.registry,
				typed.NewRegistryKey(DependentFactoryKey, rbacv1.SchemeGroupVersion.WithResource("roles"))),
			metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentRoleLabel)),
		func(ctx context.Context, apply *applyrbacv1.RoleApplyConfiguration) (*rbacv1.Role, error) {
			klog.V(4).InfoS("applying role", "namespace", *apply.Namespace, "name", *apply.Name)
			return r.kclient.RbacV1().Roles(r.cluster.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting role", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.RbacV1().Roles(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applyrbacv1.RoleApplyConfiguration {
			return CtxConfig.MustValue(ctx).Role()
		},
	), "ensureRole")
}

func (r *SpiceDBClusterHandler) ensureRoleBinding(next ...handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		newEnsureClusterComponent(r,
			libctrl.NewIndexedComponent[*rbacv1.RoleBinding](
				typed.IndexerFor[*rbacv1.RoleBinding](
					r.registry,
					typed.NewRegistryKey(DependentFactoryKey, rbacv1.SchemeGroupVersion.WithResource("rolebindings"))),
				metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentRoleBindingLabel)),
			func(ctx context.Context, apply *applyrbacv1.RoleBindingApplyConfiguration) (*rbacv1.RoleBinding, error) {
				klog.V(4).InfoS("applying rolebinding", "namespace", *apply.Namespace, "name", *apply.Name)
				return r.kclient.RbacV1().RoleBindings(r.cluster.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
			},
			func(ctx context.Context, name string) error {
				klog.V(4).InfoS("deleting rolebinding", "namespace", r.cluster.Namespace, "name", name)
				return r.kclient.RbacV1().RoleBindings(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
			},
			func(ctx context.Context) *applyrbacv1.RoleBindingApplyConfiguration {
				return CtxConfig.MustValue(ctx).RoleBinding()
			},
		).Handle(ctx)
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "ensureRoleBinding")
}

func (r *SpiceDBClusterHandler) ensureService(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewIndexedComponent[*corev1.Service](
			typed.IndexerFor[*corev1.Service](
				r.registry,
				typed.NewRegistryKey(DependentFactoryKey, corev1.SchemeGroupVersion.WithResource("services"))),
			metadata.OwningClusterIndex,
			metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentServiceLabel)),
		func(ctx context.Context, apply *applycorev1.ServiceApplyConfiguration) (*corev1.Service, error) {
			klog.V(4).InfoS("applying service", "namespace", *apply.Namespace, "name", *apply.Name)
			return r.kclient.CoreV1().Services(r.cluster.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting service", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.CoreV1().Services(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applycorev1.ServiceApplyConfiguration {
			return CtxConfig.MustValue(ctx).Service()
		},
	), "ensureService")
}
