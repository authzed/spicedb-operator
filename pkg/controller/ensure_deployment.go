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
	if currentStatus.FindStatusCondition(v1alpha1.ConditionTypeMigrating) != nil ||
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
	// secretHash may be empty when all credentials are configured with `skip: true`,
	// since there is nothing to hash in that case. Use Value (not MustValue) so we
	// fall back to the zero value rather than panic.
	secretHash := CtxSecretHash.Value(ctx)
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
			// surface the failure on the status so it's visible without
			// reading operator logs, e.g. when a user-provided patch results
			// in a deployment the API server rejects
			currentStatus.SetStatusCondition(v1alpha1.NewApplyFailedCondition(err))
			if patchErr := m.patchStatus(ctx, currentStatus); patchErr != nil {
				QueueOps.RequeueAPIErr(ctx, patchErr)
				return
			}
			QueueOps.RequeueAPIErr(ctx, err)
			return
		}

		// clear any error from a previous failed apply
		if cond := currentStatus.FindStatusCondition(v1alpha1.ConditionTypeRolloutError); cond != nil && cond.Reason == v1alpha1.ConditionReasonApplyFailed {
			currentStatus.RemoveStatusCondition(v1alpha1.ConditionTypeRolloutError)
			if err := m.patchStatus(ctx, currentStatus); err != nil {
				QueueOps.RequeueAPIErr(ctx, err)
				return
			}
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
	if cachedDeployment.Status.UnavailableReplicas > 0 {
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

	// wait for deployment to be available. Measure against the Deployment's own
	// spec.replicas rather than config.replicas: when replicas are managed
	// externally (an autoscaler scaling the Deployment after a spec.patches
	// entry removes /spec/replicas) the operator no longer sets the field, and
	// comparing to config.replicas would report a rollout that never completes.
	desiredReplicas := desiredDeploymentReplicas(cachedDeployment, config.Replicas)
	if !deploymentRolloutComplete(cachedDeployment, desiredReplicas) {
		currentStatus.SetStatusCondition(v1alpha1.NewRollingCondition(
			fmt.Sprintf("Waiting for deployment to be available: %d/%d available, %d/%d ready, %d/%d updated, %d/%d generation.",
				cachedDeployment.Status.AvailableReplicas, desiredReplicas,
				cachedDeployment.Status.ReadyReplicas, desiredReplicas,
				cachedDeployment.Status.UpdatedReplicas, desiredReplicas,
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

// desiredDeploymentReplicas returns the replica count dep is being driven to.
// It is the live Deployment's spec.replicas when set: the operator omits that
// field from its server-side apply when replicas are managed externally (for
// example an autoscaler scaling the Deployment after a spec.patches entry
// removes /spec/replicas), so the live value, rather than the operator's
// configured count, is what a rollout must be measured against. Falls back to
// configured when the live field is unset.
func desiredDeploymentReplicas(dep *appsv1.Deployment, configured int32) int32 {
	if dep.Spec.Replicas != nil {
		return *dep.Spec.Replicas
	}
	return configured
}

// deploymentRolloutComplete reports whether dep has finished rolling out to
// desired replicas, using the same signals as `kubectl rollout status`: the
// latest generation has been observed, every desired replica has been updated
// to the current pod template and is available, and no replicas from a
// previous revision remain.
func deploymentRolloutComplete(dep *appsv1.Deployment, desired int32) bool {
	return dep.Status.ObservedGeneration == dep.Generation &&
		dep.Status.UpdatedReplicas == desired &&
		dep.Status.Replicas == dep.Status.UpdatedReplicas &&
		dep.Status.AvailableReplicas == dep.Status.UpdatedReplicas
}
