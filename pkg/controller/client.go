package controller

import (
	"context"
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

// PatchStatus sets status.observedGeneration and the observedGeneration of
// each condition to patch.Generation. The caller must set patch.Generation.
func (c *Controller) PatchStatus(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
	for i := range patch.Status.Conditions {
		patch.Status.Conditions[i].ObservedGeneration = patch.Generation
	}
	patch.ManagedFields = nil
	patch.Status.ObservedGeneration = patch.Generation
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.client.Resource(v1alpha1ClusterGVR).Namespace(patch.Namespace).Patch(ctx, patch.Name, types.ApplyPatchType, data, metadata.PatchForceOwned, "status")
	return err
}

// missingSecretStatus returns a status patch that reports the missing secret.
func missingSecretStatus(cluster *v1alpha1.SpiceDBCluster, secret types.NamespacedName) *v1alpha1.SpiceDBCluster {
	status := &v1alpha1.SpiceDBCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.SpiceDBClusterKind,
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: cluster.Namespace, Name: cluster.Name, Generation: cluster.Generation},
		Status:     *cluster.Status.DeepCopy(),
	}
	status.SetStatusCondition(v1alpha1.NewMissingSecretCondition(secret))
	return status
}

func (c *Controller) Patch(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
	// an apply patch that carries a resourceVersion is rejected with a conflict
	// if the object has changed at all - e.g. by a status patch earlier in the
	// same reconcile (as in self-pause), which would make this patch always fail
	patch.ResourceVersion = ""
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.client.Resource(v1alpha1ClusterGVR).Namespace(patch.Namespace).Patch(ctx, patch.Name, types.ApplyPatchType, data, metadata.PatchForceOwned)
	return err
}
