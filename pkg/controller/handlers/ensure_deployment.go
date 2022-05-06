package handlers

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/controller/handlercontext"
	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

type DeploymentHandler struct {
	libctrl.ControlRequeueErr
	applyDeployment  func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error)
	deleteDeployment func(ctx context.Context, name string) error
	patchStatus      func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next             handler.ContextHandler
}

func NewEnsureDeploymentHandler(ctrls libctrl.HandlerControls,
	applyDeployment func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error),
	deleteDeployment func(ctx context.Context, name string) error,
	patchStatus func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error,
	next handler.ContextHandler,
) handler.Handler {
	return handler.NewHandler(&DeploymentHandler{
		ControlRequeueErr: ctrls,
		deleteDeployment:  deleteDeployment,
		applyDeployment:   applyDeployment,
		patchStatus:       patchStatus,
		next:              next,
	}, "ensureDeployment")
}

func (m *DeploymentHandler) Handle(ctx context.Context) {
	// TODO: unconditional status change can be a separate handler
	currentStatus := handlercontext.CtxClusterStatus.MustValue(ctx)
	// remove migrating condition if present
	if currentStatus.IsStatusConditionTrue(v1alpha1.ConditionTypeMigrating) {
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeMigrating)
		if err := m.patchStatus(ctx, currentStatus); err != nil {
			m.RequeueErr(err)
			return
		}
		ctx = handlercontext.CtxClusterStatus.WithValue(ctx, currentStatus)
	}

	migrationHash := handlercontext.CtxMigrationHash.MustValue(ctx)
	config := handlercontext.CtxConfig.MustValue(ctx)
	newDeployment := config.Deployment(migrationHash)
	deploymentHash, err := libctrl.HashObject(newDeployment)
	if err != nil {
		m.RequeueErr(err)
		return
	}

	matchingObjs := make([]*appsv1.Deployment, 0)
	extraObjs := make([]*appsv1.Deployment, 0)
	for _, o := range handlercontext.CtxDeployments.MustValue(ctx) {
		annotations := o.GetAnnotations()
		if annotations == nil {
			extraObjs = append(extraObjs, o)
		}
		if libctrl.HashEqual(annotations[metadata.SpiceDBConfigKey], deploymentHash) {
			matchingObjs = append(matchingObjs, o)
		} else {
			extraObjs = append(extraObjs, o)
		}
	}

	// deployment with correct hash exists
	if len(matchingObjs) == 1 {
		ctx = handlercontext.CtxCurrentSpiceDeployment.WithValue(ctx, matchingObjs[0])

		// delete extra objects
		for _, o := range extraObjs {
			if err := m.deleteDeployment(ctx, o.GetName()); err != nil {
				m.RequeueErr(err)
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
			m.RequeueErr(err)
			return
		}
		ctx = handlercontext.CtxCurrentSpiceDeployment.WithValue(ctx, deployment)
	}

	m.next.Handle(ctx)
}
