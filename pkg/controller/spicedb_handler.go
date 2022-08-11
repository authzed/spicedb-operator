package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlers"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/libctrl/middleware"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

var v1alpha1ClusterGVR = v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.SpiceDBClusterResourceName)

// TODO: wait for a specific RV to be seen, with a timeout

type SpiceDBClusterHandler struct {
	cluster              *v1alpha1.SpiceDBCluster
	client               dynamic.Interface
	kclient              kubernetes.Interface
	informers            map[schema.GroupVersionResource]dynamicinformer.DynamicSharedInformerFactory
	recorder             record.EventRecorder
	defaultSpiceDBImage  string
	allowedSpiceDBImages []string
	allowedSpiceDBTags   []string
}

// Handle inspects the current SpiceDBCluster object and ensures
// the desired state is persisted on the cluster.
func (r *SpiceDBClusterHandler) Handle(ctx context.Context) {
	ctx = handlers.CtxClusterNN.WithValue(ctx, r.cluster.NamespacedName())
	mw := []libctrl.Middleware{
		libctrl.MakeMiddleware(middleware.SyncIDMiddleware),
		libctrl.MakeMiddleware(middleware.KlogMiddleware("spicedbcluster", klog.KObj(r.cluster))),
	}
	chain := libctrl.ChainWithMiddleware(mw...)
	parallel := libctrl.ParallelWithMiddleware(mw...)

	deploymentHandlerChain := chain(
		r.ensureDeployment,
		r.cleanupJob,
	).Handler(handlers.HandlerDeploymentKey)

	waitForMigrationsChain := r.waitForMigrationsHandler(
		deploymentHandlerChain,
		r.selfPauseCluster(handler.NoopHandler),
	).WithID(handlers.HandlerWaitForMigrationsKey)

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
		handlers.CtxDeployments.HandleBuilder("deploymentsPre"),
		handlers.CtxJobs.HandleBuilder("jobsPre"),
		parallel(
			r.getDeployments,
			r.getJobs,
		),
		r.checkMigrations(
			deploymentHandlerChain,
			chain(
				r.runMigration,
				waitForMigrationsChain.Builder(),
			).Handler(handlers.HandlerMigrationRunKey),
			waitForMigrationsChain,
		).Builder(),
	).Handler("controller").Handle(ctx)
}

func (r *SpiceDBClusterHandler) ensureDeployment(next ...handler.Handler) handler.Handler {
	return handlers.NewEnsureDeploymentHandler(
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
	return handlers.NewJobCleanupHandler(
		func(ctx context.Context) []*corev1.Pod {
			pod := libctrl.NewComponent[*corev1.Pod](r.informers, corev1.SchemeGroupVersion.WithResource("pods"), metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentMigrationJobLabelValue))
			return pod.List(r.cluster.NamespacedName())
		},
		func(ctx context.Context) []*batchv1.Job {
			job := libctrl.NewComponent[*batchv1.Job](r.informers, batchv1.SchemeGroupVersion.WithResource("jobs"), metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentMigrationJobLabelValue))
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
	return handlers.NewWaitForMigrationsHandler(r.recorder, next)
}

func (r *SpiceDBClusterHandler) pauseCluster(next ...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewPauseHandler(
		handlers.CtxHandlerControls.ContextKey,
		metadata.PausedControllerSelectorKey,
		handlers.CtxClusterStatus,
		r.PatchStatus,
		handler.Handlers(next).MustOne(),
	), "pauseCluster")
}

func (r *SpiceDBClusterHandler) selfPauseCluster(...handler.Handler) handler.Handler {
	return handlers.NewSelfPauseHandler(r.cluster, r.Patch, r.PatchStatus)
}

func (r *SpiceDBClusterHandler) secretAdopter(next ...handler.Handler) handler.Handler {
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	return handlers.NewSecretAdoptionHandler(
		r.recorder,
		r.cluster.Spec.SecretRef,
		r.informers[secretsGVR].ForResource(secretsGVR).Informer().GetIndexer(),
		r.kclient.CoreV1().Secrets(r.cluster.Namespace).Apply,
		func(ctx context.Context) []*v1alpha1.SpiceDBCluster {
			objs, err := r.informers[v1alpha1ClusterGVR].ForResource(v1alpha1ClusterGVR).Lister().List(labels.Everything())
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("error listing spicedbs: %w", err))
				return nil
			}
			clusters := make([]*v1alpha1.SpiceDBCluster, 0, len(objs))
			for _, obj := range objs {
				u, ok := obj.(*unstructured.Unstructured)
				if !ok {
					utilruntime.HandleError(fmt.Errorf("lister returned invalid object %T", obj))
					return nil
				}

				var cluster v1alpha1.SpiceDBCluster
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &cluster); err != nil {
					utilruntime.HandleError(fmt.Errorf("lister returned invalid object: %w", err))
					return nil
				}

				clusters = append(clusters, &cluster)
			}
			return clusters
		},
		handler.Handlers(next).MustOne(),
	)
}

func (r *SpiceDBClusterHandler) checkConfigChanged(next ...handler.Handler) handler.Handler {
	return handlers.NewConfigChangedHandler(
		r.cluster,
		r.PatchStatus,
		handler.Handlers(next).MustOne(),
	)
}

func (r *SpiceDBClusterHandler) validateConfig(next ...handler.Handler) handler.Handler {
	return handlers.NewValidateConfigHandler(
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
		handlers.CtxDeployments,
		libctrl.NewComponent[*appsv1.Deployment](r.informers, appsv1.SchemeGroupVersion.WithResource("deployments"), metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentSpiceDBLabelValue)),
		r.cluster.NamespacedName(),
		handler.NoopHandler,
	), "getDeployments")
}

