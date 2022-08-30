package controller

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/authzed/controller-idioms/queue"
	"github.com/authzed/controller-idioms/typedctx"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
)

var (
	QueueOps                  = queue.NewQueueOperationsCtx()
	CtxOperatorConfig         = typedctx.WithDefault[*OperatorConfig](nil)
	CtxClusterNN              = typedctx.WithDefault[types.NamespacedName](types.NamespacedName{})
	CtxSecretNN               = typedctx.WithDefault[types.NamespacedName](types.NamespacedName{})
	CtxSecret                 = typedctx.WithDefault[*corev1.Secret](nil)
	CtxSecretHash             = typedctx.WithDefault[string]("")
	CtxCluster                = typedctx.WithDefault[*v1alpha1.SpiceDBCluster](nil)
	CtxClusterStatus          = typedctx.WithDefault[*v1alpha1.SpiceDBCluster](nil)
	CtxConfig                 = typedctx.WithDefault[*config.Config](nil)
	CtxMigrationHash          = typedctx.WithDefault[string]("")
	CtxDeployments            = typedctx.Boxed[[]*appsv1.Deployment](make([]*appsv1.Deployment, 0))
	CtxJobs                   = typedctx.Boxed[[]*batchv1.Job](make([]*batchv1.Job, 0))
	CtxCurrentMigrationJob    = typedctx.WithDefault[*batchv1.Job](nil)
	CtxCurrentSpiceDeployment = typedctx.WithDefault[*appsv1.Deployment](nil)
	CtxSelfPauseObject        = typedctx.WithDefault[*v1alpha1.SpiceDBCluster](new(v1alpha1.SpiceDBCluster))
)
