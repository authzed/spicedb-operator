package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"

	"github.com/authzed/controller-idioms/handler"
	"github.com/authzed/controller-idioms/hash"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

type DeploymentHandler struct {
	applyDeployment   func(ctx context.Context, dep *applyappsv1.DeploymentApplyConfiguration) (*appsv1.Deployment, error)
	deleteDeployment  func(ctx context.Context, nn types.NamespacedName) error
	getDeploymentPods func(ctx context.Context) []*corev1.Pod
	patchStatus       func(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error
	next              handler.ContextHandler
}

func (m *DeploymentHandler) Handle(ctx context.Context) {
	// TODO: unconditional status change can be a separate handler
	currentStatus := CtxCluster.MustValue(ctx)
	// remove migrating condition if present and set the current migration hash
	if currentStatus.IsStatusConditionTrue(v1alpha1.ConditionTypeMigrating) ||
		currentStatus.Status.CurrentMigrationHash != currentStatus.Status.TargetMigrationHash {
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeMigrating)
		currentStatus.Status.CurrentMigrationHash = currentStatus.Status.TargetMigrationHash
		currentStatus.SetStatusCondition(v1alpha1.NewRollingCondition("Rolling deployment to latest version"))
		if err := m.patchStatus(ctx, currentStatus); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
		ctx = CtxCluster.WithValue(ctx, currentStatus)
	}

	migrationHash := CtxMigrationHash.MustValue(ctx)
	secretHash := CtxSecretHash.MustValue(ctx)
	config := CtxConfig.MustValue(ctx)
	newDeployment := config.Deployment(migrationHash, secretHash)
	deploymentHash := hash.Object(newDeployment)

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

	var cachedDeployment *appsv1.Deployment
	// deployment with correct hash exists
	if len(matchingObjs) == 1 {
		cachedDeployment = matchingObjs[0]
		ctx = CtxCurrentSpiceDeployment.WithValue(ctx, cachedDeployment)

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

	// if the deployment isn't in the cache yet, wait until another event
	// comes in for it
	if cachedDeployment == nil {
		QueueOps.RequeueAfter(ctx, time.Second)
		return
	}

	// check if any pods have errors
	if cachedDeployment.Status.ReadyReplicas != config.Replicas {
		// sort pods by newest first
		pods := m.getDeploymentPods(ctx)
		sort.Slice(pods, func(i, j int) bool {
			return pods[i].CreationTimestamp.Before(&pods[j].CreationTimestamp)
		})
		for _, p := range m.getDeploymentPods(ctx) {
			for _, s := range p.Status.ContainerStatuses {
				if s.LastTerminationState.Terminated != nil {
					currentStatus.SetStatusCondition(v1alpha1.NewPodErrorCondition(s.LastTerminationState.Terminated.Message))
					if err := m.patchStatus(ctx, currentStatus); err != nil {
						QueueOps.RequeueAPIErr(ctx, err)
						return
					}
					QueueOps.RequeueAfter(ctx, 2*time.Second)
					return
				}
			}
		}
	}

	// wait for deployment to be available
	if cachedDeployment.Status.AvailableReplicas != config.Replicas ||
		cachedDeployment.Status.ReadyReplicas != config.Replicas ||
		cachedDeployment.Status.UpdatedReplicas != config.Replicas ||
		cachedDeployment.Status.ObservedGeneration != cachedDeployment.Generation {
		currentStatus.SetStatusCondition(v1alpha1.NewRollingCondition(
			fmt.Sprintf("Waiting for deployment to be available: %d/%d available, %d/%d ready, %d/%d updated, %d/%d generation.",
				cachedDeployment.Status.AvailableReplicas, config.Replicas,
				cachedDeployment.Status.ReadyReplicas, config.Replicas,
				cachedDeployment.Status.UpdatedReplicas, config.Replicas,
				cachedDeployment.Status.ObservedGeneration, cachedDeployment.Generation,
			)))
		if err := m.patchStatus(ctx, currentStatus); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
		QueueOps.RequeueAfter(ctx, 2*time.Second)
		return
	}

	// deployment is finished rolling out, remove condition
	if currentStatus.IsStatusConditionTrue(v1alpha1.ConditionTypeRolling) ||
		currentStatus.IsStatusConditionTrue(v1alpha1.ConditionTypeRolloutError) {
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeRolling)
		currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeRolloutError)
		if err := m.patchStatus(ctx, currentStatus); err != nil {
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}
	}

	m.next.Handle(ctx)
}