func (r *SpiceDBClusterHandler) getJobs(...handler.Handler) handler.Handler {
	return handler.NewHandler(libctrl.NewComponentContextHandler[*batchv1.Job](
		handlers.CtxJobs,
		libctrl.NewComponent[*batchv1.Job](r.informers, batchv1.SchemeGroupVersion.WithResource("jobs"), metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentMigrationJobLabelValue)),
		r.cluster.NamespacedName(),
		handler.NoopHandler,
	), "getJobs")
}

func (r *SpiceDBClusterHandler) runMigration(next ...handler.Handler) handler.Handler {
	return handlers.NewMigrationRunHandler(
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
	return handlers.NewMigrationCheckHandler(r.recorder, next)
}

func newEnsureClusterComponent[K metav1.Object, A libctrl.Annotator[A]](
	r *SpiceDBClusterHandler,
	component *libctrl.Component[K],
	applyObj func(ctx context.Context, apply A) (K, error),
	deleteObject func(ctx context.Context, name string) error,
	newObj func(ctx context.Context) A,
) *libctrl.EnsureComponentByHash[K, A] {
	return libctrl.NewEnsureComponentByHash[K, A](
		libctrl.NewHashableComponent[K](*component, libctrl.NewObjectHash(), "authzed.com/controller-component-hash"),
		r.cluster.NamespacedName(),
		handlers.CtxHandlerControls.ContextKey,
		applyObj,
		deleteObject,
		newObj)
}

func (r *SpiceDBClusterHandler) ensureServiceAccount(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewComponent[*corev1.ServiceAccount](r.informers, corev1.SchemeGroupVersion.WithResource("serviceaccounts"), metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentServiceAccountLabel)),
		func(ctx context.Context, apply *applycorev1.ServiceAccountApplyConfiguration) (*corev1.ServiceAccount, error) {
			klog.V(4).InfoS("applying serviceaccount", "namespace", *apply.Namespace, "name", *apply.Name)
			return r.kclient.CoreV1().ServiceAccounts(r.cluster.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting serviceaccount", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.CoreV1().ServiceAccounts(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applycorev1.ServiceAccountApplyConfiguration {
			return handlers.CtxConfig.MustValue(ctx).ServiceAccount()
		},
	), "ensureServiceAccount")
}

func (r *SpiceDBClusterHandler) ensureRole(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewComponent[*rbacv1.Role](r.informers, rbacv1.SchemeGroupVersion.WithResource("roles"), metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentRoleLabel)),
		func(ctx context.Context, apply *applyrbacv1.RoleApplyConfiguration) (*rbacv1.Role, error) {
			klog.V(4).InfoS("applying role", "namespace", *apply.Namespace, "name", *apply.Name)
			return r.kclient.RbacV1().Roles(r.cluster.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting role", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.RbacV1().Roles(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applyrbacv1.RoleApplyConfiguration {
			return handlers.CtxConfig.MustValue(ctx).Role()
		},
	), "ensureRole")
}

func (r *SpiceDBClusterHandler) ensureRoleBinding(next ...handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		newEnsureClusterComponent(r,
			libctrl.NewComponent[*rbacv1.RoleBinding](r.informers, rbacv1.SchemeGroupVersion.WithResource("rolebindings"), metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentRoleBindingLabel)),
			func(ctx context.Context, apply *applyrbacv1.RoleBindingApplyConfiguration) (*rbacv1.RoleBinding, error) {
				klog.V(4).InfoS("applying rolebinding", "namespace", *apply.Namespace, "name", *apply.Name)
				return r.kclient.RbacV1().RoleBindings(r.cluster.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
			},
			func(ctx context.Context, name string) error {
				klog.V(4).InfoS("deleting rolebinding", "namespace", r.cluster.Namespace, "name", name)
				return r.kclient.RbacV1().RoleBindings(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
			},
			func(ctx context.Context) *applyrbacv1.RoleBindingApplyConfiguration {
				return handlers.CtxConfig.MustValue(ctx).RoleBinding()
			},
		).Handle(ctx)
		handler.Handlers(next).MustOne().Handle(ctx)
	}, "ensureRoleBinding")
}

func (r *SpiceDBClusterHandler) ensureService(...handler.Handler) handler.Handler {
	return handler.NewHandler(newEnsureClusterComponent(r,
		libctrl.NewComponent[*corev1.Service](r.informers, corev1.SchemeGroupVersion.WithResource("services"), metadata.OwningClusterIndex, metadata.SelectorForComponent(r.cluster.Name, metadata.ComponentServiceLabel)),
		func(ctx context.Context, apply *applycorev1.ServiceApplyConfiguration) (*corev1.Service, error) {
			klog.V(4).InfoS("applying service", "namespace", *apply.Namespace, "name", *apply.Name)
			return r.kclient.CoreV1().Services(r.cluster.Namespace).Apply(ctx, apply, metadata.ApplyForceOwned)
		},
		func(ctx context.Context, name string) error {
			klog.V(4).InfoS("deleting service", "namespace", r.cluster.Namespace, "name", name)
			return r.kclient.CoreV1().Services(r.cluster.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
		func(ctx context.Context) *applycorev1.ServiceApplyConfiguration {
			return handlers.CtxConfig.MustValue(ctx).Service()
		},
	), "ensureService")
}
