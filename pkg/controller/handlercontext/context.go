package handlercontext

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
)

var (
	CtxClusterNN              = libctrl.NewContextDefaultingKey[types.NamespacedName](types.NamespacedName{})
	CtxSecret                 = libctrl.NewContextDefaultingKey[*corev1.Secret](nil)
	CtxSecretHash             = libctrl.NewContextDefaultingKey[string]("")
	CtxClusterStatus          = libctrl.NewContextDefaultingKey[*v1alpha1.SpiceDBCluster](nil)
	CtxConfig                 = libctrl.NewContextDefaultingKey[*config.Config](nil)
	CtxMigrationHash          = libctrl.NewContextDefaultingKey[string]("")
	CtxDeployments            = libctrl.NewContextHandleDefaultingKey[[]*appsv1.Deployment](make([]*appsv1.Deployment, 0))
	CtxJobs                   = libctrl.NewContextHandleDefaultingKey[[]*batchv1.Job](make([]*batchv1.Job, 0))
	CtxCurrentMigrationJob    = libctrl.NewContextDefaultingKey[*batchv1.Job](nil)
	CtxCurrentSpiceDeployment = libctrl.NewContextDefaultingKey[*appsv1.Deployment](nil)
	CtxSelfPauseObject        = libctrl.NewContextDefaultingKey[*v1alpha1.SpiceDBCluster](new(v1alpha1.SpiceDBCluster))
)
