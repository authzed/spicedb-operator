package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

type DeploymentHandler struct {
	applyDeployment  func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error)
	deleteDeployment func(ctx context.Context, nn types.NamespacedName) error
	patchStatus      func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next             handler.ContextHandler
}

func (m *DeploymentHandler) Handle(ctx context.Context) {
	// TODO: unconditional status change can be a separate handler
	currentStatus := CtxClusterStatus.MustValue(ctx)
	// remove migrating condition if present and set the current migration hash
	if currentStatus.IsStatusConditionTrue(v1alpha1.ConditionTypeMigrating) ||
		currentStatus.Status.CurrentMigrationHash != currentStatus.Status.TargetMigrationHash {
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeMigrating)
		currentStatus.Status.CurrentMigrationHash = currentStatus.Status.TargetMigrationHash
		if err := m.patchStatus(ctx, currentStatus); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
		ctx = CtxClusterStatus.WithValue(ctx, currentStatus)
	}

	migrationHash := CtxMigrationHash.MustValue(ctx)
	secretHash := CtxSecretHash.MustValue(ctx)
	config := CtxConfig.MustValue(ctx)
	newDeployment := config.Deployment(migrationHash, secretHash)
	deploymentHash, err := hash.Object(newDeployment)
	if err != nil {
		QueueOps.RequeueErr(ctx, err)
		return
	}

	matchingObjs := make([]*appsv1.Deployment, 0)
	extraObjs := make([]*appsv1.Deployment, 0)
	for _, o := range CtxDeployments.MustValue(ctx) {
		annotations := o.GetAnnotations()
		if annotations == nil {
			extraObjs = append(extraObjs, o)
		}
		if hash.Equal(annotations[metadata.SpiceDBConfigKey], deploymentHash) {
			matchingObjs = append(matchingObjs, o)
		} else {
			extraObjs = append(extraObjs, o)
		}
	}

	// deployment with correct hash exists
	if len(matchingObjs) == 1 {
		ctx = CtxCurrentSpiceDeployment.WithValue(ctx, matchingObjs[0])

		// delete extra objects
		for _, o := range extraObjs {
			if err := m.deleteDeployment(ctx, types.NamespacedName{Namespace: currentStatus.Namespace, Name: o.GetName()}); err != nil {
				QueueOps.RequeueAPIErr(ctx, err)
				return
			}
		}
	}

	// apply if no matching object in controller
	if len(matchingObjs) == 0 {
		deployment, err := m.applyDeployment(ctx,
			newDeployment.WithAnnotations(
				map[string]string{metadata.SpiceDBConfigKey: deploymentHash},
			),
		)
		if err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
		ctx = CtxCurrentSpiceDeployment.WithValue(ctx, deployment)
	}

	m.next.Handle(ctx)
}
