package cluster

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
)

var (
	ctxSyncID                 = libctrl.NewContextKey[string]()
	ctxSecret                 = libctrl.NewContextDefaultingKey[*corev1.Secret](nil)
	ctxSecretHash             = libctrl.NewContextDefaultingKey[string]("")
	ctxClusterStatus          = libctrl.NewContextDefaultingKey[*v1alpha1.SpiceDBCluster](nil)
	ctxConfig                 = libctrl.NewContextDefaultingKey[*Config](nil)
	ctxMigrationHash          = libctrl.NewContextDefaultingKey[string]("")
	ctxDeployments            = libctrl.NewContextHandleDefaultingKey[[]*appsv1.Deployment](make([]*appsv1.Deployment, 0))
	ctxJobs                   = libctrl.NewContextHandleDefaultingKey[[]*batchv1.Job](make([]*batchv1.Job, 0))
	ctxCurrentMigrationJob    = libctrl.NewContextDefaultingKey[*batchv1.Job](nil)
	ctxCurrentSpiceDeployment = libctrl.NewContextDefaultingKey[*appsv1.Deployment](nil)
)
