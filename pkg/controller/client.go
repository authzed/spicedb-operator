package controller

import (
	"context"
	"encoding/json"

	"k8s.io/apimachinery/pkg/types"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
)

func (c *Controller) PatchStatus(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
	for _, c := range patch.Status.Conditions {
		c.ObservedGeneration = patch.Generation
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

func (c *Controller) Patch(ctx context.Context, patch *v1alpha1.SpiceDBCluster) error {
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.client.Resource(v1alpha1ClusterGVR).Namespace(patch.Namespace).Patch(ctx, patch.Name, types.ApplyPatchType, data, metadata.PatchForceOwned)
	return err
}
